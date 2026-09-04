package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	llmworkflow "github.com/openvibely/openvibely/internal/llm/workflow"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// TaskCreationRequest represents a typed task creation action request.
type TaskCreationRequest struct {
	Title                   string                     `json:"title"`
	Prompt                  string                     `json:"prompt"`
	Goal                    string                     `json:"goal"`                // Optional persisted task goal
	Category                string                     `json:"category"`            // "active" or "backlog" (default: "backlog")
	Priority                int                        `json:"priority"`            // 1=Low, 2=Normal, 3=High, 4=Urgent (default: 2)
	AgentID                 string                     `json:"agent_id"`            // Optional: specific LLM config ID (empty = auto-select or default)
	AgentDefinitionID       string                     `json:"agent_definition_id"` // Optional: known agent definition ID
	Agent                   string                     `json:"agent"`               // Optional: agent definition name (resolved to AgentDefinitionID)
	Chain                   *models.ChainConfiguration `json:"chain,omitempty"`     // Optional: chain config for sequential task execution
	SourceGitHubIssueNumber int                        `json:"source_github_issue_number,omitempty"`
	SourceGitHubRepoURL     string                     `json:"source_github_repo_url,omitempty"`
}

// EffectiveTaskCreationCategory resolves the category a task creation request would
// receive after model selection and auto-start policy are applied.
func EffectiveTaskCreationCategory(req TaskCreationRequest, availableAgents []models.LLMConfig) models.TaskCategory {
	selectedAgentID, _ := selectTaskCreationAgent(req, availableAgents)
	return resolveTaskCreationCategory(req, selectedAgentID, availableAgents)
}

func selectTaskCreationAgent(req TaskCreationRequest, availableAgents []models.LLMConfig) (string, string) {
	requestedAgentID := strings.TrimSpace(req.AgentID)
	if strings.EqualFold(requestedAgentID, automationDefaultModelConfigID) {
		return "", ""
	}
	if requestedAgentID != "" {
		return requestedAgentID, ""
	}
	if len(availableAgents) > 1 {
		complexity := AnalyzeComplexity(req.Prompt)
		if result := SelectLLM(complexity, availableAgents); result != nil {
			return result.LLMConfig.ID, FormatSelectionSummary(result)
		}
	}
	if len(availableAgents) == 1 {
		return availableAgents[0].ID, ""
	}
	return "", ""
}

func resolveTaskCreationCategory(req TaskCreationRequest, selectedAgentID string, availableAgents []models.LLMConfig) models.TaskCategory {
	if req.Category != "" {
		category := models.TaskCategory(req.Category)
		if category == models.CategoryActive || category == models.CategoryBacklog {
			return category
		}
		return models.CategoryBacklog
	}

	for i := range availableAgents {
		if availableAgents[i].ID == selectedAgentID && availableAgents[i].AutoStartTasks {
			return models.CategoryActive
		}
	}
	return models.CategoryBacklog
}

// ExecuteTaskCreations creates tasks from typed action requests and returns a summary.
// The summary includes task IDs in the format [TASK_ID:id] so the frontend can
// convert them to clickable links.
// If agents is non-empty, auto-selects an agent for each task based on prompt complexity.
// Tasks with an explicit AgentID in the request skip auto-selection.
func ExecuteTaskCreations(ctx context.Context, requests []TaskCreationRequest, projectID string, taskSvc *TaskService, agents ...[]models.LLMConfig) string {
	_, summary := ExecuteTaskCreationsWithReturn(ctx, requests, projectID, taskSvc, agents...)
	return summary
}

// TaskCreationResult preserves the originating request for each successfully created task.
type TaskCreationResult struct {
	RequestIndex int
	Task         models.Task
}

// TaskCreationPersistence runs inside the task creation transaction after the
// task ID has been assigned and before active work can be submitted.
type TaskCreationPersistence func(context.Context, repository.SQLExecutor, *models.Task) error

// ExecuteTaskCreationsWithReturn creates tasks from typed action requests and returns both the created tasks and a summary.
// This variant is used when the caller needs access to the created task objects.
func ExecuteTaskCreationsWithReturn(ctx context.Context, requests []TaskCreationRequest, projectID string, taskSvc *TaskService, agents ...[]models.LLMConfig) ([]models.Task, string) {
	return ExecuteTaskCreationsWithReturnAndPersistence(ctx, requests, projectID, taskSvc, nil, agents...)
}

// ExecuteTaskCreationsWithReturnAndPersistence creates tasks and persists
// caller-specific metadata in the same transaction as each task.
func ExecuteTaskCreationsWithReturnAndPersistence(ctx context.Context, requests []TaskCreationRequest, projectID string, taskSvc *TaskService, persist TaskCreationPersistence, agents ...[]models.LLMConfig) ([]models.Task, string) {
	results, summary := ExecuteTaskCreationsWithIndexedReturnAndPersistence(ctx, requests, projectID, taskSvc, persist, agents...)
	createdTasks := make([]models.Task, 0, len(results))
	for _, result := range results {
		createdTasks = append(createdTasks, result.Task)
	}
	return createdTasks, summary
}

// ExecuteTaskCreationsWithIndexedReturn creates tasks and retains each successful
// task's original request index so callers never need to correlate by title.
func ExecuteTaskCreationsWithIndexedReturn(ctx context.Context, requests []TaskCreationRequest, projectID string, taskSvc *TaskService, agents ...[]models.LLMConfig) ([]TaskCreationResult, string) {
	return ExecuteTaskCreationsWithIndexedReturnAndPersistence(ctx, requests, projectID, taskSvc, nil, agents...)
}

// ExecuteTaskCreationsWithIndexedReturnAndPersistence is the indexed creation
// path with an optional transaction-scoped metadata callback.
func ExecuteTaskCreationsWithIndexedReturnAndPersistence(ctx context.Context, requests []TaskCreationRequest, projectID string, taskSvc *TaskService, persist TaskCreationPersistence, agents ...[]models.LLMConfig) ([]TaskCreationResult, string) {
	if len(requests) == 0 {
		return nil, ""
	}

	// Flatten optional agents parameter
	var availableAgents []models.LLMConfig
	if len(agents) > 0 {
		availableAgents = agents[0]
	}

	var createdResults []TaskCreationResult
	var created []string
	var failed []string

	for requestIndex, req := range requests {
		req.Title = strings.TrimSpace(req.Title)
		req.Prompt = strings.TrimSpace(req.Prompt)
		selectedAgentID, selectionInfo := selectTaskCreationAgent(req, availableAgents)
		if req.AgentID == "" && len(availableAgents) == 1 {
			applog.Infof("[task-creation] only one agent available, using %s", selectedAgentID)
		}

		category := resolveTaskCreationCategory(req, selectedAgentID, availableAgents)
		if req.Category == "" && category == models.CategoryActive {
			applog.Infof("[task-creation] auto-start enabled for agent %s, setting category to active", selectedAgentID)
		}

		task := &models.Task{
			ProjectID: projectID,
			Title:     req.Title,
			Prompt:    req.Prompt,
			Status:    models.StatusPending,
			Category:  category,
			Priority:  req.Priority,
		}

		// Apply chain configuration if provided
		if req.Chain != nil {
			if err := task.SetChainConfig(req.Chain); err != nil {
				applog.Infof("[task-creation] error setting chain config for %q: %v", req.Title, err)
			}
		}

		// Set the selected agent ID (LLM config)
		if selectedAgentID != "" {
			task.AgentID = &selectedAgentID
		}

		// Explicit Agent assignments are all-or-nothing. Silently dropping an
		// invalid or non-selectable Agent would run the task with a different tool
		// policy and worktree mode than the caller requested.
		agentDefinitionID, err := resolveTaskCreationAgentDefinition(ctx, req, projectID, taskSvc)
		if err != nil {
			applog.Infof("[task-creation] refusing invalid agent assignment for task %q: %v", req.Title, err)
			failed = append(failed, fmt.Sprintf("- \"%s\": %v", req.Title, err))
			continue
		}
		if agentDefinitionID != "" {
			task.AgentDefinitionID = &agentDefinitionID
		}

		if err := taskSvc.CreateWithGoalAndCallback(ctx, task, req.Goal, func(exec repository.SQLExecutor) error {
			if persist == nil {
				return nil
			}
			return persist(ctx, exec, task)
		}); err != nil {
			applog.Infof("[task-creation] error creating task %q: %v", req.Title, err)
			failed = append(failed, fmt.Sprintf("- \"%s\": %v", req.Title, err))
		} else {
			applog.Infof("[task-creation] created task %q id=%s category=%s agent=%v chain=%v selection=%s", req.Title, task.ID, category, task.AgentID, req.Chain != nil && req.Chain.Enabled, selectionInfo)

			// Pre-create blocked child for visibility when chain is configured
			if req.Chain != nil && req.Chain.Enabled {
				blockedChild := llmworkflow.BuildBlockedChild(*task, req.Chain)
				if childErr := taskSvc.Create(ctx, blockedChild); childErr != nil {
					applog.Infof("[task-creation] error pre-creating blocked child for %q: %v", req.Title, childErr)
				} else {
					applog.Infof("[task-creation] pre-created blocked child id=%s for parent=%s", blockedChild.ID, task.ID)
				}
			}

			createdResults = append(createdResults, TaskCreationResult{RequestIndex: requestIndex, Task: *task})
			line := fmt.Sprintf("- \"%s\" (%s) [TASK_ID:%s]", req.Title, category, task.ID)
			if strings.TrimSpace(req.Goal) != "" {
				line += " [goal:set]"
			} // Auto-selection info logged server-side but not shown to user to reduce clutter
			// if selectionInfo != "" {
			// 	line += " " + selectionInfo
			// }
			if req.Chain != nil && req.Chain.Enabled {
				chainDesc := "chained"
				if req.Chain.ChildTitle != "" {
					chainDesc = fmt.Sprintf("chains to: \"%s\"", req.Chain.ChildTitle)
				}
				line += fmt.Sprintf(" [%s]", chainDesc)
			}
			created = append(created, line)
		}
	}

	var summary strings.Builder
	summary.WriteString("\n\n---\n")
	if len(created) > 0 {
		summary.WriteString(fmt.Sprintf("Created %d task(s):\n", len(created)))
		summary.WriteString(strings.Join(created, "\n"))
	}
	if len(failed) > 0 {
		if len(created) > 0 {
			summary.WriteString("\n\n")
		}
		summary.WriteString(fmt.Sprintf("Failed to create %d task(s):\n", len(failed)))
		summary.WriteString(strings.Join(failed, "\n"))
	}

	return createdResults, summary.String()
}

func ResolveTaskCreationAgentDefinition(ctx context.Context, req TaskCreationRequest, projectID string, taskSvc *TaskService) (string, error) {
	return resolveTaskCreationAgentDefinition(ctx, req, projectID, taskSvc)
}

func resolveTaskCreationAgentDefinition(ctx context.Context, req TaskCreationRequest, projectID string, taskSvc *TaskService) (string, error) {
	requestedName := strings.TrimSpace(req.Agent)
	requestedID := strings.TrimSpace(req.AgentDefinitionID)
	if requestedName == "" && requestedID == "" {
		return "", nil
	}
	if taskSvc == nil || taskSvc.agentRepo == nil {
		return "", fmt.Errorf("cannot validate explicit Agent assignment because the Agent repository is unavailable")
	}

	var agent *models.Agent
	var err error
	if requestedName != "" {
		agent, err = taskSvc.agentRepo.GetUniqueSelectableByName(ctx, requestedName)
		if err != nil {
			return "", fmt.Errorf("resolve Agent %q: %w", requestedName, err)
		}
		if agent == nil {
			return "", fmt.Errorf("Agent %q is not one unique enabled, selectable primary Agent definition", requestedName)
		}
		if requestedID != "" && requestedID != agent.ID {
			return "", fmt.Errorf("Agent %q does not match agent_definition_id %q", requestedName, requestedID)
		}
	} else {
		agent, err = taskSvc.agentRepo.GetByID(ctx, requestedID)
		if err != nil {
			return "", fmt.Errorf("validate agent_definition_id %q: %w", requestedID, err)
		}
		if agent == nil {
			return "", fmt.Errorf("agent_definition_id %q does not exist", requestedID)
		}
	}
	if !isChatAssignableAgentDefinition(*agent) {
		return "", fmt.Errorf("Agent %q is not assignable as a primary task Agent", agent.Name)
	}

	if agent.Scope == models.AgentScopeProject && strings.TrimSpace(agent.ProjectID) != strings.TrimSpace(projectID) {
		return "", fmt.Errorf("Agent %q belongs to a different project", agent.Name)
	}
	return agent.ID, nil
}

// TaskEditRequest represents a typed task edit action request.
type TaskEditRequest struct {
	ID                   string                     `json:"id"`                               // Required: task ID to edit
	Title                string                     `json:"title,omitempty"`                  // Optional: new title
	Prompt               string                     `json:"prompt,omitempty"`                 // Optional: new prompt
	Category             string                     `json:"category,omitempty"`               // Optional: new category
	Priority             int                        `json:"priority,omitempty"`               // Optional: new priority (1-4)
	PrioritySet          bool                       `json:"-"`                                // Internal: true when JSON explicitly supplied priority
	Tag                  string                     `json:"tag,omitempty"`                    // Optional: new tag ("feature", "bug", "")
	AgentID              string                     `json:"agent_id,omitempty"`               // Optional: new model config ID (empty = leave unchanged)
	AgentConfigID        string                     `json:"agent_config_id,omitempty"`        // Optional: alias for agent_id (for compatibility)
	AgentDefinitionID    string                     `json:"agent_definition_id,omitempty"`    // Optional: known primary Agent definition ID
	Agent                string                     `json:"agent,omitempty"`                  // Optional: exact primary Agent definition name
	ClearAgentDefinition bool                       `json:"clear_agent_definition,omitempty"` // Optional: explicitly clear the primary Agent definition
	Chain                *models.ChainConfiguration `json:"chain,omitempty"`                  // Optional: chain config for sequential task execution
	Attachments          []string                   `json:"attachments,omitempty"`            // Optional: file paths to attach to the task
}

func (r *TaskEditRequest) UnmarshalJSON(data []byte) error {
	type alias TaskEditRequest
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	decoded.PrioritySet = raw["priority"] != nil
	*r = TaskEditRequest(decoded)
	return nil
}

// ExecuteTaskEdits applies edits to existing tasks and returns a summary.
// Only fields that are set in the request are updated.
// If attachmentRepo and uploadsDir are provided, file attachments in requests are processed.
func ExecuteTaskEdits(ctx context.Context, requests []TaskEditRequest, projectID string, taskSvc *TaskService, attachmentRepo *repository.AttachmentRepo, uploadsDir string) string {
	if len(requests) == 0 {
		return ""
	}

	var edited []string
	var failed []string

	for _, req := range requests {
		task, err := taskSvc.GetByID(ctx, req.ID)
		if err != nil || task == nil {
			applog.Infof("[task-edit] task not found id=%s: %v", req.ID, err)
			failed = append(failed, fmt.Sprintf("- task %s: not found", req.ID))
			continue
		}

		// Verify task belongs to the same project
		if task.ProjectID != projectID {
			applog.Infof("[task-edit] task %s belongs to different project", req.ID)
			failed = append(failed, fmt.Sprintf("- \"%s\": belongs to different project", task.Title))
			continue
		}

		primaryAgentDefinitionRequested := strings.TrimSpace(req.AgentDefinitionID) != "" || strings.TrimSpace(req.Agent) != "" || req.ClearAgentDefinition
		resolvedAgentDefinitionID := ""
		if primaryAgentDefinitionRequested {
			if req.ClearAgentDefinition && (strings.TrimSpace(req.AgentDefinitionID) != "" || strings.TrimSpace(req.Agent) != "") {
				failed = append(failed, fmt.Sprintf("- \"%s\": clear_agent_definition cannot be combined with agent_definition_id or agent", task.Title))
				continue
			}
			if !req.ClearAgentDefinition {
				var resolveErr error
				resolvedAgentDefinitionID, resolveErr = resolveTaskCreationAgentDefinition(ctx, TaskCreationRequest{AgentDefinitionID: req.AgentDefinitionID, Agent: req.Agent}, projectID, taskSvc)
				if resolveErr != nil {
					applog.Infof("[task-edit] refusing invalid primary Agent assignment for task %s: %v", req.ID, resolveErr)
					failed = append(failed, fmt.Sprintf("- \"%s\": %v", task.Title, resolveErr))
					continue
				}
			}
		}

		priorityRequested := req.PrioritySet || req.Priority != 0
		if priorityRequested {
			if err := validateTaskPriority(req.Priority); err != nil {
				failed = append(failed, fmt.Sprintf("- \"%s\": %v", task.Title, err))
				continue
			}
		}

		// Apply only the fields that were specified
		var changes []string
		if req.Title != "" && req.Title != task.Title {
			task.Title = req.Title
			changes = append(changes, "title")
		}
		if req.Prompt != "" && req.Prompt != task.Prompt {
			task.Prompt = req.Prompt
			changes = append(changes, "prompt")
		}
		if priorityRequested && req.Priority != task.Priority {
			task.Priority = req.Priority
			changes = append(changes, "priority")
		}
		if req.Tag != "" {
			newTag := models.TaskTag(req.Tag)
			if newTag != task.Tag {
				task.Tag = newTag
				changes = append(changes, "tag")
			}
		}
		// Handle agent assignment - support both agent_id and agent_config_id for compatibility
		agentID := req.AgentID
		if agentID == "" && req.AgentConfigID != "" {
			agentID = req.AgentConfigID
		}
		if agentID != "" {
			// Agent ID change - compare with current value
			currentAgentID := ""
			if task.AgentID != nil {
				currentAgentID = *task.AgentID
			}
			if agentID != currentAgentID {
				task.AgentID = &agentID
				changes = append(changes, "agent")
			}
		}
		if primaryAgentDefinitionRequested {
			currentAgentDefinitionID := ""
			if task.AgentDefinitionID != nil {
				currentAgentDefinitionID = *task.AgentDefinitionID
			}
			if req.ClearAgentDefinition {
				if currentAgentDefinitionID != "" {
					task.AgentDefinitionID = nil
					changes = append(changes, "primary_agent")
				}
			} else if resolvedAgentDefinitionID != currentAgentDefinitionID {
				task.AgentDefinitionID = &resolvedAgentDefinitionID
				changes = append(changes, "primary_agent")
			}
		}

		// Handle chain configuration
		if req.Chain != nil {
			if err := task.SetChainConfig(req.Chain); err != nil {
				applog.Infof("[task-edit] error setting chain config for task %s: %v", req.ID, err)
			} else {
				changes = append(changes, "chain_config")

				// Manage blocked child for visibility (same as UI UpdateTaskChainConfig path)
				if taskSvc != nil && taskSvc.repo != nil {
					if req.Chain.Enabled {
						existing, _ := taskSvc.repo.FindBlockedChildByParent(ctx, task.ID)
						if existing == nil {
							blockedChild := llmworkflow.BuildBlockedChild(*task, req.Chain)
							if childErr := taskSvc.Create(ctx, blockedChild); childErr != nil {
								applog.Infof("[task-edit] error pre-creating blocked child for task %s: %v", req.ID, childErr)
							} else {
								applog.Infof("[task-edit] pre-created blocked child id=%s for parent=%s", blockedChild.ID, task.ID)
							}
						}
					} else {
						if delErr := taskSvc.repo.DeleteBlockedChildrenByParent(ctx, task.ID); delErr != nil {
							applog.Infof("[task-edit] error deleting blocked children for task %s: %v", req.ID, delErr)
						} else {
							applog.Infof("[task-edit] removed blocked children for parent=%s (chain disabled)", task.ID)
						}
					}
				}
			}
		}

		// Handle category change separately since it has lifecycle side effects.
		categoryChanged := false
		newCategory := task.Category
		if req.Category != "" {
			requestedCategory := models.TaskCategory(req.Category)
			if requestedCategory != task.Category {
				// Validate category
				validCategory := false
				for _, c := range models.SelectableCategories {
					if requestedCategory == c {
						validCategory = true
						break
					}
				}
				if validCategory {
					newCategory = requestedCategory
					categoryChanged = true
					changes = append(changes, "category")
				} else {
					applog.Infof("[task-edit] invalid category %q for task %s", req.Category, req.ID)
				}
			}
		}

		// Handle attachments - copy files to task's upload directory
		if len(req.Attachments) > 0 && attachmentRepo != nil && uploadsDir != "" {
			copiedCount, copiedNames := copyAttachmentFiles(ctx, req.Attachments, task.ID, attachmentRepo, uploadsDir)
			if copiedCount > 0 {
				changes = append(changes, fmt.Sprintf("attachments (+%d)", copiedCount))
				// Append file references to task prompt so the executing agent knows about them
				var fileRefs []string
				for _, name := range copiedNames {
					absPath := filepath.Join(uploadsDir, "tasks", task.ID, name)
					fileRefs = append(fileRefs, fmt.Sprintf("%s (path: %s)", name, absPath))
				}
				task.Prompt += fmt.Sprintf("\n\n[Attached files:\n%s]", strings.Join(fileRefs, "\n"))
			}
		}

		if len(changes) == 0 {
			applog.Infof("[task-edit] no changes for task %s", req.ID)
			failed = append(failed, fmt.Sprintf("- \"%s\": no changes to apply", task.Title))
			continue
		}

		nonCategoryChanges := len(changes)
		if categoryChanged {
			nonCategoryChanges--
		}
		if nonCategoryChanges > 0 {
			if err := taskSvc.Update(ctx, task); err != nil {
				applog.Infof("[task-edit] error updating task %s: %v", req.ID, err)
				failed = append(failed, fmt.Sprintf("- \"%s\": %v", task.Title, err))
				continue
			}
		}
		if categoryChanged {
			if err := taskSvc.UpdateCategory(ctx, task.ID, newCategory); err != nil {
				applog.Infof("[task-edit] error updating task category %s: %v", req.ID, err)
				failed = append(failed, fmt.Sprintf("- \"%s\": %v", task.Title, err))
				continue
			}
			task.Category = newCategory
		}

		applog.Infof("[task-edit] updated task %s fields=%v", req.ID, changes)
		edited = append(edited, fmt.Sprintf("- \"%s\" (%s, updated: %s) [TASK_EDITED:%s]", task.Title, task.Category, strings.Join(changes, ", "), task.ID))
	}

	var summary strings.Builder
	summary.WriteString("\n\n---\n")
	if len(edited) > 0 {
		summary.WriteString(fmt.Sprintf("Edited %d task(s):\n", len(edited)))
		summary.WriteString(strings.Join(edited, "\n"))
	}
	if len(failed) > 0 {
		if len(edited) > 0 {
			summary.WriteString("\n\n")
		}
		summary.WriteString(fmt.Sprintf("Failed to edit %d task(s):\n", len(failed)))
		summary.WriteString(strings.Join(failed, "\n"))
	}

	return summary.String()
}

// copyAttachmentFiles copies files from the given paths to the task's upload directory
// and creates attachment records in the database. Returns count of files copied and their names.
// Supports absolute file paths and chat attachment download URLs (e.g., /chat/attachments/{id}/download).
func copyAttachmentFiles(ctx context.Context, filePaths []string, taskID string, attachmentRepo *repository.AttachmentRepo, uploadsDir string) (int, []string) {
	taskDir := filepath.Join(uploadsDir, "tasks", taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		applog.Infof("[task-edit] error creating task directory %s: %v", taskDir, err)
		return 0, nil
	}

	copiedCount := 0
	var copiedNames []string

	for _, srcPath := range filePaths {
		srcPath = strings.TrimSpace(srcPath)
		if srcPath == "" {
			continue
		}

		// Check if the file exists
		info, err := os.Stat(srcPath)
		if err != nil {
			applog.Infof("[task-edit] attachment file not found %s: %v", srcPath, err)
			continue
		}
		if info.IsDir() {
			applog.Infof("[task-edit] attachment path is a directory %s, skipping", srcPath)
			continue
		}
		if info.Size() > 10<<20 { // 10 MB limit
			applog.Infof("[task-edit] attachment file too large %s (%d bytes), skipping", srcPath, info.Size())
			continue
		}

		// Copy the file
		fileName := filepath.Base(srcPath)
		destPath := filepath.Join(taskDir, fileName)

		src, err := os.Open(srcPath)
		if err != nil {
			applog.Infof("[task-edit] error opening attachment %s: %v", srcPath, err)
			continue
		}

		dest, err := os.Create(destPath)
		if err != nil {
			applog.Infof("[task-edit] error creating destination %s: %v", destPath, err)
			src.Close()
			continue
		}

		if _, err := io.Copy(dest, src); err != nil {
			applog.Infof("[task-edit] error copying attachment %s: %v", srcPath, err)
			src.Close()
			dest.Close()
			os.Remove(destPath)
			continue
		}
		src.Close()
		dest.Close()

		// Detect media type
		mediaType := mime.TypeByExtension(filepath.Ext(fileName))
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		// Create attachment record
		att := &models.Attachment{
			TaskID:    taskID,
			FileName:  fileName,
			FilePath:  destPath,
			MediaType: mediaType,
			FileSize:  info.Size(),
		}
		if err := attachmentRepo.Create(ctx, att); err != nil {
			applog.Infof("[task-edit] error creating attachment record for %s: %v", fileName, err)
			os.Remove(destPath)
			continue
		}

		applog.Infof("[task-edit] attached file %s to task %s", fileName, taskID)
		copiedCount++
		copiedNames = append(copiedNames, fileName)
	}

	return copiedCount, copiedNames
}

// BuildTaskContextString creates a summary of existing tasks for system prompts.
// Includes task IDs so runtime task tools can target exact tasks.
func BuildTaskContextString(tasks []models.Task) string {
	if len(tasks) == 0 {
		return "No tasks exist in this project yet."
	}

	var sb strings.Builder
	for _, t := range tasks {
		sb.WriteString(fmt.Sprintf("- [ID:%s] \"%s\" [%s, %s, priority:%d]", t.ID, t.Title, t.Category, t.Status, t.Priority))
		if t.Tag != "" {
			sb.WriteString(fmt.Sprintf(" tag:%s", t.Tag))
		}
		if t.Prompt != "" {
			// Include the full prompt so the AI can explain tasks in detail
			prompt := t.Prompt
			if len(prompt) > 500 {
				prompt = prompt[:500] + "..."
			}
			sb.WriteString(fmt.Sprintf("\n  Prompt: %s", prompt))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// BuildModelContextString creates a summary of available model configs for the chat system prompt.
// This allows the AI to know which models are available and their IDs for task model changes.
func BuildModelContextString(configs []models.LLMConfig) string {
	if len(configs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Available models (use the ID in the agent_id runtime-tool field to select an internal model config; this is not an Agent definition assignment):\n")
	for _, c := range configs {
		defaultMark := ""
		if c.IsDefault {
			defaultMark = " (default)"
		}
		sb.WriteString(fmt.Sprintf("- [ID:%s] \"%s\" (model: %s, provider: %s)%s\n", c.ID, c.Name, c.Model, c.Provider, defaultMark))
	}
	return sb.String()
}

// BuildAgentDefinitionContextString creates a prompt-safe summary of Agent definitions
// that can be assigned as a task's primary Agent from chat orchestration.
func BuildAgentDefinitionContextString(agents []models.ChatAssignableAgentDefinition) string {
	assignable := UniqueChatAssignableAgentDefinitions(agents)
	if len(assignable) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Available Agent definitions (from the Agents page; use the exact Name in the create_task tool's agent field to assign one as the task's primary Agent definition):\n")
	for _, a := range assignable {
		description := sanitizeAgentContextField(a.Description, 160)
		parts := []string{fmt.Sprintf("- Name: %q", sanitizeAgentContextField(a.Name, 120))}
		if key := sanitizeAgentContextField(a.Key, 80); key != "" {
			parts = append(parts, fmt.Sprintf("key: %s", key))
		}
		if description != "" {
			parts = append(parts, fmt.Sprintf("description: %s", description))
		}
		sb.WriteString(strings.Join(parts, "; "))
		sb.WriteString("\n")
	}
	return sb.String()
}

func UniqueChatAssignableAgentDefinitions(agents []models.ChatAssignableAgentDefinition) []models.ChatAssignableAgentDefinition {
	counts := make(map[string]int, len(agents))
	for _, a := range agents {
		if isChatAssignableAgentDefinitionSummary(a) {
			counts[strings.ToLower(strings.TrimSpace(a.Name))]++
		}
	}

	out := make([]models.ChatAssignableAgentDefinition, 0, len(agents))
	for _, a := range agents {
		key := strings.ToLower(strings.TrimSpace(a.Name))
		if isChatAssignableAgentDefinitionSummary(a) && counts[key] == 1 {
			out = append(out, a)
		}
	}
	return out
}

func isChatAssignableAgentDefinitionSummary(a models.ChatAssignableAgentDefinition) bool {
	return strings.TrimSpace(a.Name) != "" && strings.TrimSpace(a.SystemKind) == "" && a.Enabled && a.SelectableAsPrimary && a.GeneratedStatus != models.AgentStatusArchived && a.ArchivedAt == nil
}

func isChatAssignableAgentDefinition(a models.Agent) bool {
	return strings.TrimSpace(a.Name) != "" && strings.TrimSpace(a.SystemKind) == "" && a.Enabled && a.SelectableAsPrimary && a.GeneratedStatus != models.AgentStatusArchived && a.ArchivedAt == nil
}

func sanitizeAgentContextField(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	if limit > 0 && len(value) > limit {
		return value[:limit] + "..."
	}
	return value
}

// BuildTaskContextWithModels creates a summary of existing tasks including their assigned model.
// modelMap maps agent_id to LLMConfig for display purposes.
func BuildTaskContextWithModels(tasks []models.Task, modelMap map[string]models.LLMConfig) string {
	if len(tasks) == 0 {
		return "No tasks exist in this project yet."
	}

	var sb strings.Builder
	for _, t := range tasks {
		sb.WriteString(fmt.Sprintf("- [ID:%s] \"%s\" [%s, %s, priority:%d]", t.ID, t.Title, t.Category, t.Status, t.Priority))
		if t.Tag != "" {
			sb.WriteString(fmt.Sprintf(" tag:%s", t.Tag))
		}
		if t.AgentID != nil && *t.AgentID != "" {
			if cfg, ok := modelMap[*t.AgentID]; ok {
				sb.WriteString(fmt.Sprintf(" model:%s", cfg.Name))
			}
		}
		if chainCfg, err := t.ParseChainConfig(); err == nil && chainCfg.Enabled {
			chainInfo := fmt.Sprintf(" chain:%s", chainCfg.Trigger)
			if chainCfg.ChildTitle != "" {
				chainInfo += fmt.Sprintf("→\"%s\"", chainCfg.ChildTitle)
			}
			sb.WriteString(chainInfo)
		}
		if t.ParentTaskID != nil && *t.ParentTaskID != "" {
			sb.WriteString(fmt.Sprintf(" parent:%s", *t.ParentTaskID))
		}
		if t.Prompt != "" {
			prompt := t.Prompt
			if len(prompt) > 500 {
				prompt = prompt[:500] + "..."
			}
			sb.WriteString(fmt.Sprintf("\n  Prompt: %s", prompt))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// BuildScheduleContextString creates a summary of scheduled tasks with their schedule details
// for inclusion in chat system prompts. This enables the chat assistant to answer questions
// about the schedule (e.g., "What's scheduled today?", "Show me this week's schedule").
//
// The scheduleMap maps task IDs to their schedule entries. Tasks without schedules are skipped.
// The current time is used to format relative time descriptions.
func BuildScheduleContextString(tasks []models.Task, scheduleMap map[string][]models.Schedule, now time.Time) string {
	var scheduledTasks []struct {
		task     models.Task
		schedule models.Schedule
	}

	for _, t := range tasks {
		if scheds, ok := scheduleMap[t.ID]; ok {
			for _, s := range scheds {
				scheduledTasks = append(scheduledTasks, struct {
					task     models.Task
					schedule models.Schedule
				}{task: t, schedule: s})
			}
		}
	}

	if len(scheduledTasks) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Scheduled tasks in this project:\n")

	for _, st := range scheduledTasks {
		sb.WriteString(fmt.Sprintf("- [ID:%s] \"%s\"", st.task.ID, st.task.Title))

		// Schedule status
		if st.schedule.Enabled {
			sb.WriteString(" [enabled]")
		} else {
			sb.WriteString(" [disabled]")
		}

		// Recurrence pattern
		sb.WriteString(fmt.Sprintf(" repeat:%s", FormatRepeatPattern(st.schedule.RepeatType, st.schedule.RepeatInterval)))

		// Next run
		if st.schedule.NextRun != nil {
			localNext := st.schedule.NextRun.Local()
			sb.WriteString(fmt.Sprintf(" next_run:%s", localNext.Format("2006-01-02 15:04")))
		} else {
			sb.WriteString(" next_run:none")
		}

		// Last run
		if st.schedule.LastRun != nil {
			localLast := st.schedule.LastRun.Local()
			sb.WriteString(fmt.Sprintf(" last_run:%s", localLast.Format("2006-01-02 15:04")))
		}

		// Task status
		sb.WriteString(fmt.Sprintf(" status:%s", st.task.Status))

		sb.WriteString("\n")
	}

	// Add current time reference for the AI
	sb.WriteString(fmt.Sprintf("\nCurrent time: %s\n", now.Local().Format("2006-01-02 15:04 (Monday)")))

	return sb.String()
}

// FormatRepeatPattern returns a human-readable string for a schedule's repeat configuration.
func FormatRepeatPattern(repeatType models.RepeatType, interval int) string {
	switch repeatType {
	case models.RepeatOnce:
		return "once"
	case models.RepeatSeconds:
		if interval == 1 {
			return "every second"
		}
		return fmt.Sprintf("every %d seconds", interval)
	case models.RepeatMinutes:
		if interval == 1 {
			return "every minute"
		}
		return fmt.Sprintf("every %d minutes", interval)
	case models.RepeatHours:
		if interval == 1 {
			return "every hour"
		}
		return fmt.Sprintf("every %d hours", interval)
	case models.RepeatDaily:
		if interval == 1 {
			return "daily"
		}
		return fmt.Sprintf("every %d days", interval)
	case models.RepeatWeekly:
		if interval == 1 {
			return "weekly"
		}
		return fmt.Sprintf("every %d weeks", interval)
	case models.RepeatMonthly:
		if interval == 1 {
			return "monthly"
		}
		return fmt.Sprintf("every %d months", interval)
	default:
		return string(repeatType)
	}
}

// TaskExecutionRequest represents a typed filter-based task execution request.
type TaskExecutionRequest struct {
	TaskID           string   `json:"task_id"`           // Optional: exact task ID to execute.
	Title            string   `json:"title"`             // Optional: task title query to execute (first match in project).
	Tags             []string `json:"tags"`              // Optional: tags to match (e.g., ["feature", "bug"]). If empty, matches all tags.
	MinPriority      int      `json:"min_priority"`      // Optional: minimum priority (1=Low, 2=Normal, 3=High, 4=Urgent, default: 0 = all)
	IncludeCompleted bool     `json:"include_completed"` // Optional: include completed-status tasks in bulk matching results (default: false).
}

// ExecuteTaskExecutions executes tasks matching typed action requests and returns a summary.
func ExecuteTaskExecutions(ctx context.Context, requests []TaskExecutionRequest, projectID string, taskSvc *TaskService) string {
	if len(requests) == 0 {
		return ""
	}

	var executed []string
	var failed []string

	for _, req := range requests {
		// Convert string tags to TaskTag type
		var tags []models.TaskTag
		for _, tagStr := range req.Tags {
			tag := models.TaskTag(tagStr)
			// Validate tag
			validTag := false
			for _, validTagEnum := range models.AllTags {
				if tag == validTagEnum {
					validTag = true
					break
				}
			}
			if validTag && tag != models.TagNone {
				tags = append(tags, tag)
			}
		}

		// If tags were specified but none were valid, report an error
		if len(req.Tags) > 0 && len(tags) == 0 {
			applog.Infof("[task-execution] no valid tags found in request")
			failed = append(failed, "- No valid tags specified")
			continue
		}

		// Build filter description for consistent error/success messages
		var filterParts []string
		taskID := strings.TrimSpace(req.TaskID)
		title := strings.TrimSpace(req.Title)

		if taskID != "" {
			filterParts = append(filterParts, fmt.Sprintf("task_id=%s", taskID))
		}
		if title != "" {
			filterParts = append(filterParts, fmt.Sprintf("title=%q", title))
		}
		if len(tags) > 0 {
			filterParts = append(filterParts, fmt.Sprintf("tags=%v", tags))
		}
		if req.MinPriority > 0 {
			filterParts = append(filterParts, fmt.Sprintf("priority>=%d", req.MinPriority))
		}
		if req.IncludeCompleted {
			filterParts = append(filterParts, "include_completed=true")
		}
		filterDesc := strings.Join(filterParts, ", ")
		if filterDesc == "" {
			filterDesc = "all tasks"
		}

		var (
			matchedTasks []models.Task
			submitted    int
			err          error
		)
		if taskID != "" || title != "" {
			matchedTasks, submitted, err = executeTaskExecutionByReference(ctx, taskSvc, projectID, taskID, title)
		} else {
			// Bulk execute mode by filters.
			matchedTasks, submitted, err = taskSvc.ExecuteTasksByTags(ctx, tags, projectID, req.MinPriority, req.IncludeCompleted)
		}
		if err != nil {
			applog.Infof("[task-execution] error executing tasks: %v", err)
			failed = append(failed, fmt.Sprintf("- %s: %v", filterDesc, err))
			continue
		}

		if len(matchedTasks) == 0 {
			applog.Infof("[task-execution] no tasks found matching %s", filterDesc)
			failed = append(failed, fmt.Sprintf("- No tasks found matching %s", filterDesc))
			continue
		}

		if submitted == 0 {
			applog.Infof("[task-execution] %d tasks matched %s but none could be submitted", len(matchedTasks), filterDesc)
			failed = append(failed, fmt.Sprintf("- %d task(s) matched %s but none could be submitted (check logs for errors)", len(matchedTasks), filterDesc))
			continue
		}

		// Build summary of executed tasks
		taskSummary := make([]string, 0, len(matchedTasks))
		for _, task := range matchedTasks {
			taskSummary = append(taskSummary, fmt.Sprintf("  - \"%s\" (%s) [TASK_ID:%s]", task.Title, task.Category, task.ID))
		}

		executed = append(executed, fmt.Sprintf("- Executed %d task(s) matching %s:\n%s",
			submitted, filterDesc, strings.Join(taskSummary, "\n")))
	}

	var summary strings.Builder
	summary.WriteString("\n\n---\n")
	if len(executed) > 0 {
		summary.WriteString("Task Execution Results:\n")
		summary.WriteString(strings.Join(executed, "\n\n"))
	}
	if len(failed) > 0 {
		if len(executed) > 0 {
			summary.WriteString("\n\n")
		}
		summary.WriteString("Failed:\n")
		summary.WriteString(strings.Join(failed, "\n"))
	}

	return summary.String()
}

func executeTaskExecutionByReference(ctx context.Context, taskSvc *TaskService, projectID, taskID, title string) ([]models.Task, int, error) {
	var task *models.Task
	var err error
	if taskID != "" {
		task, err = taskSvc.repo.GetByID(ctx, taskID)
		if err != nil {
			return nil, 0, fmt.Errorf("error looking up task %s: %w", taskID, err)
		}
		if task == nil {
			return nil, 0, fmt.Errorf("task %s not found", taskID)
		}
		if task.ProjectID != projectID {
			return nil, 0, fmt.Errorf("task %s belongs to a different project", taskID)
		}
	} else {
		matches, searchErr := taskSvc.repo.SearchByTitle(ctx, projectID, title)
		if searchErr != nil {
			return nil, 0, fmt.Errorf("error searching for task %q: %w", title, searchErr)
		}
		if len(matches) == 0 {
			return nil, 0, fmt.Errorf("no task found matching %q", title)
		}
		task = &matches[0]
	}

	alreadyRunningOrQueued := task.Status == models.StatusRunning || task.Status == models.StatusQueued
	if runErr := taskSvc.RunTask(ctx, task.ID); runErr != nil {
		return nil, 0, runErr
	}

	updated, getErr := taskSvc.repo.GetByID(ctx, task.ID)
	if getErr != nil {
		return nil, 0, fmt.Errorf("reloading task %s after run: %w", task.ID, getErr)
	}
	if updated == nil {
		return nil, 0, fmt.Errorf("task %s disappeared after run", task.ID)
	}
	if alreadyRunningOrQueued {
		return []models.Task{*updated}, 0, nil
	}
	return []models.Task{*updated}, 1, nil
}

// ViewThreadRequest represents a request to view a task's thread/execution history.
type ViewThreadRequest struct {
	TaskID string `json:"task_id"` // Required: task ID to view thread for
	Title  string `json:"title"`   // Optional: task title for fuzzy search
	Offset int    `json:"offset"`  // Optional: execution index to start from (0-based)
	Limit  int    `json:"limit"`   // Optional: max executions to return (0 = all that fit)
}

// SendToTaskRequest represents a request to send a message to a task's thread.
type SendToTaskRequest struct {
	TaskID  string `json:"task_id"` // Required: task ID to send to
	Title   string `json:"title"`   // Optional: task title for fuzzy search
	Message string `json:"message"` // Required: message to send
}

// ScheduleTaskRequest represents a typed request to schedule a task.
type ScheduleTaskRequest struct {
	TaskID              string   `json:"task_id"`                // Task ID to schedule
	Title               string   `json:"title"`                  // Optional: task title for fuzzy search
	Time                string   `json:"time"`                   // Required: HH:MM format (24-hour)
	Repeat              string   `json:"repeat"`                 // once, daily, weekly, monthly, hours, minutes, seconds (default: daily)
	Interval            int      `json:"interval"`               // Optional: repeat interval (e.g., 2 = every 2 days/hours/etc., default: 1)
	Days                []string `json:"days"`                   // Optional: at most one day abbreviation for weekly (mon,tue,wed,thu,fri,sat,sun)
	ClearContextOnStart *bool    `json:"clear_context_on_start"` // Optional; defaults to true for new schedules
}

// DeleteScheduleRequest represents a typed request to delete a schedule entry.
type DeleteScheduleRequest struct {
	ScheduleID string `json:"schedule_id"` // Direct schedule ID
	TaskID     string `json:"task_id"`     // Task ID to find schedule for
	Title      string `json:"title"`       // Optional: task title for fuzzy search
}

// ModifyScheduleRequest represents a typed request to modify a schedule entry.
type ModifyScheduleRequest struct {
	ScheduleID          string   `json:"schedule_id"`            // Direct schedule ID
	TaskID              string   `json:"task_id"`                // Task ID to find schedule for
	Title               string   `json:"title"`                  // Optional: task title for fuzzy search
	Time                string   `json:"time"`                   // New time in HH:MM format (optional)
	Repeat              string   `json:"repeat"`                 // New repeat type (optional)
	Interval            *int     `json:"interval"`               // New interval (optional, pointer to distinguish 0 from unset)
	Days                []string `json:"days"`                   // New weekly day (optional; at most one)
	Enabled             *bool    `json:"enabled"`                // Enable/disable (optional, pointer to distinguish false from unset)
	ClearContextOnStart *bool    `json:"clear_context_on_start"` // Clear model replay context at each scheduled start (optional)
}

// CreateAlertRequest represents a request to create an alert from chat.
type CreateAlertRequest struct {
	Title    string `json:"title"`              // Alert title
	Message  string `json:"message"`            // Alert message/description
	Severity string `json:"severity,omitempty"` // info, warning, error (default: info)
	TaskID   string `json:"task_id,omitempty"`  // Optional task ID
	Type     string `json:"type,omitempty"`     // custom, task_failed, task_needs_followup (default: custom)
}

// DeleteAlertRequest represents a request to delete an alert from chat.
type DeleteAlertRequest struct {
	AlertID string `json:"alert_id"` // Alert ID to delete
}

// ToggleAlertRequest represents a request to toggle an alert's read status from chat.
type ToggleAlertRequest struct {
	AlertID string `json:"alert_id"` // Alert ID to toggle
}

// SetPersonalityRequest represents a request to change the global personality setting.
type SetPersonalityRequest struct {
	Personality string `json:"personality"` // Personality key to set
}

// SwitchProjectRequest represents a request to switch the active project.
type SwitchProjectRequest struct {
	Project string `json:"project"` // Project name or ID to switch to
}

// BuildChatContext builds the full context string for chat prompts, including task,
// model, and schedule information. This is the single source of truth for chat context
// used by both the /chat web handler and the Telegram bot, ensuring consistent responses.
//
// Parameters:
//   - tasks: all tasks in the project (chat tasks will be filtered out)
//   - availableModels: all configured LLM models
//   - schedules: all schedules for the project (may be nil/empty)
//   - now: current time for schedule context formatting
//
// Returns a formatted string with current tasks, available models, and schedule details.
func BuildChatContext(tasks []models.Task, availableModels []models.LLMConfig, schedules []models.Schedule, now time.Time) string {
	return BuildChatContextWithAgentDefinitions(tasks, availableModels, nil, schedules, now)
}

// BuildChatContextWithAgentDefinitions builds the chat context with an optional
// list of Agent definitions that can be assigned via create_task.agent.
func BuildChatContextWithAgentDefinitions(tasks []models.Task, availableModels []models.LLMConfig, agentDefinitions []models.ChatAssignableAgentDefinition, schedules []models.Schedule, now time.Time) string {
	// Filter out chat tasks
	var nonChatTasks []models.Task
	for _, t := range tasks {
		if t.Category != models.CategoryChat {
			nonChatTasks = append(nonChatTasks, t)
		}
	}

	// Create model map for task context
	modelMap := make(map[string]models.LLMConfig, len(availableModels))
	for _, m := range availableModels {
		modelMap[m.ID] = m
	}

	var taskContext string
	if len(nonChatTasks) > 0 {
		taskContext = "Current tasks in this project:\n" + BuildTaskContextWithModels(nonChatTasks, modelMap)
	}
	if modelCtx := BuildModelContextString(availableModels); modelCtx != "" {
		if taskContext != "" {
			taskContext += "\n"
		}
		taskContext += modelCtx
	}
	if agentCtx := BuildAgentDefinitionContextString(agentDefinitions); agentCtx != "" {
		if taskContext != "" {
			taskContext += "\n"
		}
		taskContext += agentCtx
	}

	// Add schedule context
	if len(schedules) > 0 {
		scheduleMap := make(map[string][]models.Schedule, len(schedules))
		for _, s := range schedules {
			scheduleMap[s.TaskID] = append(scheduleMap[s.TaskID], s)
		}
		if schedCtx := BuildScheduleContextString(nonChatTasks, scheduleMap, now); schedCtx != "" {
			if taskContext != "" {
				taskContext += "\n"
			}
			taskContext += schedCtx
		}
	}

	return taskContext
}
