package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type AutomationPullRequestProvider interface {
	ResolveRepo(ctx context.Context, repoURL, repoPath string) (*GitHubRepoRef, error)
	GetPullRequest(ctx context.Context, repo *GitHubRepoRef, number int) (*GitHubPullRequest, error)
}

type AutomationExternalStateService struct {
	automations *repository.AutomationRepo
	pulls       *repository.TaskPullRequestRepo
	projects    *repository.ProjectRepo
	github      AutomationPullRequestProvider
}

func NewAutomationExternalStateService(automations *repository.AutomationRepo, pulls *repository.TaskPullRequestRepo, projects *repository.ProjectRepo, github AutomationPullRequestProvider) *AutomationExternalStateService {
	return &AutomationExternalStateService{automations: automations, pulls: pulls, projects: projects, github: github}
}

func (s *AutomationExternalStateService) Refresh(ctx context.Context, projectID, automationID string, now time.Time) (models.AutomationExternalState, error) {
	if s == nil || s.automations == nil || s.pulls == nil || s.projects == nil || s.github == nil {
		return models.AutomationExternalState{}, errors.New("automation external refresh is unavailable")
	}
	exists, err := s.automations.Exists(ctx, projectID, automationID)
	if err != nil {
		return models.AutomationExternalState{}, err
	}
	if !exists {
		return models.AutomationExternalState{}, errors.New("automation not found")
	}
	pulls, err := s.automations.ListAutomationPullRequests(ctx, projectID, automationID, 20)
	if err != nil {
		return models.AutomationExternalState{}, err
	}
	if len(pulls) == 0 {
		return models.AutomationExternalState{}, nil
	}
	project, err := s.projects.GetByID(ctx, projectID)
	if err != nil {
		return models.AutomationExternalState{}, err
	}
	if project == nil {
		return models.AutomationExternalState{}, errors.New("project not found")
	}

	repoRef, err := resolveAutomationProjectGitHubRepository(ctx, s.github, project)
	if err != nil {
		return models.AutomationExternalState{}, err
	}
	if repoRef == nil {
		return models.AutomationExternalState{}, errors.New("project GitHub repository is unavailable")
	}
	fullName := strings.Trim(strings.TrimSpace(repoRef.FullName), "/")
	if fullName == "" {
		fullName = strings.Trim(strings.TrimSpace(repoRef.Owner), "/") + "/" + strings.Trim(strings.TrimSpace(repoRef.Name), "/")
	}
	if fullName == "/" {
		return models.AutomationExternalState{}, errors.New("project GitHub repository identity is unavailable")
	}

	cacheCutoff := now.UTC().Add(-automationExternalRefreshCache)
	for i := range pulls {
		pull := &pulls[i]
		if pull.UpdatedAt.UTC().Before(cacheCutoff) {
			live, getErr := s.github.GetPullRequest(ctx, repoRef, pull.PRNumber)
			if getErr != nil {
				return models.AutomationExternalState{}, getErr
			}
			if live == nil || live.Number != pull.PRNumber {
				return models.AutomationExternalState{}, fmt.Errorf("GitHub pull request %d was not returned", pull.PRNumber)
			}
			pull.PRState = strings.ToLower(strings.TrimSpace(live.State))
			if live.Merged {
				pull.PRState = "merged"
			}
			if strings.TrimSpace(live.URL) != "" {
				pull.PRURL = strings.TrimSpace(live.URL)
			}
			if err := s.pulls.Upsert(ctx, pull); err != nil {
				return models.AutomationExternalState{}, err
			}
		}
		resourceID := fmt.Sprintf("github:%s:pull:%d", fullName, pull.PRNumber)
		if err := s.reconcilePullRequestState(ctx, projectID, automationID, pull.TaskID, resourceID, pull.PRState); err != nil {
			return models.AutomationExternalState{}, err
		}
	}
	state, err := s.automations.AutomationExternalState(ctx, projectID, automationID, now.UTC().Add(-repository.AutomationExternalStaleAfter))
	if err != nil {
		return models.AutomationExternalState{}, err
	}
	if _, err := s.automations.RecomputeAutomationHealth(ctx, projectID, automationID, now); err != nil {
		return models.AutomationExternalState{}, err
	}
	return state, nil
}

func (s *AutomationExternalStateService) reconcilePullRequestState(ctx context.Context, projectID, automationID, taskID, resourceID, state string) error {
	state = strings.ToLower(strings.TrimSpace(state))
	if state != "merged" && state != "closed" {
		return nil
	}
	automationContext, err := s.automations.BindingsForActivityResource(ctx, projectID, automationID, "pull_request", resourceID)
	if err != nil {
		return err
	}
	for _, sourceBinding := range automationContext.Bindings {
		review, err := s.automations.GetConnectedNodeByRole(ctx, projectID, automationID, sourceBinding.VersionID, sourceBinding.NodeID, "pull_request_review", true)
		if err != nil {
			return err
		}
		if review == nil {
			continue
		}
		binding := sourceBinding
		binding.NodeID = review.ID
		event := repository.AutomationProjectionEvent{
			Context: automationContext, Binding: binding,
			ActivityKey: resourceID + ":state:" + state, ActivityType: "pull_request_state",
			Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: taskID}, {ResourceType: "pull_request", ResourceID: resourceID}},
			EventKey:  resourceID + ":state:" + state, FromNodeID: review.ID,
		}
		if state == "merged" {
			completed, nodeErr := s.automations.GetConnectedNodeByRole(ctx, projectID, automationID, sourceBinding.VersionID, review.ID, "completed", true)
			if nodeErr != nil {
				return nodeErr
			}
			if completed == nil {
				continue
			}
			event.ActivityStatus = models.AutomationActivityCompleted
			event.ToNodeID = completed.ID
			event.Transition = models.AutomationTransitionCompleted
		} else {
			event.ActivityStatus = models.AutomationActivityFailed
			event.ToNodeID = review.ID
			event.Transition = models.AutomationTransitionFailed
		}
		if _, _, err := s.automations.RecordProjectionEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
