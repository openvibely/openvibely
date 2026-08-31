package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	llmworkflow "github.com/openvibely/openvibely/internal/llm/workflow"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type workflowTaskCreator struct {
	s *LLMService
}

func (c workflowTaskCreator) Create(ctx context.Context, task *models.Task) error {
	return c.s.taskSvc.Create(ctx, task)
}

type workflowLineageResolver struct {
	s *LLMService
}

// ResolveParentLineage resolves the Git branch + commit SHA for a parent task.
// Preferred: parent worktree branch + HEAD commit SHA.
// Fallback: merge target / default branch HEAD SHA.
func (r workflowLineageResolver) ResolveParentLineage(ctx context.Context, parentTask models.Task) (string, string, error) {
	// If parent has a worktree, resolve from it
	if parentTask.WorktreePath != "" && parentTask.WorktreeBranch != "" {
		sha, err := resolveGitHEAD(parentTask.WorktreePath)
		if err != nil {
			applog.Infof("[lineage] failed to resolve HEAD in worktree %s: %v", parentTask.WorktreePath, err)
		} else {
			return parentTask.WorktreeBranch, sha, nil
		}
	}

	// Fallback: resolve from the project repo using merge target or default branch
	repoDir := ""
	if parentTask.ProjectID != "" && r.s.projectRepo != nil {
		project, err := r.s.projectRepo.GetByID(ctx, parentTask.ProjectID)
		if err == nil && project != nil {
			repoDir = project.RepoPath
		}
	}
	if repoDir == "" || !IsGitRepo(repoDir) {
		return "", "", fmt.Errorf("no git repo available for lineage resolution (project=%s)", parentTask.ProjectID)
	}

	targetBranch := parentTask.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}

	sha, err := resolveGitRef(repoDir, targetBranch)
	if err != nil {
		return "", "", fmt.Errorf("resolving ref %s in %s: %w", targetBranch, repoDir, err)
	}
	return targetBranch, sha, nil
}

// resolveGitHEAD returns the HEAD commit SHA in the given directory.
func resolveGitHEAD(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveGitRef returns the commit SHA for a named ref in the given repo.
func resolveGitRef(repoDir, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *LLMService) workflowChainService() *llmworkflow.Service {
	svc := llmworkflow.NewService(workflowTaskCreator{s: s})
	svc.SetLineageResolver(workflowLineageResolver{s: s})
	return svc
}

func defaultRunnableChildCategory(parentCategory models.TaskCategory) models.TaskCategory {
	switch parentCategory {
	case models.CategoryActive, models.CategoryBacklog:
		return parentCategory
	default:
		// Parent can be moved to completed before chain activation runs.
		// Defaulting to active preserves sequential execution behavior.
		return models.CategoryActive
	}
}

// triggerTaskChain checks if a task has chaining configured and activates the child task.
// If a blocked child was pre-created for visibility, it is activated in place.
// Otherwise, a new child task is created (fallback for chains without pre-created children).
func (s *LLMService) triggerTaskChain(ctx context.Context, parentTask models.Task, parentOutput string) error {
	// Reload latest parent from DB so chain edits made while the task was running
	// (e.g., via the chat edit_task runtime action) are respected at completion time.
	if s.taskRepo != nil {
		if latest, getErr := s.taskRepo.GetByID(ctx, parentTask.ID); getErr != nil {
			applog.Infof("[agent-svc] triggerTaskChain error loading latest parent task=%s: %v", parentTask.ID, getErr)
		} else if latest != nil {
			parentTask = *latest
		}
	}

	if handled, handoffErr := s.activatePublishedCustomAutomationChild(ctx, parentTask, parentOutput); handled {
		return handoffErr
	}
	config, err := parentTask.ParseChainConfig()
	if err != nil || !config.Enabled {
		return s.workflowChainService().TriggerTaskChain(ctx, parentTask, parentOutput)
	}
	if config.ChildTaskID != "" || config.ChildAutomationNodeKey != "" {
		return s.activateCompiledAutomationChild(ctx, parentTask, parentOutput, config)
	}

	// Look for an existing blocked child to activate
	if s.taskRepo != nil {
		blockedChild, findErr := s.taskRepo.FindBlockedChildByParent(ctx, parentTask.ID)
		if findErr != nil {
			applog.Infof("[agent-svc] triggerTaskChain error finding blocked child for parent=%s: %v", parentTask.ID, findErr)
		}
		if blockedChild != nil {
			applog.Infof("[agent-svc] triggerTaskChain activating blocked child id=%s parent=%s", blockedChild.ID, parentTask.ID)

			// Build the real prompt from parent output
			childPrompt := llmworkflow.CleanOutputForChain(parentOutput)
			if config.ChildPromptPrefix != "" {
				childPrompt = config.ChildPromptPrefix + "\n\n" + childPrompt
			}
			blockedChild.Prompt = childPrompt
			blockedChild.Status = models.StatusPending

			// Resolve lineage now that parent is complete
			svc := s.workflowChainService()
			if svc != nil {
				branch, sha, lineageErr := workflowLineageResolver{s: s}.ResolveParentLineage(ctx, parentTask)
				if lineageErr != nil {
					applog.Infof("[agent-svc] triggerTaskChain lineage resolution failed parent=%s: %v", parentTask.ID, lineageErr)
				} else {
					blockedChild.BaseBranch = branch
					blockedChild.BaseCommitSHA = sha
					applog.Infof("[agent-svc] triggerTaskChain resolved lineage for child=%s branch=%s sha=%s", blockedChild.ID, branch, sha)
				}
			}

			// Resolve category: inherit only runnable categories by default.
			if config.ChildCategory != "" {
				blockedChild.Category = models.TaskCategory(config.ChildCategory)
			} else {
				blockedChild.Category = defaultRunnableChildCategory(parentTask.Category)
			}

			if err := s.taskRepo.Update(ctx, blockedChild); err != nil {
				applog.Infof("[agent-svc] triggerTaskChain error updating blocked child id=%s: %v", blockedChild.ID, err)
				return fmt.Errorf("activating blocked child: %w", err)
			}

			// Submit to worker pool
			if s.taskSvc != nil && blockedChild.Category == models.CategoryActive {
				applog.Infof("[agent-svc] triggerTaskChain submitting activated child id=%s to worker pool", blockedChild.ID)
				s.taskSvc.workerSvc.Submit(*blockedChild)
			}
			return nil
		}
	}

	// Fallback: no blocked child found, create a new one via the workflow service
	applog.Infof("[agent-svc] triggerTaskChain no blocked child found for parent=%s, creating new child", parentTask.ID)
	return s.workflowChainService().TriggerTaskChain(ctx, parentTask, parentOutput)
}

func (s *LLMService) activatePublishedCustomAutomationChild(ctx context.Context, parentTask models.Task, parentOutput string) (bool, error) {
	if s == nil || s.automationRepo == nil || !strings.HasPrefix(parentTask.CreatedVia, "automation:") {
		return false, nil
	}
	createdVia := strings.TrimPrefix(parentTask.CreatedVia, "automation:")
	separator := strings.Index(createdVia, ":")
	if separator <= 0 {
		return false, nil
	}
	automationID := createdVia[:separator]
	sourceNodeKey := createdVia[separator+1:]
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != parentTask.ProjectID {
		return false, nil
	}
	for _, binding := range automationContext.Bindings {
		if binding.AutomationID != automationID {
			continue
		}
		sourceNode, err := s.automationRepo.GetNodeByKey(ctx, parentTask.ProjectID, automationID, binding.VersionID, sourceNodeKey)
		if err != nil {
			return true, err
		}
		if sourceNode == nil || sourceNode.ID != binding.NodeID {
			return true, errors.New("custom Automation task source does not match its immutable version")
		}
		custom, handoffs, err := s.automationRepo.ListCustomTaskHandoffs(ctx, parentTask.ProjectID, automationID, binding.VersionID, sourceNode.ID)
		if err != nil {
			return true, err
		}
		if !custom {
			return false, nil
		}
		var busyErr error
		for _, handoff := range handoffs {
			targetNode := &handoff.Node
			promptCandidate, targetDraft, err := s.publishedCustomPromptCandidateForNode(ctx, parentTask.ProjectID, automationID, binding.VersionID, *targetNode)
			if err != nil {
				return true, err
			}
			category, _ := targetDraft.Config["category"].(string)
			if sourceNode.NodeType == models.AutomationNodeTrigger {
				category = string(models.CategoryActive)
			}
			chain := &models.ChainConfiguration{
				Enabled: true, Trigger: "on_completion", ChildTaskID: handoff.TaskID, ChildAutomationNodeKey: targetNode.NodeKey,
				ChildCategory: category, ChildPromptPrefix: automationCompiledTaskPrompt(promptCandidate, targetDraft),
			}
			if err := s.activateCompiledAutomationChild(ctx, parentTask, parentOutput, chain); err != nil {
				if errors.Is(err, repository.ErrAutomationChainChildBusy) {
					busyErr = errors.Join(busyErr, err)
					continue
				}
				return true, err
			}
		}
		return true, busyErr
	}
	return false, nil
}

func (s *LLMService) publishedCustomPromptCandidateForNode(ctx context.Context, projectID, automationID, versionID string, targetNode models.AutomationNode) (models.AutomationDraftCandidate, models.AutomationDraftNode, error) {
	metadata, err := s.automationRepo.GetAutomationGraphMetadata(ctx, projectID, automationID, versionID)
	if err != nil {
		return models.AutomationDraftCandidate{}, models.AutomationDraftNode{}, err
	}
	if metadata != nil && strings.TrimSpace(metadata.CandidateJSON) != "" {
		var candidate models.AutomationDraftCandidate
		if err := json.Unmarshal([]byte(metadata.CandidateJSON), &candidate); err != nil {
			return models.AutomationDraftCandidate{}, models.AutomationDraftNode{}, fmt.Errorf("decoding custom Automation graph metadata: %w", err)
		}
		if candidate.AdapterKey == AutomationAdapterCustom {
			if targetDraft, ok := findDraftNode(candidate, targetNode.NodeKey); ok {
				return candidate, targetDraft, nil
			}
		}
	}

	targetDraft, err := automationDraftNodeFromPublishedNode(targetNode)
	if err != nil {
		return models.AutomationDraftCandidate{}, models.AutomationDraftNode{}, err
	}
	promptCandidate := models.AutomationDraftCandidate{AdapterKey: AutomationAdapterCustom, Nodes: []models.AutomationDraftNode{targetDraft}}
	notificationNode, err := s.automationRepo.GetCustomNotificationHandoff(ctx, projectID, automationID, versionID, targetNode.ID)
	if err != nil {
		return models.AutomationDraftCandidate{}, models.AutomationDraftNode{}, err
	}
	if notificationNode != nil {
		notificationDraft, err := automationDraftNodeFromPublishedNode(*notificationNode)
		if err != nil {
			return models.AutomationDraftCandidate{}, models.AutomationDraftNode{}, err
		}
		promptCandidate.Nodes = append(promptCandidate.Nodes, notificationDraft)
		promptCandidate.Edges = append(promptCandidate.Edges, models.AutomationDraftEdge{
			From: targetNode.NodeKey, To: notificationNode.NodeKey, FromPort: "right", ToPort: "left",
		})
	}
	return promptCandidate, targetDraft, nil
}

func automationDraftNodeFromPublishedNode(node models.AutomationNode) (models.AutomationDraftNode, error) {
	var config map[string]any
	if err := json.Unmarshal([]byte(node.ConfigJSON), &config); err != nil {
		return models.AutomationDraftNode{}, fmt.Errorf("decoding custom Automation task configuration: %w", err)
	}
	return models.AutomationDraftNode{Key: node.NodeKey, Name: node.Name, Type: node.NodeType, Role: node.Role, Config: config}, nil
}

func (s *LLMService) activateCompiledAutomationChild(ctx context.Context, parentTask models.Task, parentOutput string, config *models.ChainConfiguration) error {
	if s == nil || s.taskRepo == nil || s.automationRepo == nil || config == nil || config.ChildTaskID == "" || config.ChildAutomationNodeKey == "" {
		return errors.New("compiled Automation task handoff is incomplete")
	}
	createdVia := strings.TrimPrefix(parentTask.CreatedVia, "automation:")
	separator := strings.Index(createdVia, ":")
	if createdVia == parentTask.CreatedVia || separator <= 0 {
		return errors.New("compiled Automation task handoff has an invalid parent identity")
	}
	automationID := createdVia[:separator]
	sourceNodeKey := createdVia[separator+1:]
	child, err := s.taskRepo.GetByID(ctx, config.ChildTaskID)
	if err != nil {
		return err
	}
	if child == nil || child.ProjectID != parentTask.ProjectID || child.ParentTaskID == nil || *child.ParentTaskID != parentTask.ID ||
		child.CreatedVia != repository.AutomationCompilerTaskCreatedVia(automationID, config.ChildAutomationNodeKey) {
		return errors.New("compiled Automation child task does not match the published topology")
	}
	automationContext, ok := AutomationContextFromContext(ctx)
	if !ok || automationContext.ProjectID != parentTask.ProjectID {
		automationContext, err = s.automationRepo.ContextForTask(ctx, parentTask.ProjectID, parentTask.ID)
		if err != nil {
			return err
		}
	}
	_, executionID, executionOK := AutomationExecutionFromContext(ctx)
	if !executionOK {
		return errors.New("compiled Automation task handoff has no causal execution")
	}
	var sourceBinding *models.AutomationBinding
	for i := range automationContext.Bindings {
		if automationContext.Bindings[i].AutomationID == automationID {
			sourceBinding = &automationContext.Bindings[i]
			break
		}
	}
	if sourceBinding == nil {
		return errors.New("compiled Automation task handoff has no causal binding")
	}
	sourceNode, err := s.automationRepo.GetNodeByKey(ctx, parentTask.ProjectID, automationID, sourceBinding.VersionID, sourceNodeKey)
	if err != nil || sourceNode == nil || sourceNode.ID != sourceBinding.NodeID {
		return errors.New("compiled Automation task handoff source node does not match its causal binding")
	}
	targetNode, err := s.automationRepo.GetNodeByKey(ctx, parentTask.ProjectID, automationID, sourceBinding.VersionID, config.ChildAutomationNodeKey)
	if err != nil {
		return err
	}
	if targetNode == nil || targetNode.NodeType != models.AutomationNodeAgentTask {
		return errors.New("compiled Automation task handoff target node is unavailable")
	}

	child.Prompt = llmworkflow.CleanOutputForChain(parentOutput)
	if config.ChildPromptPrefix != "" {
		child.Prompt = config.ChildPromptPrefix + "\n\n" + child.Prompt
	}
	if config.ChildCategory != "" {
		child.Category = models.TaskCategory(config.ChildCategory)
	} else {
		child.Category = defaultRunnableChildCategory(parentTask.Category)
	}
	if branch, sha, lineageErr := (workflowLineageResolver{s: s}).ResolveParentLineage(ctx, parentTask); lineageErr == nil {
		child.BaseBranch = branch
		child.BaseCommitSHA = sha
	}
	child.LineageDepth = parentTask.LineageDepth + 1

	targetBinding := *sourceBinding
	targetBinding.NodeID = targetNode.ID
	targetContext := models.AutomationContext{ProjectID: parentTask.ProjectID, Bindings: []models.AutomationBinding{targetBinding}}
	event := repository.AutomationProjectionEvent{
		Context: targetContext, Binding: targetBinding,
		ActivityKey: "execution:" + executionID + ":handoff:" + child.ID, ActivityType: "task_handoff", ActivityStatus: models.AutomationActivityCompleted,
		EventKey:   "execution:" + executionID + ":handoff:" + sourceNode.ID + ":" + targetNode.ID,
		FromNodeID: sourceNode.ID, ToNodeID: targetNode.ID, Transition: models.AutomationTransitionEntered,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: child.ID, Relation: "child"}, {ResourceType: "execution", ResourceID: executionID, Relation: "source"}},
	}
	if targetBinding.WorkItemID == "" {
		event.WorkItemKey = "execution:" + executionID + ":chain"
		event.WorkItemKind = "task_chain"
		event.WorkItemTitle = child.Title
	}
	workItem, _, activated, err := s.taskRepo.ActivateAutomationChainedTask(ctx, parentTask, child, event)
	if errors.Is(err, repository.ErrAutomationChainChildBusy) {
		blocked := event
		blocked.ActivityKey += ":blocked"
		blocked.ActivityStatus = models.AutomationActivityFailed
		blocked.EventKey += ":blocked"
		blocked.Transition = models.AutomationTransitionBlocked
		blocked.WorkItemStatus = models.AutomationWorkItemBlocked
		workItem, _, projectionErr := s.automationRepo.RecordProjectionEvent(ctx, blocked)
		if projectionErr != nil {
			return fmt.Errorf("recording blocked Automation task handoff: %w", projectionErr)
		}
		if workItem != nil {
			targetBinding.WorkItemID = workItem.ID
		}
		s.automationRepo.PublishInvalidation(events.AutomationTransitionCreated, parentTask.ProjectID, targetBinding)
		return err
	}
	if err != nil {
		return err
	}
	if !activated {
		return nil
	}
	if s.automationHandoffBeforeFinalAdmission != nil {
		s.automationHandoffBeforeFinalAdmission()
	}
	stillAdmitted, err := s.taskRepo.ConfirmAutomationChainedTaskAdmission(ctx, parentTask.ProjectID, targetBinding, child.ID)
	if err != nil {
		return fmt.Errorf("confirming Automation task handoff admission: %w", err)
	}
	if !stillAdmitted {
		return nil
	}
	if workItem != nil {
		targetBinding.WorkItemID = workItem.ID
	}
	s.automationRepo.PublishInvalidation(events.AutomationTransitionCreated, parentTask.ProjectID, targetBinding)
	if child.Category == models.CategoryActive {
		if s.taskSvc == nil || s.taskSvc.workerSvc == nil {
			return errors.New("worker service is unavailable for compiled Automation task handoff")
		}
		s.taskSvc.workerSvc.Submit(*child)
	}
	return nil
}
