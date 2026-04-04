package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// TaskCreationRequest represents a task creation request parsed from AI output.
type TaskCreationRequest struct {
	Title             string                     `json:"title"`
	Prompt            string                     `json:"prompt"`
	Category          string                     `json:"category"`            // "active" or "backlog" (default: "backlog")
	Priority          int                        `json:"priority"`            // 1=Low, 2=Normal, 3=High, 4=Urgent (default: 2)
	AgentID           string                     `json:"agent_id"`            // Optional: specific LLM config ID (empty = auto-select or default)
	AgentDefinitionID string                     `json:"agent_definition_id"` // Optional: agent definition ID or name
	Agent             string                     `json:"agent"`               // Optional: agent name (resolved to AgentDefinitionID)
	Chain             *models.ChainConfiguration `json:"chain,omitempty"`     // Optional: chain config for sequential task execution
}

// createTaskMarkerRe matches [CREATE_TASK]{...}[/CREATE_TASK] blocks in agent output.
var createTaskMarkerRe = regexp.MustCompile(`(?s)\[CREATE_TASK\]\s*(.*?)\s*\[/CREATE_TASK\]`)

// ParseTaskCreations extracts task creation requests from AI output.
func ParseTaskCreations(output string) []TaskCreationRequest {
	// Strip [Thinking]...[/Thinking] blocks first — the model may reference
	// marker names inside its thinking (e.g. "I need to output [CREATE_TASK]"),
	// which would cause the regex to match thinking content as JSON.
	output = reThinkingBlock.ReplaceAllString(output, "")
	matches := createTaskMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var tasks []TaskCreationRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req TaskCreationRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[task-creation] error parsing task JSON %q: %v", jsonStr, err)
			continue
		}
		if req.Title == "" {
			log.Printf("[task-creation] skipping task with empty title")
			continue
		}
		// Apply priority default only (category defaulting happens in ExecuteTaskCreationsWithReturn)
		if req.Priority == 0 {
			req.Priority = 2
		}
		tasks = append(tasks, req)
	}
	return tasks
}

// ExecuteTaskCreations creates tasks from parsed requests and returns a summary.
// The summary includes task IDs in the format [TASK_ID:id] so the frontend can
// convert them to clickable links.
// If agents is non-empty, auto-selects an agent for each task based on prompt complexity.
// Tasks with an explicit AgentID in the request skip auto-selection.
func ExecuteTaskCreations(ctx context.Context, requests []TaskCreationRequest, projectID string, taskSvc *TaskService, agents ...[]models.LLMConfig) string {
	_, summary := ExecuteTaskCreationsWithReturn(ctx, requests, projectID, taskSvc, agents...)
	return summary
}

// ExecuteTaskCreationsWithReturn creates tasks from parsed requests and returns both the created tasks and a summary.
// This variant is used when the caller needs access to the created task objects (e.g., to copy attachments).
func ExecuteTaskCreationsWithReturn(ctx context.Context, requests []TaskCreationRequest, projectID string, taskSvc *TaskService, agents ...[]models.LLMConfig) ([]models.Task, string) {
	if len(requests) == 0 {
		return nil, ""
	}

	// Flatten optional agents parameter
	var availableAgents []models.LLMConfig
	if len(agents) > 0 {
		availableAgents = agents[0]
	}

	// Build map of agent ID -> config for fast lookups
	agentConfigMap := make(map[string]*models.LLMConfig)
	for i := range availableAgents {
		agentConfigMap[availableAgents[i].ID] = &availableAgents[i]
	}

	var createdTasks []models.Task
	var created []string
	var failed []string

	for _, req := range requests {
		// Agent selection: explicit > auto-select > default (nil)
		var selectedAgentID string
		var selectionInfo string
		if req.AgentID != "" {
			selectedAgentID = req.AgentID
		} else if len(availableAgents) > 1 {
			complexity := AnalyzeComplexity(req.Prompt)
			result := SelectLLM(complexity, availableAgents)
			if result != nil {
				selectedAgentID = result.LLMConfig.ID
				selectionInfo = FormatSelectionSummary(result)
			}
		} else if len(availableAgents) == 1 {
			// If only one agent available, use it by default
			selectedAgentID = availableAgents[0].ID
			log.Printf("[task-creation] only one agent available, using %s", selectedAgentID)
		}

		// Determine initial category based on auto-start setting
		category := models.TaskCategory(req.Category)

		// If no category was specified, apply auto-start logic or default to backlog
		if req.Category == "" {
			// Check if selected agent has auto-start enabled
			if selectedAgentID != "" {
				if agentConfig, ok := agentConfigMap[selectedAgentID]; ok && agentConfig.AutoStartTasks {
					category = models.CategoryActive
					log.Printf("[task-creation] auto-start enabled for agent %s, setting category to active", selectedAgentID)
				} else {
					category = models.CategoryBacklog
				}
			} else {
				category = models.CategoryBacklog
			}
		} else {
			// Category was explicitly set, validate it
			if category != models.CategoryActive && category != models.CategoryBacklog {
				category = models.CategoryBacklog
			}
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
				log.Printf("[task-creation] error setting chain config for %q: %v", req.Title, err)
			}
		}

		// Set the selected agent ID (LLM config)
		if selectedAgentID != "" {
			task.AgentID = &selectedAgentID
		}

		// Set agent definition ID if provided
		if req.AgentDefinitionID != "" {
			task.AgentDefinitionID = &req.AgentDefinitionID
		}

		if err := taskSvc.Create(ctx, task); err != nil {
			log.Printf("[task-creation] error creating task %q: %v", req.Title, err)
			failed = append(failed, fmt.Sprintf("- \"%s\": %v", req.Title, err))
		} else {
			log.Printf("[task-creation] created task %q id=%s category=%s agent=%v chain=%v selection=%s", req.Title, task.ID, category, task.AgentID, req.Chain != nil && req.Chain.Enabled, selectionInfo)
			createdTasks = append(createdTasks, *task)
			line := fmt.Sprintf("- \"%s\" (%s) [TASK_ID:%s]", req.Title, category, task.ID)
			// Auto-selection info logged server-side but not shown to user to reduce clutter
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

	return createdTasks, summary.String()
}

// TaskEditRequest represents a task edit request parsed from AI output.
type TaskEditRequest struct {
	ID            string                     `json:"id"`                        // Required: task ID to edit
	Title         string                     `json:"title,omitempty"`           // Optional: new title
	Prompt        string                     `json:"prompt,omitempty"`          // Optional: new prompt
	Category      string                     `json:"category,omitempty"`        // Optional: new category
	Priority      int                        `json:"priority,omitempty"`        // Optional: new priority (1-5)
	Tag           string                     `json:"tag,omitempty"`             // Optional: new tag ("feature", "bug", "")
	AgentID       string                     `json:"agent_id,omitempty"`        // Optional: new agent ID (empty = use default)
	AgentConfigID string                     `json:"agent_config_id,omitempty"` // Optional: alias for agent_id (for compatibility)
	Chain         *models.ChainConfiguration `json:"chain,omitempty"`           // Optional: chain config for sequential task execution
	Attachments   []string                   `json:"attachments,omitempty"`     // Optional: file paths to attach to the task
}

// editTaskMarkerRe matches [EDIT_TASK]{...}[/EDIT_TASK] blocks in agent output.
var editTaskMarkerRe = regexp.MustCompile(`(?s)\[EDIT_TASK\]\s*(.*?)\s*\[/EDIT_TASK\]`)

// ParseTaskEdits extracts task edit requests from AI output.
func ParseTaskEdits(output string) []TaskEditRequest {
	output = reThinkingBlock.ReplaceAllString(output, "")
	matches := editTaskMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var edits []TaskEditRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req TaskEditRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[task-edit] error parsing edit JSON %q: %v", jsonStr, err)
			continue
		}
		if req.ID == "" {
			log.Printf("[task-edit] skipping edit with empty task ID")
			continue
		}
		edits = append(edits, req)
	}
	return edits
}

// ExecuteTaskEdits applies edits to existing tasks and returns a summary.
// Only fields that are set (non-zero) in the request are updated.
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
			log.Printf("[task-edit] task not found id=%s: %v", req.ID, err)
			failed = append(failed, fmt.Sprintf("- task %s: not found", req.ID))
			continue
		}

		// Verify task belongs to the same project
		if task.ProjectID != projectID {
			log.Printf("[task-edit] task %s belongs to different project", req.ID)
			failed = append(failed, fmt.Sprintf("- \"%s\": belongs to different project", task.Title))
			continue
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
		if req.Priority > 0 && req.Priority != task.Priority {
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

		// Handle chain configuration
		if req.Chain != nil {
			if err := task.SetChainConfig(req.Chain); err != nil {
				log.Printf("[task-edit] error setting chain config for task %s: %v", req.ID, err)
			} else {
				changes = append(changes, "chain_config")
			}
		}

		// Handle category change separately since it has side effects (auto-submit)
		if req.Category != "" {
			newCategory := models.TaskCategory(req.Category)
			if newCategory != task.Category {
				// Validate category
				validCategory := false
				for _, c := range models.SelectableCategories {
					if newCategory == c {
						validCategory = true
						break
					}
				}
				if validCategory {
					task.Category = newCategory
					changes = append(changes, "category")
				} else {
					log.Printf("[task-edit] invalid category %q for task %s", req.Category, req.ID)
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
			log.Printf("[task-edit] no changes for task %s", req.ID)
			failed = append(failed, fmt.Sprintf("- \"%s\": no changes to apply", task.Title))
			continue
		}

		if err := taskSvc.Update(ctx, task); err != nil {
			log.Printf("[task-edit] error updating task %s: %v", req.ID, err)
			failed = append(failed, fmt.Sprintf("- \"%s\": %v", task.Title, err))
		} else {
			log.Printf("[task-edit] updated task %s fields=%v", req.ID, changes)
			edited = append(edited, fmt.Sprintf("- \"%s\" (%s, updated: %s) [TASK_EDITED:%s]", task.Title, task.Category, strings.Join(changes, ", "), task.ID))
		}
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
		log.Printf("[task-edit] error creating task directory %s: %v", taskDir, err)
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
			log.Printf("[task-edit] attachment file not found %s: %v", srcPath, err)
			continue
		}
		if info.IsDir() {
			log.Printf("[task-edit] attachment path is a directory %s, skipping", srcPath)
			continue
		}
		if info.Size() > 10<<20 { // 10 MB limit
			log.Printf("[task-edit] attachment file too large %s (%d bytes), skipping", srcPath, info.Size())
			continue
		}

		// Copy the file
		fileName := filepath.Base(srcPath)
		destPath := filepath.Join(taskDir, fileName)

		src, err := os.Open(srcPath)
		if err != nil {
			log.Printf("[task-edit] error opening attachment %s: %v", srcPath, err)
			continue
		}

		dest, err := os.Create(destPath)
		if err != nil {
			log.Printf("[task-edit] error creating destination %s: %v", destPath, err)
			src.Close()
			continue
		}

		if _, err := io.Copy(dest, src); err != nil {
			log.Printf("[task-edit] error copying attachment %s: %v", srcPath, err)
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
			log.Printf("[task-edit] error creating attachment record for %s: %v", fileName, err)
			os.Remove(destPath)
			continue
		}

		log.Printf("[task-edit] attached file %s to task %s", fileName, taskID)
		copiedCount++
		copiedNames = append(copiedNames, fileName)
	}

	return copiedCount, copiedNames
}

// BuildTaskContextString creates a summary of existing tasks for system prompts.
// Includes task IDs so the AI can reference them in [EDIT_TASK] blocks.
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
	sb.WriteString("Available models (use the ID in the agent_id field of [EDIT_TASK] to change a task's model):\n")
	for _, c := range configs {
		defaultMark := ""
		if c.IsDefault {
			defaultMark = " (default)"
		}
		sb.WriteString(fmt.Sprintf("- [ID:%s] \"%s\" (model: %s, provider: %s)%s\n", c.ID, c.Name, c.Model, c.Provider, defaultMark))
	}
	return sb.String()
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

// TaskExecutionRequest represents a filter-based execution request parsed from AI output.
type TaskExecutionRequest struct {
	TaskID           string   `json:"task_id"`           // Optional: exact task ID to execute.
	Title            string   `json:"title"`             // Optional: task title query to execute (first match in project).
	Tags             []string `json:"tags"`              // Optional: tags to match (e.g., ["feature", "bug"]). If empty, matches all tags.
	MinPriority      int      `json:"min_priority"`      // Optional: minimum priority (1=Low, 2=Normal, 3=High, 4=Urgent, default: 0 = all)
	IncludeCompleted bool     `json:"include_completed"` // Optional: include completed-status tasks in bulk matching results (default: false).
}

// executeTasksMarkerRe matches [EXECUTE_TASKS]{...}[/EXECUTE_TASKS] blocks in agent output.
var executeTasksMarkerRe = regexp.MustCompile(`(?s)\[EXECUTE_TASKS\]\s*(.*?)\s*\[/EXECUTE_TASKS\]`)

// ParseTaskExecutions extracts task execution requests from AI output.
func ParseTaskExecutions(output string) []TaskExecutionRequest {
	matches := executeTasksMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []TaskExecutionRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req TaskExecutionRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[task-execution] error parsing execution JSON %q: %v", jsonStr, err)
			continue
		}
		if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" && len(req.Tags) == 0 && req.MinPriority == 0 {
			log.Printf("[task-execution] skipping execution request with no task_id/title/tags/priority filter")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// ExecuteTaskExecutions executes tasks matching the parsed requests and returns a summary.
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
			log.Printf("[task-execution] no valid tags found in request")
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
			log.Printf("[task-execution] error executing tasks: %v", err)
			failed = append(failed, fmt.Sprintf("- %s: %v", filterDesc, err))
			continue
		}

		if len(matchedTasks) == 0 {
			log.Printf("[task-execution] no tasks found matching %s", filterDesc)
			failed = append(failed, fmt.Sprintf("- No tasks found matching %s", filterDesc))
			continue
		}

		if submitted == 0 {
			log.Printf("[task-execution] %d tasks matched %s but none could be submitted", len(matchedTasks), filterDesc)
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

// viewThreadMarkerRe matches [VIEW_TASK_CHAT]{...}[/VIEW_TASK_CHAT] blocks in agent output.
var viewThreadMarkerRe = regexp.MustCompile(`(?s)\[VIEW_TASK_CHAT\]\s*(.*?)\s*\[/VIEW_TASK_CHAT\]`)

// ParseViewThread extracts view thread requests from AI output.
func ParseViewThread(output string) []ViewThreadRequest {
	matches := viewThreadMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []ViewThreadRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req ViewThreadRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[thread-view] error parsing view request JSON %q: %v", jsonStr, err)
			continue
		}
		if req.TaskID == "" && req.Title == "" {
			log.Printf("[thread-view] skipping view request with no task_id or title")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// SendToTaskRequest represents a request to send a message to a task's thread.
type SendToTaskRequest struct {
	TaskID  string `json:"task_id"` // Required: task ID to send to
	Title   string `json:"title"`   // Optional: task title for fuzzy search
	Message string `json:"message"` // Required: message to send
}

// sendToTaskMarkerRe matches [SEND_TO_TASK]{...}[/SEND_TO_TASK] blocks in agent output.
var sendToTaskMarkerRe = regexp.MustCompile(`(?s)\[SEND_TO_TASK\]\s*(.*?)\s*\[/SEND_TO_TASK\]`)

// ParseSendToTask extracts send-to-task requests from AI output.
func ParseSendToTask(output string) []SendToTaskRequest {
	matches := sendToTaskMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []SendToTaskRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req SendToTaskRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[thread-send] error parsing send request JSON %q: %v", jsonStr, err)
			continue
		}
		if req.TaskID == "" && req.Title == "" {
			log.Printf("[thread-send] skipping send request with no task_id or title")
			continue
		}
		if req.Message == "" {
			log.Printf("[thread-send] skipping send request with no message")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// ScheduleTaskRequest represents a request to schedule a task parsed from AI output.
type ScheduleTaskRequest struct {
	TaskID   string   `json:"task_id"`  // Task ID to schedule
	Title    string   `json:"title"`    // Optional: task title for fuzzy search
	Time     string   `json:"time"`     // Required: HH:MM format (24-hour)
	Repeat   string   `json:"repeat"`   // once, daily, weekly, monthly, hours, minutes, seconds (default: daily)
	Interval int      `json:"interval"` // Optional: repeat interval (e.g., 2 = every 2 days/hours/etc., default: 1)
	Days     []string `json:"days"`     // Optional: day abbreviations for weekly (mon,tue,wed,thu,fri,sat,sun)
}

// scheduleTaskMarkerRe matches [SCHEDULE_TASK]{...}[/SCHEDULE_TASK] blocks in agent output.
var scheduleTaskMarkerRe = regexp.MustCompile(`(?s)\[SCHEDULE_TASK\]\s*(.*?)\s*\[/SCHEDULE_TASK\]`)

// ParseScheduleTask extracts schedule task requests from AI output.
func ParseScheduleTask(output string) []ScheduleTaskRequest {
	matches := scheduleTaskMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []ScheduleTaskRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req ScheduleTaskRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[schedule-task] error parsing schedule request JSON %q: %v", jsonStr, err)
			continue
		}
		if req.TaskID == "" && req.Title == "" {
			log.Printf("[schedule-task] skipping schedule request with no task_id or title")
			continue
		}
		if req.Time == "" {
			log.Printf("[schedule-task] skipping schedule request with no time")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// DeleteScheduleRequest represents a request to delete a schedule entry parsed from AI output.
type DeleteScheduleRequest struct {
	ScheduleID string `json:"schedule_id"` // Direct schedule ID
	TaskID     string `json:"task_id"`     // Task ID to find schedule for
	Title      string `json:"title"`       // Optional: task title for fuzzy search
}

// deleteScheduleMarkerRe matches [DELETE_SCHEDULE]{...}[/DELETE_SCHEDULE] blocks in agent output.
var deleteScheduleMarkerRe = regexp.MustCompile(`(?s)\[DELETE_SCHEDULE\]\s*(.*?)\s*\[/DELETE_SCHEDULE\]`)

// ParseDeleteSchedule extracts delete schedule requests from AI output.
func ParseDeleteSchedule(output string) []DeleteScheduleRequest {
	matches := deleteScheduleMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []DeleteScheduleRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req DeleteScheduleRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[delete-schedule] error parsing JSON %q: %v", jsonStr, err)
			continue
		}
		if req.ScheduleID == "" && req.TaskID == "" && req.Title == "" {
			log.Printf("[delete-schedule] skipping request with no schedule_id, task_id, or title")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// ModifyScheduleRequest represents a request to modify a schedule entry parsed from AI output.
type ModifyScheduleRequest struct {
	ScheduleID string   `json:"schedule_id"` // Direct schedule ID
	TaskID     string   `json:"task_id"`     // Task ID to find schedule for
	Title      string   `json:"title"`       // Optional: task title for fuzzy search
	Time       string   `json:"time"`        // New time in HH:MM format (optional)
	Repeat     string   `json:"repeat"`      // New repeat type (optional)
	Interval   *int     `json:"interval"`    // New interval (optional, pointer to distinguish 0 from unset)
	Days       []string `json:"days"`        // New days for weekly (optional)
	Enabled    *bool    `json:"enabled"`     // Enable/disable (optional, pointer to distinguish false from unset)
}

// modifyScheduleMarkerRe matches [MODIFY_SCHEDULE]{...}[/MODIFY_SCHEDULE] blocks in agent output.
var modifyScheduleMarkerRe = regexp.MustCompile(`(?s)\[MODIFY_SCHEDULE\]\s*(.*?)\s*\[/MODIFY_SCHEDULE\]`)

// ParseModifySchedule extracts modify schedule requests from AI output.
func ParseModifySchedule(output string) []ModifyScheduleRequest {
	matches := modifyScheduleMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []ModifyScheduleRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req ModifyScheduleRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[modify-schedule] error parsing JSON %q: %v", jsonStr, err)
			continue
		}
		if req.ScheduleID == "" && req.TaskID == "" && req.Title == "" {
			log.Printf("[modify-schedule] skipping request with no schedule_id, task_id, or title")
			continue
		}
		requests = append(requests, req)
	}
	return requests
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

// createAlertMarkerRe matches [CREATE_ALERT]{...}[/CREATE_ALERT] blocks.
var createAlertMarkerRe = regexp.MustCompile(`(?s)\[CREATE_ALERT\]\s*(.*?)\s*\[/CREATE_ALERT\]`)

// deleteAlertMarkerRe matches [DELETE_ALERT]{...}[/DELETE_ALERT] blocks.
var deleteAlertMarkerRe = regexp.MustCompile(`(?s)\[DELETE_ALERT\]\s*(.*?)\s*\[/DELETE_ALERT\]`)

// toggleAlertMarkerRe matches [TOGGLE_ALERT]{...}[/TOGGLE_ALERT] blocks.
var toggleAlertMarkerRe = regexp.MustCompile(`(?s)\[TOGGLE_ALERT\]\s*(.*?)\s*\[/TOGGLE_ALERT\]`)

// listAlertsMarkerRe matches [LIST_ALERTS] markers (no JSON body needed).
var listAlertsMarkerRe = regexp.MustCompile(`\[LIST_ALERTS\]`)

// ParseCreateAlert extracts create alert requests from AI output.
func ParseCreateAlert(output string) []CreateAlertRequest {
	matches := createAlertMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []CreateAlertRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req CreateAlertRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[create-alert] error parsing JSON %q: %v", jsonStr, err)
			continue
		}
		if req.Title == "" {
			log.Printf("[create-alert] skipping request with no title")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// ParseDeleteAlert extracts delete alert requests from AI output.
func ParseDeleteAlert(output string) []DeleteAlertRequest {
	matches := deleteAlertMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []DeleteAlertRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req DeleteAlertRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[delete-alert] error parsing JSON %q: %v", jsonStr, err)
			continue
		}
		if req.AlertID == "" {
			log.Printf("[delete-alert] skipping request with no alert_id")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// ParseToggleAlert extracts toggle alert requests from AI output.
func ParseToggleAlert(output string) []ToggleAlertRequest {
	matches := toggleAlertMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []ToggleAlertRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req ToggleAlertRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[toggle-alert] error parsing JSON %q: %v", jsonStr, err)
			continue
		}
		if req.AlertID == "" {
			log.Printf("[toggle-alert] skipping request with no alert_id")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// HasListAlerts checks if the output contains a [LIST_ALERTS] marker.
func HasListAlerts(output string) bool {
	return listAlertsMarkerRe.MatchString(output)
}

// SetPersonalityRequest represents a request to change the global personality setting.
type SetPersonalityRequest struct {
	Personality string `json:"personality"` // Personality key to set
}

// setPersonalityMarkerRe matches [SET_PERSONALITY]{...}[/SET_PERSONALITY] blocks in agent output.
var setPersonalityMarkerRe = regexp.MustCompile(`(?s)\[SET_PERSONALITY\]\s*(.*?)\s*\[/SET_PERSONALITY\]`)

// listPersonalitiesMarkerRe matches [LIST_PERSONALITIES] markers (no JSON body needed).
var listPersonalitiesMarkerRe = regexp.MustCompile(`\[LIST_PERSONALITIES\]`)

// listModelsMarkerRe matches [LIST_MODELS] markers.
var listModelsMarkerRe = regexp.MustCompile(`\[LIST_MODELS\]`)

// viewSettingsMarkerRe matches [VIEW_SETTINGS] markers.
var viewSettingsMarkerRe = regexp.MustCompile(`\[VIEW_SETTINGS\]`)

// projectInfoMarkerRe matches [PROJECT_INFO] markers.
var projectInfoMarkerRe = regexp.MustCompile(`\[PROJECT_INFO\]`)

// listAgentsMarkerRe matches [LIST_AGENTS] markers.
var listAgentsMarkerRe = regexp.MustCompile(`\[LIST_AGENTS\]`)

// listProjectsMarkerRe matches [LIST_PROJECTS] markers.
var listProjectsMarkerRe = regexp.MustCompile(`\[LIST_PROJECTS\]`)

// switchProjectMarkerRe matches [SWITCH_PROJECT]{...}[/SWITCH_PROJECT] blocks.
var switchProjectMarkerRe = regexp.MustCompile(`(?s)\[SWITCH_PROJECT\]\s*(.*?)\s*\[/SWITCH_PROJECT\]`)

// SwitchProjectRequest represents a request to switch the active project.
type SwitchProjectRequest struct {
	Project string `json:"project"` // Project name or ID to switch to
}

// HasListProjects checks if the output contains a [LIST_PROJECTS] marker.
func HasListProjects(output string) bool {
	return listProjectsMarkerRe.MatchString(output)
}

// ParseSwitchProject extracts switch project requests from AI output.
func ParseSwitchProject(output string) []SwitchProjectRequest {
	matches := switchProjectMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []SwitchProjectRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req SwitchProjectRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[switch-project] error parsing JSON %q: %v", jsonStr, err)
			continue
		}
		if req.Project == "" {
			log.Printf("[switch-project] skipping request with empty project name")
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// ParseSetPersonality extracts set personality requests from AI output.
func ParseSetPersonality(output string) []SetPersonalityRequest {
	matches := setPersonalityMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}

	var requests []SetPersonalityRequest
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(match[1])
		var req SetPersonalityRequest
		if err := json.Unmarshal([]byte(jsonStr), &req); err != nil {
			log.Printf("[set-personality] error parsing JSON %q: %v", jsonStr, err)
			continue
		}
		requests = append(requests, req)
	}
	return requests
}

// HasListPersonalities checks if the output contains a [LIST_PERSONALITIES] marker.
func HasListPersonalities(output string) bool {
	return listPersonalitiesMarkerRe.MatchString(output)
}

// HasListModels checks if the output contains a [LIST_MODELS] marker.
func HasListModels(output string) bool {
	return listModelsMarkerRe.MatchString(output)
}

// HasViewSettings checks if the output contains a [VIEW_SETTINGS] marker.
func HasViewSettings(output string) bool {
	return viewSettingsMarkerRe.MatchString(output)
}

// HasListAgents checks if the output contains a [LIST_AGENTS] marker.
func HasListAgents(output string) bool {
	return listAgentsMarkerRe.MatchString(output)
}

// HasProjectInfo checks if the output contains a [PROJECT_INFO] marker.
func HasProjectInfo(output string) bool {
	return projectInfoMarkerRe.MatchString(output)
}

// MissingMarkerWarning describes a case where the LLM response appears to promise
// an action but doesn't contain the corresponding marker block.
type MissingMarkerWarning struct {
	Action      string // e.g., "view_thread", "send_to_task"
	MarkerName  string // e.g., "[VIEW_TASK_CHAT]", "[SEND_TO_TASK]"
	MatchedHint string // the phrase that triggered the detection
}

// intentPhrases maps marker types to phrases that indicate the LLM intended to
// perform an action. Each entry is a list of lowercase phrases to match against
// the lowercased output.
var intentPhrases = map[string]struct {
	MarkerName string
	Phrases    []string
}{
	"view_thread": {
		MarkerName: "[VIEW_TASK_CHAT]",
		Phrases: []string{
			"let me retrieve the thread",
			"let me fetch the thread",
			"let me get the thread history",
			"let me pull up the thread",
			"i'll retrieve the thread",
			"i'll fetch the thread",
			"i'll get the thread history",
			"i'll pull up the thread",
			"i'll show you the thread",
			"i'll view the task",
			"let me check the task output",
			"let me get the output",
			"let me retrieve the execution",
			"i'll retrieve the execution",
			"i'll get the execution",
			"i'll check the output",
			"let me view the task",
			"retrieving the thread history",
			"fetching the execution history",
		},
	},
	"send_to_task": {
		MarkerName: "[SEND_TO_TASK]",
		Phrases: []string{
			"i'll send that to the task",
			"i'll send that instruction",
			"i'll send that message",
			"i'll tell the task",
			"i'll forward that to",
			"let me send that to",
			"let me tell the task",
			"sending that to the task",
			"sending the message to",
			"i'll pass that along to",
			"i'll relay that to",
			"i'll communicate that to",
			"let me send a message to",
			"i'll send a follow-up",
			"let me send a follow-up",
		},
	},
	"create_task": {
		MarkerName: "[CREATE_TASK]",
		Phrases: []string{
			"i'll create that task",
			"i'll create a task for",
			"let me create that task",
			"let me create a task for",
			"creating that task now",
			"i'll add that as a task",
		},
	},
	"schedule_task": {
		MarkerName: "[SCHEDULE_TASK]",
		Phrases: []string{
			"i'll schedule that task",
			"i'll schedule the task",
			"let me schedule that",
			"let me schedule the task",
			"i'll set up a schedule",
			"i'll set that up to run",
			"scheduling that task",
			"i'll configure the schedule",
			"let me set up the schedule",
		},
	},
	"delete_schedule": {
		MarkerName: "[DELETE_SCHEDULE]",
		Phrases: []string{
			"i'll delete that schedule",
			"i'll remove that schedule",
			"let me delete the schedule",
			"let me remove the schedule",
			"deleting that schedule",
			"removing that schedule",
			"i'll cancel that schedule",
			"let me cancel the schedule",
			"i'll unschedule that task",
			"let me unschedule that",
		},
	},
	"modify_schedule": {
		MarkerName: "[MODIFY_SCHEDULE]",
		Phrases: []string{
			"i'll modify that schedule",
			"i'll update the schedule",
			"let me modify the schedule",
			"let me update the schedule",
			"i'll change the schedule",
			"let me change the schedule",
			"modifying the schedule",
			"updating the schedule",
			"i'll adjust the schedule",
			"let me adjust the schedule",
		},
	},
	"list_personalities": {
		MarkerName: "[LIST_PERSONALITIES]",
		Phrases: []string{
			"let me list the personalities",
			"i'll show you the available personalities",
			"here are the personalities",
			"let me show you the personality options",
		},
	},
	"set_personality": {
		MarkerName: "[SET_PERSONALITY]",
		Phrases: []string{
			"i'll change the personality",
			"i'll set the personality",
			"let me change the personality",
			"let me update the personality",
			"changing the personality",
			"setting the personality to",
		},
	},
	"list_models": {
		MarkerName: "[LIST_MODELS]",
		Phrases: []string{
			"let me list the models",
			"i'll show you the available models",
			"here are the configured models",
			"let me show you the models",
		},
	},
	"view_settings": {
		MarkerName: "[VIEW_SETTINGS]",
		Phrases: []string{
			"let me show you the settings",
			"i'll retrieve the settings",
			"let me get the current settings",
			"here are the current settings",
			"i'll show you the app settings",
		},
	},
	"project_info": {
		MarkerName: "[PROJECT_INFO]",
		Phrases: []string{
			"let me get the project info",
			"i'll show you the project details",
			"let me retrieve the project information",
			"here's the project info",
			"i'll look up the project details",
		},
	},
	"list_agents": {
		MarkerName: "[LIST_AGENTS]",
		Phrases: []string{
			"let me list the agents",
			"i'll show you the agents",
			"here are the configured agents",
			"let me show you the available agents",
			"here are your agents",
		},
	},
	"list_alerts": {
		MarkerName: "[LIST_ALERTS]",
		Phrases: []string{
			"let me list the alerts",
			"i'll show you the alerts",
			"here are the alerts",
			"let me check the alerts",
			"i'll retrieve the alerts",
			"let me show you the current alerts",
		},
	},
	"create_alert": {
		MarkerName: "[CREATE_ALERT]",
		Phrases: []string{
			"i'll create that alert",
			"i'll create an alert",
			"let me create that alert",
			"let me create an alert",
			"creating that alert",
			"i'll set up an alert",
			"let me set up an alert",
		},
	},
	"delete_alert": {
		MarkerName: "[DELETE_ALERT]",
		Phrases: []string{
			"i'll delete that alert",
			"i'll remove that alert",
			"let me delete the alert",
			"let me remove the alert",
			"deleting that alert",
			"removing that alert",
		},
	},
	"toggle_alert": {
		MarkerName: "[TOGGLE_ALERT]",
		Phrases: []string{
			"i'll mark that alert",
			"i'll toggle that alert",
			"let me mark the alert",
			"let me toggle the alert",
			"marking that alert",
			"toggling that alert",
		},
	},
}

// DetectMissingMarkers checks if the LLM output contains phrases that suggest
// it intended to perform an action but did not include the corresponding marker.
// Returns a list of warnings for each detected case.
func DetectMissingMarkers(output string) []MissingMarkerWarning {
	if output == "" {
		return nil
	}

	lower := strings.ToLower(output)
	var warnings []MissingMarkerWarning

	for action, config := range intentPhrases {
		// Skip if the marker is actually present in the output
		if strings.Contains(output, config.MarkerName) {
			continue
		}

		// Check if any intent phrase matches
		for _, phrase := range config.Phrases {
			if strings.Contains(lower, phrase) {
				warnings = append(warnings, MissingMarkerWarning{
					Action:      action,
					MarkerName:  config.MarkerName,
					MatchedHint: phrase,
				})
				break // One match per action type is enough
			}
		}
	}

	return warnings
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
