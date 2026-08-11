package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type GitHubIssueRuntimeProvider interface {
	ResolveRepo(ctx context.Context, repoURL, repoPath string) (*GitHubRepoRef, error)
	DefaultBranch(ctx context.Context, repo *GitHubRepoRef) (string, error)
	PublishBranch(ctx context.Context, repo *GitHubRepoRef, publishReq GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error)
	GetPullRequest(ctx context.Context, repo *GitHubRepoRef, number int) (*GitHubPullRequest, error)
	FindPullRequestByBranch(ctx context.Context, repo *GitHubRepoRef, branch string) (*GitHubPullRequest, error)
	CreatePullRequest(ctx context.Context, repo *GitHubRepoRef, createReq GitHubCreatePullRequestRequest) (*GitHubPullRequest, error)
	EnsureIssueLabels(ctx context.Context, repo *GitHubRepoRef, labels []string) error
	CreateIssue(ctx context.Context, repo *GitHubRepoRef, createReq GitHubCreateIssueRequest) (*GitHubIssue, error)
	GetIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubIssue, error)
	GetAuthenticatedUser(ctx context.Context) (*GitHubAuthenticatedUser, error)
	GetAuthenticatedUserForRepo(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, error)
	ListAuthenticatedAssignedIssues(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error)
	ListAssignedIssues(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssue, error)
	ListAssignedIssuesWithPullRequests(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssueWithPullRequest, error)
	FindPullRequestForIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubPullRequest, error)
	ListPullRequestFeedback(ctx context.Context, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error)
	CommentOnIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, bodyText string) error
	AddLabelsToIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, labels []string) error
	GlobalAPIEndpoint(ctx context.Context) string
}

const (
	automationGitHubIssueDedupLeaseDuration      = 2 * time.Minute
	automationGitHubIssueDedupPersistenceTimeout = 5 * time.Second
)

type githubIssueRuntimeOptions struct {
	ProjectID                string
	ProjectRepo              *repository.ProjectRepo
	TaskRepo                 *repository.TaskRepo
	TaskPullRequestRepo      *repository.TaskPullRequestRepo
	GitHubPRFeedbackRepo     *repository.GitHubPRFeedbackRepo
	GitHubAuthRepo           *repository.GitHubAuthRepo
	ThreadInputRepo          *repository.ThreadInputRepo
	AutomationRepo           *repository.AutomationRepo
	GitHub                   GitHubIssueRuntimeProvider
	AfterPRFeedbackForwarded func(taskID string)
	AfterPullRequestOpened   func(taskID string, result *OpenTaskPullRequestResult)
	TaskCreatedVia           string
	SuppressIssueComments    bool
}

type githubCreateIssueRuntimeInput struct {
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Labels         []string `json:"labels"`
	Assignees      []string `json:"assignees"`
	RepoURL        string   `json:"repo_url"`
	IdempotencyKey string   `json:"idempotency_key"`
}

func buildGitHubIssueRuntimeTools(opts githubIssueRuntimeOptions) *llmcontracts.RuntimeTools {
	if opts.GitHub == nil || opts.ProjectRepo == nil || strings.TrimSpace(opts.ProjectID) == "" {
		return nil
	}
	defs := gitHubIssueRuntimeToolDefs(opts.SuppressIssueComments)
	if len(defs) == 0 {
		return nil
	}
	handlers := buildGitHubIssueRuntimeHandlers(opts)
	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    chatcontrol.BuildRuntimeToolExecutorForActions(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, handlers, runtimeToolDefinitionSet(defs)),
	}
}

func gitHubIssueRuntimeToolDefs(suppressIssueComments bool) []llmcontracts.RuntimeToolDefinition {
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, false)
	filtered := make([]llmcontracts.RuntimeToolDefinition, 0, 6)
	for _, def := range defs {
		if strings.HasPrefix(strings.ToLower(def.Name), "github_") && (!suppressIssueComments || def.Name != "github_comment_on_issue") {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

func runtimeToolDefinitionSet(defs []llmcontracts.RuntimeToolDefinition) map[string]bool {
	out := make(map[string]bool, len(defs))
	for _, def := range defs {
		name := strings.ToLower(strings.TrimSpace(def.Name))
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func buildGitHubIssueRuntimeHandlers(opts githubIssueRuntimeOptions) map[string]chatcontrol.RuntimeActionHandler {
	core := NewGitHubIssueActionCore(opts.GitHub, opts.GitHubAuthRepo, opts.ProjectID, chatcontrol.DecodeRuntimeToolInput,
		func(ctx context.Context, repoURL string) (*GitHubRepoRef, error) {
			return resolveGitHubRepoForRuntimeToolURL(ctx, opts, repoURL)
		})
	assignedIssueDetails := newGitHubAssignedIssueDetailHydrator(opts.GitHub)
	postprocessAssigned := func(ctx context.Context, repo *GitHubRepoRef, issues []GitHubIssue) ([]GitHubIssue, error) {
		filtered, err := filterGitHubAssignedIssuesForAutomationInbox(ctx, opts, repo, issues)
		if err != nil {
			return nil, err
		}
		hydrated, err := assignedIssueDetails.hydrate(ctx, repo, filtered)
		if err != nil {
			return nil, err
		}
		if err := recordGitHubAssignedIssues(ctx, opts, repo, hydrated); err != nil {
			return nil, err
		}
		return hydrated, nil
	}
	return map[string]chatcontrol.RuntimeActionHandler{
		"github_create_issue": func(ctx context.Context, input json.RawMessage) (string, error) {
			var req githubCreateIssueRuntimeInput
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			automationBound, err := applyAutomationGitHubIssueConfiguration(ctx, opts, &req)
			if err != nil {
				return "", err
			}
			repo, err := resolveGitHubRepoForRuntimeToolURL(ctx, opts, req.RepoURL)
			if err != nil {
				return "", err
			}
			requiredLabels := []string(nil)
			if automationBound && len(req.Labels) > 0 {
				requiredLabels = append(requiredLabels, req.Labels...)
				if err := opts.GitHub.EnsureIssueLabels(ctx, repo, requiredLabels); err != nil {
					return "", fmt.Errorf("ensuring GitHub issue labels: %w", err)
				}
			}
			activityKey := githubIssueCreationActivityKey(ctx, repo, req)
			automationContext, hasAutomationContext := AutomationContextFromContext(ctx)
			var reservedBindings []models.AutomationBinding
			var reconciliationBindings []models.AutomationBinding
			reservationNeedsReconciliation := false
			if opts.AutomationRepo != nil && hasAutomationContext {
				for _, binding := range automationContext.Bindings {
					if binding.InvocationID == "" {
						continue
					}
					effectiveBinding, bindingErr := effectiveAutomationGitHubIssueBinding(ctx, opts, binding)
					if bindingErr != nil {
						return "", bindingErr
					}
					if effectiveBinding.InvocationID == "" {
						continue
					}
					issueNode, nodeErr := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, effectiveBinding.AutomationID, effectiveBinding.VersionID, effectiveBinding.NodeID, "create_github_issue", true)
					if nodeErr != nil {
						return "", nodeErr
					}
					if issueNode == nil {
						continue
					}
					effectiveBinding.NodeID = issueNode.ID
					resourceID, reserveErr := opts.AutomationRepo.ReserveExternalActivity(ctx, opts.ProjectID, effectiveBinding,
						activityKey, "create_github_issue", "github_issue")
					if errors.Is(reserveErr, repository.ErrAutomationExternalReconciliation) {
						reservationNeedsReconciliation = true
						reconciliationBindings = append(reconciliationBindings, effectiveBinding)
						continue
					}
					if reserveErr != nil {
						return "", reserveErr
					}
					if resourceID != "" {
						reservationNeedsReconciliation = true
						reconciliationBindings = append(reconciliationBindings, effectiveBinding)
						continue
					}
					reservedBindings = append(reservedBindings, effectiveBinding)
				}
			}
			var dedupFingerprint, dedupOwner string
			var dedupClaim repository.AutomationGitHubIssueDedupClaim
			if automationBound {
				if opts.AutomationRepo == nil {
					return "", errors.New("automation repository unavailable for GitHub issue duplicate protection")
				}
				taskID, executionID, hasExecution := AutomationExecutionFromContext(ctx)
				if !hasAutomationContext || !hasExecution {
					return "", errors.New("complete Automation source is required for GitHub issue creation")
				}
				dedupFingerprint = githubIssueDedupFingerprint(req.Title, req.IdempotencyKey)
				dedupOwner = activityKey
				dedupClaim, err = opts.AutomationRepo.AcquireGitHubIssueDedupLease(ctx, opts.ProjectID, repo.FullName, dedupFingerprint,
					dedupOwner, repository.AutomationGitHubIssueDedupSource{Context: automationContext, TaskID: taskID, ExecutionID: executionID},
					time.Now().UTC(), automationGitHubIssueDedupLeaseDuration)
				if err != nil {
					if releaseErr := releaseGitHubIssueActivityReservationsDetached(opts, reservedBindings, activityKey); releaseErr != nil {
						return "", releaseErr
					}
					return "", err
				}
				if dedupClaim.IssueNumber > 0 {
					issue := githubIssueFromCanonicalResource(githubIssueResourceID(repo, dedupClaim.IssueNumber))
					if len(requiredLabels) > 0 {
						issue, err = ensureReusedGitHubIssueLabels(ctx, opts.GitHub, repo, dedupClaim.IssueNumber, requiredLabels)
						if err != nil {
							_ = releaseGitHubIssueActivityReservationsDetached(opts, reservedBindings, activityKey)
							return "", fmt.Errorf("%w: verifying reused GitHub issue #%d labels: %v",
								repository.ErrAutomationExternalReconciliation, dedupClaim.IssueNumber, err)
						}
					}
					_, projectionErr := repairAutomationGitHubIssueProjection(opts, repo, req.Title, dedupClaim)
					if projectionErr != nil {
						_ = releaseGitHubIssueActivityReservationsDetached(opts, reservedBindings, activityKey)
						return "", fmt.Errorf("%w: repairing created GitHub issue #%d projection: %v", repository.ErrAutomationExternalReconciliation, dedupClaim.IssueNumber, projectionErr)
					}
					cleanupBindings := append(reservedBindings, reconciliationBindings...)
					if releaseErr := releaseGitHubIssueActivityReservationsDetached(opts, cleanupBindings, activityKey); releaseErr != nil {
						return "", releaseErr
					}
					return githubIssueRuntimeJSON(map[string]any{"ok": true, "issue": issue, "reused": true})
				}
				if reservationNeedsReconciliation {
					releaseCtx, cancel := context.WithTimeout(context.Background(), automationGitHubIssueDedupPersistenceTimeout)
					_ = opts.AutomationRepo.ReleaseGitHubIssueDedupLease(releaseCtx, opts.ProjectID, repo.FullName, dedupFingerprint, dedupOwner)
					cancel()
					if releaseErr := releaseGitHubIssueActivityReservationsDetached(opts, reservedBindings, activityKey); releaseErr != nil {
						return "", releaseErr
					}
					return "", repository.ErrAutomationExternalReconciliation
				}
				if err := opts.AutomationRepo.MarkGitHubIssueDedupDispatched(ctx, opts.ProjectID, repo.FullName, dedupFingerprint, dedupOwner); err != nil {
					releaseCtx, cancel := context.WithTimeout(context.Background(), automationGitHubIssueDedupPersistenceTimeout)
					_ = opts.AutomationRepo.ReleaseGitHubIssueDedupLease(releaseCtx, opts.ProjectID, repo.FullName, dedupFingerprint, dedupOwner)
					cancel()
					if releaseErr := releaseGitHubIssueActivityReservationsDetached(opts, reservedBindings, activityKey); releaseErr != nil {
						return "", releaseErr
					}
					return "", err
				}
			}
			issue, err := opts.GitHub.CreateIssue(ctx, repo, GitHubCreateIssueRequest{Title: req.Title, Body: req.Body, Labels: req.Labels, Assignees: req.Assignees})
			if err != nil {
				if dedupFingerprint != "" {
					return "", fmt.Errorf("%w: GitHub issue creation outcome is uncertain: %v", repository.ErrAutomationExternalReconciliation, err)
				}
				return "", err
			}
			if issue == nil {
				if dedupFingerprint != "" {
					return "", fmt.Errorf("%w: GitHub issue creation returned no issue", repository.ErrAutomationExternalReconciliation)
				}
				return "", errors.New("GitHub issue creation returned no issue")
			}
			if missing := missingGitHubIssueLabels(requiredLabels, issue.Labels); len(missing) > 0 {
				return "", fmt.Errorf("%w: created GitHub issue #%d is missing required category labels: %s",
					repository.ErrAutomationExternalReconciliation, issue.Number, strings.Join(missing, ", "))
			}
			if dedupFingerprint != "" {
				persistenceCtx, cancel := context.WithTimeout(context.Background(), automationGitHubIssueDedupPersistenceTimeout)
				completeErr := opts.AutomationRepo.CompleteGitHubIssueDedupLease(persistenceCtx, opts.ProjectID, repo.FullName, dedupFingerprint, dedupOwner, issue.Number)
				cancel()
				if completeErr != nil {
					return "", fmt.Errorf("%w: recording created GitHub issue #%d locally: %v", repository.ErrAutomationExternalReconciliation, issue.Number, completeErr)
				}
				dedupClaim.IssueNumber = issue.Number
				if _, projectionErr := repairAutomationGitHubIssueProjection(opts, repo, req.Title, dedupClaim); projectionErr != nil {
					return "", fmt.Errorf("%w: recording created GitHub issue #%d projection: %v", repository.ErrAutomationExternalReconciliation, issue.Number, projectionErr)
				}
			} else if _, err := recordGitHubIssueCreated(ctx, opts, repo, issue, activityKey); err != nil {
				return "", err
			}
			return githubIssueRuntimeJSON(map[string]any{"ok": true, "issue": issue})
		},
		"github_get_issue":           core.ExecuteGetIssue,
		"github_get_project_inbox":   core.ExecuteGetProjectInbox,
		"github_is_actor_authorized": core.ExecuteIsActorAuthorized,
		"github_list_my_assigned_issues": func(ctx context.Context, input json.RawMessage) (string, error) {
			return core.ExecuteListMyAssignedIssues(ctx, input, postprocessAssigned)
		},
		"github_list_assigned_issues": func(ctx context.Context, input json.RawMessage) (string, error) {
			return core.ExecuteListAssignedIssues(ctx, input, postprocessAssigned)
		},
		"github_list_assigned_issues_with_prs": core.ExecuteListAssignedIssuesWithPRs,
		"github_comment_on_issue":              core.ExecuteCommentOnIssue,
		"github_add_issue_labels":              core.ExecuteAddIssueLabels,
		"github_open_pull_request": func(ctx context.Context, input json.RawMessage) (string, error) {
			if opts.TaskPullRequestRepo == nil {
				return "", fmt.Errorf("task pull request repository unavailable")
			}
			if opts.TaskRepo == nil {
				return "", fmt.Errorf("task repository unavailable")
			}
			var req GitHubIssueActionRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			project, err := resolveGitHubRuntimeProject(ctx, opts)
			if err != nil {
				return "", err
			}
			task, err := resolveGitHubRuntimeTask(ctx, opts.TaskRepo, opts.ProjectID, req.TaskID, req.Title)
			if err != nil {
				return "", err
			}
			automationBound, err := applyAutomationPullRequestConfiguration(ctx, opts, task, &req, true)
			if err != nil {
				return "", err
			}
			if automationBound {
				requiresStructuredBody, err := automationPullRequestRequiresStructuredBody(ctx, opts)
				if err != nil {
					return "", err
				}
				if requiresStructuredBody {
					trustedIssue, err := automationGitHubSDLCSourceIssue(ctx, opts, project, task)
					if err != nil {
						return "", err
					}
					if req.IssueNumber != trustedIssue.Number {
						return "", fmt.Errorf("GitHub SDLC pull request issue_number must match the task's source issue #%d", trustedIssue.Number)
					}
					if suppliedIssueURL := strings.TrimSpace(req.IssueURL); suppliedIssueURL != "" && suppliedIssueURL != trustedIssue.URL {
						return "", fmt.Errorf("GitHub SDLC pull request issue_url must match the task's source issue %s", trustedIssue.URL)
					}
					req.IssueURL = trustedIssue.URL
					if err := validateGitHubSDLCReviewPRBody(req.PRBody, trustedIssue.Number); err != nil {
						return "", err
					}
				}
			}
			var issueNumber *int
			if req.IssueNumber > 0 {
				issueNumber = &req.IssueNumber
			}
			pullRequestService := NewTaskPullRequestService(opts.GitHub, opts.TaskPullRequestRepo)
			var result *OpenTaskPullRequestResult
			if automationBound {
				result, err = pullRequestService.OpenForAutomationTask(ctx, project, task, OpenTaskPullRequestOptions{
					Title: req.PRTitle, Body: req.PRBody, Base: req.Base, Draft: req.Draft, IssueNumber: issueNumber, IssueURL: req.IssueURL,
				})
			} else {
				result, err = pullRequestService.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{
					Title: req.PRTitle, Body: req.PRBody, Base: req.Base, Draft: req.Draft, IssueNumber: issueNumber, IssueURL: req.IssueURL,
				})
			}
			if err != nil {
				return "", err
			}
			var repoRef *GitHubRepoRef
			if automationBound {
				repoRef, err = resolveAutomationProjectGitHubRepository(ctx, opts.GitHub, project)
			} else {
				repoRef, err = opts.GitHub.ResolveRepo(ctx, project.RepoURL, project.RepoPath)
			}
			if err != nil {
				return "", err
			}
			if err := ConfigureGitHubRepoEndpoint(repoRef, opts.GitHub.GlobalAPIEndpoint(ctx)); err != nil {
				return "", err
			}
			if err := recordGitHubPullRequestOpened(ctx, opts, repoRef, task, req, result); err != nil {
				return "", err
			}
			if opts.AfterPullRequestOpened != nil {
				opts.AfterPullRequestOpened(task.ID, result)
			}
			return githubIssueRuntimeJSON(map[string]any{"ok": true, "task_id": task.ID, "pull_request": result.PullRequest, "reused_existing_record": result.ReusedExistingRecord, "reused_remote": result.ReusedRemote, "created": result.Created})
		},
		"github_replace_pull_request_branch": func(ctx context.Context, input json.RawMessage) (string, error) {
			if opts.TaskPullRequestRepo == nil {
				return "", fmt.Errorf("task pull request repository unavailable")
			}
			if opts.TaskRepo == nil {
				return "", fmt.Errorf("task repository unavailable")
			}
			var req GitHubIssueActionRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			if !req.ConfirmHistoryRewrite {
				return "", fmt.Errorf("confirm_history_rewrite must be true to replace pull request branch history")
			}
			project, err := resolveGitHubRuntimeProject(ctx, opts)
			if err != nil {
				return "", err
			}
			task, err := resolveGitHubRuntimeTask(ctx, opts.TaskRepo, opts.ProjectID, req.TaskID, req.Title)
			if err != nil {
				return "", err
			}
			automationBound, err := applyAutomationPullRequestConfiguration(ctx, opts, task, &req, false)
			if err != nil {
				return "", err
			}
			pullRequestService := NewTaskPullRequestService(opts.GitHub, opts.TaskPullRequestRepo)
			var record *models.TaskPullRequest
			if automationBound {
				record, err = pullRequestService.ReplaceBranchHeadForAutomationTask(ctx, project, task, req.ExpectedHeadSHA)
			} else {
				record, err = pullRequestService.ReplaceBranchHeadForTask(ctx, project, task, req.ExpectedHeadSHA)
			}
			if err != nil {
				return "", err
			}
			return githubIssueRuntimeJSON(map[string]any{
				"ok":                true,
				"task_id":           task.ID,
				"pull_request":      record,
				"replaced_branch":   task.WorktreeBranch,
				"expected_head_sha": strings.ToLower(strings.TrimSpace(req.ExpectedHeadSHA)),
			})
		},
		"github_forward_pr_feedback_to_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			if opts.TaskPullRequestRepo == nil || opts.GitHubPRFeedbackRepo == nil || opts.GitHubAuthRepo == nil || opts.ThreadInputRepo == nil {
				return "", fmt.Errorf("github pr feedback forwarding dependencies unavailable")
			}
			var req GitHubIssueActionRequest
			if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
				return "", err
			}
			repo, err := resolveGitHubRepoForRuntimeToolURL(ctx, opts, req.RepoURL)
			if err != nil {
				return "", err
			}
			result, err := NewGitHubPRFeedbackForwarder(opts.GitHub, opts.TaskPullRequestRepo, opts.GitHubPRFeedbackRepo, opts.GitHubAuthRepo, opts.ThreadInputRepo).ForwardAuthorizedFeedback(ctx, opts.ProjectID, repo)
			if err != nil {
				return "", err
			}
			if opts.AfterPRFeedbackForwarded != nil {
				seen := map[string]bool{}
				for _, forwarded := range result.Forwarded {
					if strings.TrimSpace(forwarded.TaskID) == "" || seen[forwarded.TaskID] {
						continue
					}
					seen[forwarded.TaskID] = true
					opts.AfterPRFeedbackForwarded(forwarded.TaskID)
				}
			}
			return githubIssueRuntimeJSON(map[string]any{"ok": true, "result": result})
		},
	}
}

func missingGitHubIssueLabels(required, actual []string) []string {
	present := make(map[string]bool, len(actual))
	for _, label := range actual {
		label = strings.ToLower(strings.TrimSpace(label))
		if label != "" {
			present[label] = true
		}
	}
	missing := make([]string, 0)
	for _, label := range required {
		label = strings.TrimSpace(label)
		if label != "" && !present[strings.ToLower(label)] {
			missing = append(missing, label)
		}
	}
	return missing
}

func ensureReusedGitHubIssueLabels(ctx context.Context, provider GitHubIssueRuntimeProvider, repo *GitHubRepoRef, issueNumber int, requiredLabels []string) (*GitHubIssue, error) {
	issue, err := provider.GetIssue(ctx, repo, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("reading reused GitHub issue: %w", err)
	}
	if issue == nil || issue.Number != issueNumber {
		return nil, errors.New("reused GitHub issue could not be confirmed")
	}
	missing := missingGitHubIssueLabels(requiredLabels, issue.Labels)
	if len(missing) == 0 {
		return issue, nil
	}
	if err := provider.AddLabelsToIssue(ctx, repo, issueNumber, missing); err != nil {
		return nil, fmt.Errorf("restoring required category labels: %w", err)
	}
	issue, err = provider.GetIssue(ctx, repo, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("confirming reused GitHub issue labels: %w", err)
	}
	if issue == nil || issue.Number != issueNumber {
		return nil, errors.New("reused GitHub issue could not be confirmed after label repair")
	}
	if missing = missingGitHubIssueLabels(requiredLabels, issue.Labels); len(missing) > 0 {
		return nil, fmt.Errorf("reused GitHub issue is missing required category labels after repair: %s", strings.Join(missing, ", "))
	}
	return issue, nil
}

func effectiveAutomationGitHubIssueBinding(ctx context.Context, opts githubIssueRuntimeOptions, binding models.AutomationBinding) (models.AutomationBinding, error) {
	current, err := opts.AutomationRepo.IsCurrentActiveBinding(ctx, opts.ProjectID, binding)
	if err != nil {
		return models.AutomationBinding{}, err
	}
	if current {
		return binding, nil
	}
	if strings.TrimSpace(binding.InvocationID) != "" {
		launchedBinding, ok, err := opts.AutomationRepo.CurrentActiveBindingForLaunchNode(ctx, opts.ProjectID, binding, "create_github_issue")
		if err != nil {
			return models.AutomationBinding{}, err
		}
		if ok {
			return launchedBinding, nil
		}
	}
	if automationID, nodeKey, createdViaOK := automationCreatedViaNode(opts.TaskCreatedVia); createdViaOK && automationID == binding.AutomationID {
		createdViaBinding, createdViaOK, createdViaErr := opts.AutomationRepo.CurrentActiveBindingForNodeKey(ctx, opts.ProjectID, automationID, nodeKey, "create_github_issue")
		if createdViaErr != nil {
			return models.AutomationBinding{}, createdViaErr
		}
		if createdViaOK {
			return createdViaBinding, nil
		}
	}
	return models.AutomationBinding{}, errors.New("github_create_issue is not authorized by the caller's current active Automation graph")
}

func automationCreatedViaNode(createdVia string) (string, string, bool) {
	parts := strings.Split(strings.TrimSpace(createdVia), ":")
	if len(parts) < 3 || parts[0] != "automation" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
		return "", "", false
	}
	return strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), true
}

func applyAutomationGitHubIssueConfiguration(ctx context.Context, opts githubIssueRuntimeOptions, req *githubCreateIssueRuntimeInput) (bool, error) {
	automationContext, automationBound := AutomationContextFromContext(ctx)
	if !automationBound || automationContext.ProjectID != opts.ProjectID {
		return false, nil
	}
	if opts.AutomationRepo == nil || req == nil {
		return false, errors.New("Automation GitHub issue authorization is unavailable")
	}
	var configuredLabels []string
	configured := false
	var maintainedLabels []string
	maintainedConfigured := false
	actionAuthorized := false
	for _, binding := range automationContext.Bindings {
		effectiveBinding, err := effectiveAutomationGitHubIssueBinding(ctx, opts, binding)
		if err != nil {
			return false, err
		}
		issueNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, effectiveBinding.AutomationID, effectiveBinding.VersionID, effectiveBinding.NodeID, "create_github_issue", true)
		if err != nil {
			return false, err
		}
		if issueNode == nil {
			return false, errors.New("github_create_issue is not authorized by the caller's Automation graph: every causal binding must authorize the action")
		}
		actionAuthorized = true
		finderLabels, err := maintainedGitHubSDLCFinderLabels(ctx, opts, effectiveBinding)
		if err != nil {
			return false, err
		}
		if len(finderLabels) > 0 {
			if maintainedConfigured && strings.Join(maintainedLabels, "\x00") != strings.Join(finderLabels, "\x00") {
				return false, errors.New("Automation bindings have conflicting GitHub finder categories")
			}
			maintainedLabels = finderLabels
			maintainedConfigured = true
		}
		assignmentNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, effectiveBinding.AutomationID, effectiveBinding.VersionID, issueNode.ID, "github_assignment", true)
		if err != nil {
			return false, err
		}
		if assignmentNode != nil && len(req.Assignees) > 0 {
			return false, errors.New("this Automation requires human GitHub assignment; github_create_issue cannot assign the issue")
		}
		var config map[string]any
		if err := json.Unmarshal([]byte(issueNode.ConfigJSON), &config); err != nil {
			return false, fmt.Errorf("decoding GitHub issue node configuration: %w", err)
		}
		labels, exists := config["labels"]
		if !exists {
			continue
		}
		parsed, valid := draftStringSlice(labels)
		if !valid {
			return false, errors.New("published GitHub issue labels are invalid")
		}
		parsed = normalizeDraftReferences(parsed)
		if configured && strings.Join(configuredLabels, "\x00") != strings.Join(parsed, "\x00") {
			return false, errors.New("Automation bindings have conflicting GitHub issue label configuration")
		}
		configuredLabels = parsed
		configured = true
	}
	if !actionAuthorized {
		return false, errors.New("github_create_issue is not authorized by the caller's Automation graph")
	}
	if key := strings.Join(strings.Fields(strings.TrimSpace(req.IdempotencyKey)), " "); len(key) > 200 {
		return false, errors.New("GitHub issue idempotency_key must be at most 200 characters")
	} else {
		req.IdempotencyKey = key
	}
	req.RepoURL = ""
	if configured {
		req.Labels = configuredLabels
		req.Assignees = nil
	}
	if maintainedConfigured {
		req.Labels = normalizeDraftReferences(append(append([]string{}, maintainedLabels...), req.Labels...))
	}
	return true, nil
}

func maintainedGitHubSDLCFinderLabels(ctx context.Context, opts githubIssueRuntimeOptions, binding models.AutomationBinding) ([]string, error) {
	definition, err := opts.AutomationRepo.GetDefinition(ctx, opts.ProjectID, binding.AutomationID)
	if err != nil {
		return nil, err
	}
	if definition == nil || definition.Version.ID != binding.VersionID || definition.Version.AdapterKey != AutomationAdapterGitHubSDLC {
		return nil, nil
	}
	for _, node := range definition.Nodes {
		if node.ID != binding.NodeID {
			continue
		}
		switch node.Role {
		case "offering_manager":
			return []string{"suggestion", "feature"}, nil
		case "bug_finder":
			return []string{"bug"}, nil
		case "optimization_finder":
			return []string{"performance"}, nil
		case "redundancy_finder":
			return []string{"duplication"}, nil
		default:
			return nil, nil
		}
	}
	return nil, errors.New("Automation GitHub finder binding node was not found")
}

func automationPullRequestRequiresStructuredBody(ctx context.Context, opts githubIssueRuntimeOptions) (bool, error) {
	automationContext, automationBound := AutomationContextFromContext(ctx)
	if !automationBound || automationContext.ProjectID != opts.ProjectID {
		return false, nil
	}
	if opts.AutomationRepo == nil {
		return false, errors.New("Automation repository unavailable for pull request body validation")
	}
	if automationContext.OriginTask {
		taskID, _, hasExecution := AutomationExecutionFromContext(ctx)
		if !hasExecution {
			return false, errors.New("Automation pull request action requires exact causal task provenance")
		}
		provenance, err := opts.AutomationRepo.GitHubIssueTaskProvenance(ctx, opts.ProjectID, taskID)
		if err != nil {
			return false, err
		}
		if provenance == nil {
			return false, errors.New("Automation GitHub implementation task has no trusted source issue")
		}
		definition, err := opts.AutomationRepo.GetDefinition(ctx, opts.ProjectID, provenance.AutomationID)
		if err != nil {
			return false, err
		}
		if definition == nil || definition.Automation.LifecycleState != models.AutomationActive || definition.Automation.PublishedVersionID == nil {
			return false, errors.New("current Automation definition is unavailable for pull request body validation")
		}
		return definition.Version.AdapterKey == AutomationAdapterGitHubSDLC, nil
	}
	for _, binding := range automationContext.Bindings {
		definition, err := opts.AutomationRepo.GetDefinition(ctx, opts.ProjectID, binding.AutomationID)
		if err != nil {
			return false, err
		}
		if definition == nil || definition.Version.ID != binding.VersionID {
			return false, errors.New("current Automation definition is unavailable for pull request body validation")
		}
		if definition.Version.AdapterKey == AutomationAdapterGitHubSDLC {
			return true, nil
		}
	}
	return false, nil
}

func automationGitHubSDLCSourceIssue(ctx context.Context, opts githubIssueRuntimeOptions, project *models.Project, task *models.Task) (*GitHubIssue, error) {
	if opts.AutomationRepo == nil || task == nil {
		return nil, errors.New("Automation GitHub source issue provenance is unavailable")
	}
	resourceID, err := opts.AutomationRepo.GitHubIssueResourceForTask(ctx, opts.ProjectID, task.ID)
	if err != nil {
		return nil, fmt.Errorf("loading Automation GitHub source issue provenance: %w", err)
	}
	parts := strings.Split(resourceID, ":")
	issue := githubIssueFromCanonicalResource(resourceID)
	if len(parts) != 4 || strings.TrimSpace(parts[1]) == "" || issue.Number <= 0 {
		return nil, errors.New("Automation GitHub implementation task has no trusted source issue")
	}
	repo, err := resolveAutomationProjectGitHubRepository(ctx, opts.GitHub, project)
	if err != nil {
		return nil, fmt.Errorf("resolving Automation GitHub source issue repository: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(parts[1]), strings.TrimSpace(repo.FullName)) {
		return nil, errors.New("Automation GitHub source issue repository does not match the selected project repository")
	}
	repoURL, err := url.Parse(repo.HTMLURL)
	if err != nil || repoURL.Scheme != "https" || repoURL.Host == "" {
		return nil, errors.New("Automation GitHub source issue repository has no valid web URL")
	}
	issue.URL = fmt.Sprintf("https://%s/%s/issues/%d", repoURL.Host, parts[1], issue.Number)
	return issue, nil
}

func validateGitHubSDLCReviewPRBody(body string, issueNumber int) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return errors.New("GitHub SDLC pull requests require pr_body with a factual summary and validation")
	}
	lowerBody := strings.ToLower(body)
	if !strings.Contains(lowerBody, "## summary") || !strings.Contains(lowerBody, "## validation") {
		return errors.New("GitHub SDLC pr_body must include ## Summary and ## Validation sections")
	}
	if issueNumber <= 0 {
		return errors.New("GitHub SDLC pull requests require the source issue number")
	}
	if !strings.Contains(lowerBody, fmt.Sprintf("closes #%d", issueNumber)) {
		return fmt.Errorf("GitHub SDLC pr_body must include Closes #%d", issueNumber)
	}
	return nil
}

type automationPullRequestConfig struct {
	base  string
	draft bool
}

func applyAutomationPullRequestConfiguration(ctx context.Context, opts githubIssueRuntimeOptions, task *models.Task, req *GitHubIssueActionRequest, allowDurableProvenance bool) (bool, error) {
	automationContext, automationBound := AutomationContextFromContext(ctx)
	if !automationBound || automationContext.ProjectID != opts.ProjectID {
		return false, nil
	}
	if opts.AutomationRepo == nil || task == nil || req == nil {
		return false, errors.New("Automation pull request authorization is unavailable")
	}
	callerTaskID, _, hasExecution := AutomationExecutionFromContext(ctx)
	if !hasExecution || strings.TrimSpace(callerTaskID) == "" {
		return false, errors.New("Automation pull request action requires exact causal task provenance")
	}
	if callerTaskID != task.ID {
		return false, errors.New("Automation pull request action cannot mutate a different task")
	}
	if automationContext.OriginTask && allowDurableProvenance {
		provenance, err := opts.AutomationRepo.GitHubIssueTaskProvenance(ctx, opts.ProjectID, task.ID)
		if err != nil {
			return false, err
		}
		if provenance == nil {
			return false, errors.New("github pull request action is not authorized by trusted Automation task provenance")
		}
		configured, err := currentPullRequestConfigForProvenance(ctx, opts, provenance, req)
		if err != nil {
			return false, err
		}
		req.Base = configured.base
		req.Draft = configured.draft
		return true, nil
	}
	var configured *automationPullRequestConfig
	for _, binding := range automationContext.Bindings {
		currentBinding, currentErr := opts.AutomationRepo.IsCurrentActiveBinding(ctx, opts.ProjectID, binding)
		if currentErr != nil {
			return false, currentErr
		}
		if !currentBinding {
			return false, errors.New("github pull request action is not authorized by the caller's current active Automation graph")
		}
		node, nodeErr := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, binding.AutomationID, binding.VersionID, binding.NodeID, "open_pull_request", true)
		if nodeErr != nil {
			return false, nodeErr
		}
		if node == nil {
			return false, errors.New("github pull request action is not authorized by the caller's Automation graph: every causal binding must authorize the action")
		}
		current, err := pullRequestConfigFromNode(node, req)
		if err != nil {
			return false, err
		}
		if configured != nil && *configured != current {
			return false, errors.New("Automation bindings have conflicting pull request configuration")
		}
		configured = &current
	}
	if configured == nil {
		return false, errors.New("github pull request action is not authorized by the caller's Automation graph")
	}
	req.Base = configured.base
	req.Draft = configured.draft
	return true, nil
}

func currentPullRequestConfigForProvenance(ctx context.Context, opts githubIssueRuntimeOptions, provenance *repository.AutomationGitHubIssueTaskProvenance, req *GitHubIssueActionRequest) (automationPullRequestConfig, error) {
	if provenance == nil || strings.TrimSpace(provenance.AutomationID) == "" || strings.TrimSpace(provenance.ImplementationNodeKey) == "" {
		return automationPullRequestConfig{}, errors.New("complete Automation GitHub issue task provenance is required")
	}
	definition, err := opts.AutomationRepo.GetDefinition(ctx, opts.ProjectID, provenance.AutomationID)
	if err != nil {
		return automationPullRequestConfig{}, err
	}
	if definition == nil || definition.Automation.LifecycleState != models.AutomationActive || definition.Automation.PublishedVersionID == nil {
		return automationPullRequestConfig{}, errors.New("github pull request action is not authorized by the current active Automation graph")
	}
	var implementationNode *models.AutomationNode
	for i := range definition.Nodes {
		node := &definition.Nodes[i]
		if node.NodeKey == provenance.ImplementationNodeKey && node.NodeType == models.AutomationNodeAgentTask && (node.Role == "task" || node.Role == "implementation") {
			implementationNode = node
			break
		}
	}
	if implementationNode == nil {
		return automationPullRequestConfig{}, errors.New("github pull request action is not authorized by the current Automation implementation node")
	}
	var prNode *models.AutomationNode
	for _, edge := range definition.Edges {
		if edge.SourceNodeID != implementationNode.ID {
			continue
		}
		for i := range definition.Nodes {
			node := &definition.Nodes[i]
			if node.ID == edge.TargetNodeID && node.Role == "open_pull_request" {
				if prNode != nil {
					return automationPullRequestConfig{}, errors.New("Automation implementation has ambiguous pull request targets")
				}
				prNode = node
			}
		}
	}
	if prNode == nil {
		return automationPullRequestConfig{}, errors.New("github pull request action is not authorized by the current Automation graph")
	}
	return pullRequestConfigFromNode(prNode, req)
}

func pullRequestConfigFromNode(node *models.AutomationNode, req *GitHubIssueActionRequest) (automationPullRequestConfig, error) {
	if node == nil || req == nil {
		return automationPullRequestConfig{}, errors.New("pull request node configuration is unavailable")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(node.ConfigJSON), &config); err != nil {
		return automationPullRequestConfig{}, fmt.Errorf("decoding pull request node configuration: %w", err)
	}
	base := strings.TrimSpace(req.Base)
	if rawBase, exists := config["base"]; exists {
		parsedBase, valid := rawBase.(string)
		if !valid {
			return automationPullRequestConfig{}, errors.New("published pull request base configuration is invalid")
		}
		base = strings.TrimSpace(parsedBase)
	}
	draft := req.Draft
	if rawDraft, exists := config["draft"]; exists {
		parsedDraft, valid := rawDraft.(bool)
		if !valid {
			return automationPullRequestConfig{}, errors.New("published pull request draft configuration is invalid")
		}
		draft = parsedDraft
	}
	return automationPullRequestConfig{base: base, draft: draft}, nil
}

func resolveGitHubRuntimeProject(ctx context.Context, opts githubIssueRuntimeOptions) (*models.Project, error) {
	project, err := opts.ProjectRepo.GetByID(ctx, opts.ProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, fmt.Errorf("current project not found")
	}
	return project, nil
}

func resolveGitHubRepoForRuntimeTool(ctx context.Context, opts githubIssueRuntimeOptions) (*GitHubRepoRef, error) {
	return resolveGitHubRepoForRuntimeToolURL(ctx, opts, "")
}

func resolveGitHubRepoForRuntimeToolURL(ctx context.Context, opts githubIssueRuntimeOptions, repoURL string) (*GitHubRepoRef, error) {
	automationContext, automationBound := AutomationContextFromContext(ctx)
	project, err := resolveGitHubRuntimeProject(ctx, opts)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(repoURL) != "" && (!automationBound || automationContext.ProjectID != opts.ProjectID) {
		repo, err := opts.GitHub.ResolveRepo(ctx, repoURL, "")
		if err != nil {
			return nil, err
		}
		if err := ConfigureGitHubRepoEndpointForProject(repo, repoURL, project.RepoURL, opts.GitHub.GlobalAPIEndpoint(ctx)); err != nil {
			return nil, err
		}
		return repo, nil
	}
	if automationBound && automationContext.ProjectID == opts.ProjectID {
		return resolveAutomationProjectGitHubRepository(ctx, opts.GitHub, project)
	}
	repo, err := opts.GitHub.ResolveRepo(ctx, project.RepoURL, project.RepoPath)
	if err != nil {
		return nil, err
	}
	if err := ConfigureGitHubRepoEndpoint(repo, opts.GitHub.GlobalAPIEndpoint(ctx)); err != nil {
		return nil, err
	}
	return repo, nil
}

func resolveGitHubRuntimeTask(ctx context.Context, taskRepo *repository.TaskRepo, projectID, taskID, title string) (*models.Task, error) {
	if taskRepo == nil {
		return nil, fmt.Errorf("task repository unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "current" {
		callerTaskID, _, hasExecution := AutomationExecutionFromContext(ctx)
		if !hasExecution || strings.TrimSpace(callerTaskID) == "" {
			return nil, fmt.Errorf("task_id current requires Automation task execution context")
		}
		taskID = callerTaskID
	}
	if taskID != "" {
		task, err := taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task == nil || task.ProjectID != projectID {
			return nil, fmt.Errorf("task not found in current project")
		}
		return task, nil
	}
	if strings.TrimSpace(title) != "" {
		task, err := taskRepo.GetByProjectAndTitle(ctx, projectID, strings.TrimSpace(title))
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("task not found in current project")
		}
		return task, nil
	}
	return nil, fmt.Errorf("task_id or title is required")
}

func releaseGitHubIssueActivityReservations(ctx context.Context, opts githubIssueRuntimeOptions, bindings []models.AutomationBinding, activityKey string) error {
	if opts.AutomationRepo == nil {
		return nil
	}
	for _, binding := range bindings {
		if err := opts.AutomationRepo.ReleaseExternalActivityReservation(ctx, opts.ProjectID, binding, activityKey); err != nil {
			return err
		}
	}
	return nil
}

func releaseGitHubIssueActivityReservationsDetached(opts githubIssueRuntimeOptions, bindings []models.AutomationBinding, activityKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), automationGitHubIssueDedupPersistenceTimeout)
	defer cancel()
	return releaseGitHubIssueActivityReservations(ctx, opts, bindings, activityKey)
}

func repairAutomationGitHubIssueProjection(opts githubIssueRuntimeOptions, repo *GitHubRepoRef, title string, claim repository.AutomationGitHubIssueDedupClaim) (*GitHubIssue, error) {
	if claim.IssueNumber <= 0 || strings.TrimSpace(claim.OwnerToken) == "" || claim.Source.Context.ProjectID != opts.ProjectID ||
		len(claim.Source.Context.Bindings) == 0 || strings.TrimSpace(claim.Source.TaskID) == "" || strings.TrimSpace(claim.Source.ExecutionID) == "" {
		return nil, errors.New("trusted local GitHub issue projection source is unavailable")
	}
	ctx := WithAutomationContext(context.Background(), claim.Source.Context)
	ctx = withAutomationExecution(ctx, claim.Source.TaskID, claim.Source.ExecutionID)
	ctx, cancel := context.WithTimeout(ctx, automationGitHubIssueDedupPersistenceTimeout)
	defer cancel()
	projectionIssue := githubIssueFromCanonicalResource(githubIssueResourceID(repo, claim.IssueNumber))
	projectionIssue.Title = strings.TrimSpace(title)
	recordedBindings, err := recordGitHubIssueCreated(ctx, opts, repo, projectionIssue, claim.OwnerToken)
	if err != nil {
		return nil, err
	}
	if recordedBindings != len(claim.Source.Context.Bindings) {
		return nil, fmt.Errorf("expected GitHub issue projection for %d source binding(s), recorded %d", len(claim.Source.Context.Bindings), recordedBindings)
	}
	return githubIssueFromCanonicalResource(githubIssueResourceID(repo, claim.IssueNumber)), nil
}

func githubIssueDedupFingerprint(title, idempotencyKey string) string {
	if normalizedKey := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(idempotencyKey))), " "); normalizedKey != "" {
		hash := sha256.Sum256([]byte("finding:\n" + normalizedKey))
		return fmt.Sprintf("%x", hash[:])
	}
	return githubIssueTitleFingerprint(title)
}

func githubIssueTitleFingerprint(title string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(title))), " ")
	hash := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", hash[:])
}

func githubIssueCreationActivityKey(ctx context.Context, repo *GitHubRepoRef, req githubCreateIssueRuntimeInput) string {
	_, executionID, _ := AutomationExecutionFromContext(ctx)
	payload, _ := json.Marshal(req)
	hash := sha256.Sum256(append([]byte(strings.ToLower(repo.FullName)+"\n"), payload...))
	return fmt.Sprintf("execution:%s:github-create-issue:%x", executionID, hash[:12])
}

func githubIssueResourceID(repo *GitHubRepoRef, number int) string {
	return fmt.Sprintf("github:%s:issue:%d", strings.ToLower(strings.TrimSpace(repo.FullName)), number)
}

func githubPullRequestResourceID(repo *GitHubRepoRef, number int) string {
	return fmt.Sprintf("github:%s:pull:%d", strings.ToLower(strings.TrimSpace(repo.FullName)), number)
}

func githubIssueFromCanonicalResource(resourceID string) *GitHubIssue {
	parts := strings.Split(resourceID, ":")
	if len(parts) != 4 || parts[0] != "github" || parts[2] != "issue" {
		return &GitHubIssue{}
	}
	var number int
	_, _ = fmt.Sscanf(parts[3], "%d", &number)
	return &GitHubIssue{Number: number, URL: fmt.Sprintf("https://github.com/%s/issues/%d", parts[1], number)}
}

func recordGitHubIssueCreated(ctx context.Context, opts githubIssueRuntimeOptions, repo *GitHubRepoRef, issue *GitHubIssue, activityKey string) (int, error) {
	if opts.AutomationRepo == nil || issue == nil {
		return 0, nil
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != opts.ProjectID {
		return 0, nil
	}
	taskID, executionID, _ := AutomationExecutionFromContext(ctx)
	resourceID := githubIssueResourceID(repo, issue.Number)
	recordedBindings := 0
	for _, sourceBinding := range automationContext.Bindings {
		effectiveBinding, err := effectiveAutomationGitHubIssueBinding(ctx, opts, sourceBinding)
		if err != nil {
			return recordedBindings, err
		}
		sourceNodeID := effectiveBinding.NodeID
		issueNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, effectiveBinding.AutomationID, effectiveBinding.VersionID, effectiveBinding.NodeID, "create_github_issue", true)
		if err != nil {
			return recordedBindings, err
		}
		if issueNode == nil {
			continue
		}
		assignmentNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, effectiveBinding.AutomationID, effectiveBinding.VersionID, issueNode.ID, "github_assignment", true)
		if err != nil {
			return recordedBindings, err
		}
		if assignmentNode == nil {
			return recordedBindings, errors.New("expected GitHub issue assignment projection node is unavailable")
		}
		binding := effectiveBinding
		if binding.VersionID != sourceBinding.VersionID {
			binding.InvocationID = ""
		}
		binding.NodeID = issueNode.ID
		resources := []models.AutomationActivityResource{{ResourceType: "github_issue", ResourceID: resourceID}}
		if taskID != "" {
			resources = append(resources, models.AutomationActivityResource{ResourceType: "task", ResourceID: taskID})
		}
		if executionID != "" {
			resources = append(resources, models.AutomationActivityResource{ResourceType: "execution", ResourceID: executionID})
		}
		item, _, err := opts.AutomationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: automationContext, Binding: binding, WorkItemKey: resourceID, WorkItemKind: "github_issue",
			WorkItemTitle: issue.Title, WorkItemStatus: models.AutomationWorkItemWaiting,
			ActivityKey: activityKey, ActivityType: "create_github_issue", ActivityStatus: models.AutomationActivityCompleted,
			Resources: resources, EventKey: resourceID + ":created:issue", FromNodeID: sourceNodeID,
			ToNodeID: issueNode.ID, Transition: models.AutomationTransitionEntered,
		})
		if err != nil {
			return recordedBindings, err
		}
		binding.WorkItemID = item.ID
		if _, _, err := opts.AutomationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: automationContext, Binding: binding,
			ActivityKey: activityKey, ActivityType: "create_github_issue", ActivityStatus: models.AutomationActivityCompleted,
			Resources: resources, EventKey: resourceID + ":created:assignment", FromNodeID: issueNode.ID,
			ToNodeID: assignmentNode.ID, Transition: models.AutomationTransitionWaiting,
		}); err != nil {
			return recordedBindings, err
		}
		recordedBindings++
	}
	return recordedBindings, nil
}

func filterGitHubAssignedIssuesForAutomationInbox(_ context.Context, _ githubIssueRuntimeOptions, _ *GitHubRepoRef, issues []GitHubIssue) ([]GitHubIssue, error) {
	return issues, nil
}

type githubAssignedIssueDetailHydrator struct {
	provider GitHubIssueRuntimeProvider
	mu       sync.Mutex
	cache    map[string]GitHubIssue
}

func newGitHubAssignedIssueDetailHydrator(provider GitHubIssueRuntimeProvider) *githubAssignedIssueDetailHydrator {
	return &githubAssignedIssueDetailHydrator{provider: provider, cache: make(map[string]GitHubIssue)}
}

func (h *githubAssignedIssueDetailHydrator) hydrate(ctx context.Context, repo *GitHubRepoRef, issues []GitHubIssue) ([]GitHubIssue, error) {
	if h == nil || h.provider == nil || len(issues) == 0 {
		return issues, nil
	}
	out := append([]GitHubIssue(nil), issues...)
	for i, issue := range out {
		if githubAssignedIssueHasTaskCreationFields(issue) || issue.Number <= 0 {
			continue
		}
		key := githubAssignedIssueDetailCacheKey(repo, issue.Number)
		h.mu.Lock()
		cached, ok := h.cache[key]
		h.mu.Unlock()
		if ok {
			out[i] = cached
			continue
		}
		detail, err := h.provider.GetIssue(ctx, repo, issue.Number)
		if err != nil {
			return nil, fmt.Errorf("hydrating assigned GitHub issue #%d: %w", issue.Number, err)
		}
		if detail == nil || detail.Number != issue.Number {
			return nil, fmt.Errorf("hydrating assigned GitHub issue #%d returned mismatched issue", issue.Number)
		}
		h.mu.Lock()
		h.cache[key] = *detail
		h.mu.Unlock()
		out[i] = *detail
	}
	return out, nil
}

func githubAssignedIssueHasTaskCreationFields(issue GitHubIssue) bool {
	return issue.Number > 0 && strings.TrimSpace(issue.URL) != "" && strings.TrimSpace(issue.Title) != "" && strings.TrimSpace(issue.State) != ""
}

func githubAssignedIssueDetailCacheKey(repo *GitHubRepoRef, issueNumber int) string {
	repoKey := ""
	if repo != nil {
		repoKey = strings.ToLower(strings.TrimSpace(repo.FullName))
		if repoKey == "" && strings.TrimSpace(repo.Owner) != "" && strings.TrimSpace(repo.Name) != "" {
			repoKey = strings.ToLower(strings.TrimSpace(repo.Owner)) + "/" + strings.ToLower(strings.TrimSpace(repo.Name))
		}
	}
	return fmt.Sprintf("%s#%d", repoKey, issueNumber)
}

func recordGitHubAssignedIssues(ctx context.Context, opts githubIssueRuntimeOptions, repo *GitHubRepoRef, issues []GitHubIssue) error {
	if opts.AutomationRepo == nil || len(issues) == 0 {
		return nil
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != opts.ProjectID {
		return nil
	}
	taskID, executionID, _ := AutomationExecutionFromContext(ctx)
	for _, sourceBinding := range automationContext.Bindings {
		assignmentNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, sourceBinding.NodeID, "github_assignment", false)
		if err != nil {
			return err
		}
		if assignmentNode == nil {
			continue
		}
		devInboxNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, assignmentNode.ID, "github_inbox", true)
		if err != nil {
			return err
		}
		if devInboxNode == nil || devInboxNode.ID != sourceBinding.NodeID {
			continue
		}
		for _, issue := range issues {
			resourceID := githubIssueResourceID(repo, issue.Number)
			binding := sourceBinding
			binding.NodeID = devInboxNode.ID
			resources := []models.AutomationActivityResource{{ResourceType: "github_issue", ResourceID: resourceID}}
			if taskID != "" {
				resources = append(resources, models.AutomationActivityResource{ResourceType: "task", ResourceID: taskID})
			}
			if executionID != "" {
				resources = append(resources, models.AutomationActivityResource{ResourceType: "execution", ResourceID: executionID})
			}
			if _, _, err := opts.AutomationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
				Context: automationContext, Binding: binding, WorkItemKey: resourceID, WorkItemKind: "github_issue",
				WorkItemTitle: issue.Title, WorkItemStatus: models.AutomationWorkItemWaiting,
				ActivityKey:  "invocation:" + sourceBinding.InvocationID + ":discover:" + resourceID,
				ActivityType: "discover_assigned_issue", ActivityStatus: models.AutomationActivityCompleted,
				Resources: resources, EventKey: resourceID + ":assigned", FromNodeID: assignmentNode.ID,
				ToNodeID: devInboxNode.ID, Transition: models.AutomationTransitionWaiting,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func recordGitHubPullRequestOpened(ctx context.Context, opts githubIssueRuntimeOptions, repo *GitHubRepoRef, task *models.Task, req GitHubIssueActionRequest, result *OpenTaskPullRequestResult) error {
	if opts.AutomationRepo == nil || task == nil || result == nil || result.PullRequest == nil {
		return nil
	}
	automationContext, err := opts.AutomationRepo.ContextForTask(ctx, opts.ProjectID, task.ID)
	if err != nil || len(automationContext.Bindings) == 0 {
		return err
	}
	prResourceID := githubPullRequestResourceID(repo, result.PullRequest.Number)
	for _, sourceBinding := range automationContext.Bindings {
		openPRNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, sourceBinding.NodeID, "open_pull_request", true)
		if err != nil {
			return err
		}
		if openPRNode == nil {
			continue
		}
		reviewNode, err := opts.AutomationRepo.GetConnectedNodeByRole(ctx, opts.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, openPRNode.ID, "pull_request_review", true)
		if err != nil {
			return err
		}
		if reviewNode == nil {
			continue
		}
		implementationBinding := sourceBinding
		if _, _, err := opts.AutomationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: automationContext, Binding: implementationBinding,
			ActivityKey:  "work-item:" + sourceBinding.WorkItemID + ":implementation-task:" + task.ID,
			ActivityType: "implementation_task", ActivityStatus: models.AutomationActivityCompleted,
			Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
			EventKey:  "work-item:" + sourceBinding.WorkItemID + ":implementation-completed:" + task.ID,
			ToNodeID:  sourceBinding.NodeID, Transition: models.AutomationTransitionCompleted,
		}); err != nil {
			return err
		}
		prBinding := implementationBinding
		prBinding.NodeID = openPRNode.ID
		resources := []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}, {ResourceType: "pull_request", ResourceID: prResourceID}}
		if req.IssueNumber > 0 {
			resources = append(resources, models.AutomationActivityResource{ResourceType: "github_issue", ResourceID: githubIssueResourceID(repo, req.IssueNumber)})
		}
		if _, _, err := opts.AutomationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: automationContext, Binding: prBinding,
			ActivityKey: prResourceID + ":open", ActivityType: "open_pull_request", ActivityStatus: models.AutomationActivityCompleted,
			Resources: resources, EventKey: prResourceID + ":opened", FromNodeID: sourceBinding.NodeID,
			ToNodeID: openPRNode.ID, Transition: models.AutomationTransitionEntered,
		}); err != nil {
			return err
		}
		if _, _, err := opts.AutomationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: automationContext, Binding: prBinding,
			ActivityKey: prResourceID + ":open", ActivityType: "open_pull_request", ActivityStatus: models.AutomationActivityCompleted,
			Resources: resources, EventKey: prResourceID + ":review", FromNodeID: openPRNode.ID,
			ToNodeID: reviewNode.ID, Transition: models.AutomationTransitionWaiting,
		}); err != nil {
			return err
		}
	}
	return nil
}

func githubIssueRuntimeJSON(payload map[string]any) (string, error) {
	b, err := json.Marshal(payload)
	return string(b), err
}
