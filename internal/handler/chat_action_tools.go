package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func supportsChatActionTools(agent models.LLMConfig) bool {
	switch agent.Provider {
	case models.ProviderOpenAI:
		return agent.IsOpenAIAPIKey() || agent.IsOpenAIOAuth()
	case models.ProviderAnthropic:
		return agent.IsAnthropicAPIKey() || agent.IsOAuth()
	default:
		return false
	}
}

type chatActionSummaryCollector struct {
	createdLines []string
	editedLines  []string
}

func newChatActionSummaryCollector() *chatActionSummaryCollector {
	return &chatActionSummaryCollector{
		createdLines: []string{},
		editedLines:  []string{},
	}
}

func (c *chatActionSummaryCollector) addCreated(summary string) {
	c.addMarkerLines(summary, "[TASK_ID:", &c.createdLines)
}

func (c *chatActionSummaryCollector) addEdited(summary string) {
	c.addMarkerLines(summary, "[TASK_EDITED:", &c.editedLines)
}

func (c *chatActionSummaryCollector) addMarkerLines(summary, marker string, target *[]string) {
	if c == nil || summary == "" || marker == "" {
		return
	}
	for _, raw := range strings.Split(summary, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, marker) {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			line = "- " + line
		}
		if containsSummaryLine(*target, line) {
			continue
		}
		*target = append(*target, line)
	}
}

func containsSummaryLine(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (c *chatActionSummaryCollector) appendToOutput(output string) string {
	if c == nil {
		return output
	}
	var blocks []string
	if len(c.createdLines) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Created %d task(s):\n", len(c.createdLines)))
		b.WriteString(strings.Join(c.createdLines, "\n"))
		blocks = append(blocks, b.String())
	}
	if len(c.editedLines) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Edited %d task(s):\n", len(c.editedLines)))
		b.WriteString(strings.Join(c.editedLines, "\n"))
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return output
	}

	summary := "\n\n---\n" + strings.Join(blocks, "\n\n")
	if strings.Contains(output, summary) {
		return output
	}
	return output + summary
}

// buildChatActionToolRuntime creates a RuntimeTools using the canonical registry.
// Tool definitions come from chatcontrol.ToolDefsForContext; execution delegates
// to the same marker-processing methods, keeping behavior identical.
func (h *Handler) buildChatActionToolRuntime(params streamingResponseParams, collector *chatActionSummaryCollector) *llmcontracts.RuntimeTools {
	surface := chatcontrol.SurfaceWeb
	mode := params.ChatMode
	if mode == "" {
		mode = models.ChatModeOrchestrate
	}
	// Web handler includes thread tools for orchestrate mode
	includeThread := mode == models.ChatModeOrchestrate
	defs := chatcontrol.ToolDefsForContext(mode, surface, includeThread)

	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

// buildAPIChatActionToolRuntime creates a RuntimeTools for the API surface.
func (h *Handler) buildAPIChatActionToolRuntime(params streamingResponseParams, collector *chatActionSummaryCollector) *llmcontracts.RuntimeTools {
	surface := chatcontrol.SurfaceAPI
	mode := params.ChatMode
	if mode == "" {
		mode = models.ChatModeOrchestrate
	}
	includeThread := mode == models.ChatModeOrchestrate
	defs := chatcontrol.ToolDefsForContext(mode, surface, includeThread)

	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

// chatActionExecutor returns a RuntimeToolExecutor that uses the canonical
// chatcontrol execution engine with a surface-aware action handler map.
func (h *Handler) chatActionExecutor(params streamingResponseParams, collector *chatActionSummaryCollector, mode models.ChatMode, surface chatcontrol.Surface) llmcontracts.RuntimeToolExecutor {
	handlers := h.chatActionHandlers(params, collector, mode, surface)
	return chatcontrol.BuildRuntimeToolExecutor(mode, surface, handlers)
}

func (h *Handler) chatActionHandlers(params streamingResponseParams, collector *chatActionSummaryCollector, mode models.ChatMode, surface chatcontrol.Surface) map[string]chatcontrol.RuntimeActionHandler {
	return map[string]chatcontrol.RuntimeActionHandler{
		"create_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("CREATE_TASK", input, true)
			if err != nil {
				return "", err
			}
			agents, err := h.llmConfigRepo.List(ctx)
			if err != nil {
				agents = nil
			}
			updated, _ := h.processChatTaskCreations(ctx, params.ExecID, params.ProjectID, marker, agents)
			summary := toolSummaryFromMarker(marker, updated)
			if collector != nil {
				collector.addCreated(summary)
			}
			return summary, nil
		},
		"edit_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("EDIT_TASK", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatTaskEdits(ctx, params.ExecID, params.ProjectID, marker)
			summary := toolSummaryFromMarker(marker, updated)
			if collector != nil {
				collector.addEdited(summary)
			}
			return summary, nil
		},
		"execute_tasks": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("EXECUTE_TASKS", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatTaskExecutions(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"view_task_thread": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("VIEW_TASK_CHAT", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processViewThread(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"send_to_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("SEND_TO_TASK", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatSendToTask(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"schedule_task": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("SCHEDULE_TASK", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatScheduleTask(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"delete_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("DELETE_SCHEDULE", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatDeleteSchedule(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"modify_schedule": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("MODIFY_SCHEDULE", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatModifySchedule(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"list_personalities": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("LIST_PERSONALITIES", input, false)
			if err != nil {
				return "", err
			}
			updated := h.processChatListPersonalities(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"get_personality": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return h.executeGetPersonality(ctx), nil
		},
		"set_personality": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("SET_PERSONALITY", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatSetPersonality(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"list_models": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("LIST_MODELS", input, false)
			if err != nil {
				return "", err
			}
			updated := h.processChatListModels(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"get_model": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGetModel(ctx, input), nil
		},
		"list_agents": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("LIST_AGENTS", input, false)
			if err != nil {
				return "", err
			}
			updated := h.processChatListAgents(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"view_settings": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("VIEW_SETTINGS", input, false)
			if err != nil {
				return "", err
			}
			updated := h.processChatViewSettings(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"project_info": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("PROJECT_INFO", input, false)
			if err != nil {
				return "", err
			}
			updated := h.processChatProjectInfo(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"get_current_project": func(ctx context.Context, _ json.RawMessage) (string, error) {
			return h.executeGetCurrentProject(ctx, params.ProjectID), nil
		},
		"list_projects": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("LIST_PROJECTS", input, false)
			if err != nil {
				return "", err
			}
			updated := h.processChatListProjects(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"switch_project": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeSwitchProject(ctx, params.ProjectID, input), nil
		},
		"list_alerts": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("LIST_ALERTS", input, false)
			if err != nil {
				return "", err
			}
			updated := h.processChatListAlerts(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"get_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			return h.executeGetAlert(ctx, params.ProjectID, input), nil
		},
		"create_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("CREATE_ALERT", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatCreateAlert(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"delete_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("DELETE_ALERT", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatDeleteAlert(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"toggle_alert": func(ctx context.Context, input json.RawMessage) (string, error) {
			marker, err := buildToolMarker("TOGGLE_ALERT", input, true)
			if err != nil {
				return "", err
			}
			updated := h.processChatToggleAlert(ctx, params.ExecID, params.ProjectID, marker)
			return toolSummaryFromMarker(marker, updated), nil
		},
		"get_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return fmt.Sprintf("Current chat mode: %s", mode), nil
		},
		"set_chat_mode": func(_ context.Context, input json.RawMessage) (string, error) {
			var req struct {
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(input, &req); err != nil {
				return "", fmt.Errorf("invalid input: %w", err)
			}
			newMode := models.NormalizeChatMode(req.Mode)
			return fmt.Sprintf("Chat mode set to %s. The mode change will take effect on the next message.", newMode), nil
		},
		"list_capabilities": func(_ context.Context, _ json.RawMessage) (string, error) {
			summaries := chatcontrol.ListForContext(mode, surface)
			return formatCapabilities(summaries), nil
		},
	}
}

// ---- New action executors ----

func (h *Handler) executeGetPersonality(ctx context.Context) string {
	if h.settingsRepo == nil {
		return "Current personality: default (no personality set)"
	}
	current, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil {
		log.Printf("[handler] executeGetPersonality error: %v", err)
		return "Error retrieving personality setting."
	}
	if current == "" {
		return "Current personality: default (base, no personality modifier active)"
	}
	return fmt.Sprintf("Current personality: %s", current)
}

func (h *Handler) executeGetModel(ctx context.Context, input json.RawMessage) string {
	var req struct {
		ModelID string `json:"model_id"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return "Invalid input for get_model."
	}

	configs, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		log.Printf("[handler] executeGetModel error: %v", err)
		return "Error retrieving model configurations."
	}

	for _, c := range configs {
		if (req.ModelID != "" && c.ID == req.ModelID) ||
			(req.Name != "" && strings.EqualFold(c.Name, req.Name)) {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			workerInfo := ""
			if c.MaxWorkers > 0 {
				workerInfo = fmt.Sprintf(", max_workers: %d", c.MaxWorkers)
			}
			return fmt.Sprintf("Model: %s%s\n  Provider: %s\n  Model ID: %s\n  Auth: %s%s",
				c.Name, defaultStr, c.Provider, c.Model, c.AuthMethod, workerInfo)
		}
	}

	if req.ModelID != "" {
		return fmt.Sprintf("Model with id %q not found.", req.ModelID)
	}
	return fmt.Sprintf("Model with name %q not found.", req.Name)
}

func (h *Handler) executeGetCurrentProject(ctx context.Context, projectID string) string {
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return fmt.Sprintf("Current project ID: %s (details unavailable)", projectID)
	}
	desc := ""
	if project.Description != "" {
		desc = fmt.Sprintf("\nDescription: %s", project.Description)
	}
	repo := ""
	if project.RepoPath != "" {
		repo = fmt.Sprintf("\nRepository: %s", project.RepoPath)
	}
	return fmt.Sprintf("Current project: %s (id: %s)%s%s", project.Name, project.ID, desc, repo)
}

func (h *Handler) executeSwitchProject(ctx context.Context, currentProjectID string, input json.RawMessage) string {
	var req struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return "Invalid input for switch_project."
	}
	target := strings.TrimSpace(req.Project)
	if target == "" {
		return "switch_project requires a project name or ID."
	}

	projects, err := h.projectRepo.List(ctx)
	if err != nil {
		return "Error loading projects: " + err.Error()
	}

	for _, p := range projects {
		if strings.EqualFold(p.Name, target) || p.ID == target {
			// For web/API, the project switch is informational — the frontend
			// manages the active project_id. Return the target so the model
			// can communicate the switch to the user.
			return fmt.Sprintf("Switched to project: %s (id: %s). Use this project for subsequent actions.", p.Name, p.ID)
		}
	}

	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return fmt.Sprintf("Project not found: %q. Available projects: %s", target, strings.Join(names, ", "))
}

func (h *Handler) executeGetAlert(ctx context.Context, projectID string, input json.RawMessage) string {
	var req struct {
		AlertID string `json:"alert_id"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return "Invalid input for get_alert."
	}
	if req.AlertID == "" {
		return "get_alert requires alert_id."
	}

	if h.alertSvc == nil {
		return "Alert service not available."
	}

	alert, err := h.alertSvc.GetByID(ctx, req.AlertID)
	if err != nil {
		log.Printf("[handler] executeGetAlert error: %v", err)
		return fmt.Sprintf("Error retrieving alert %q: %v", req.AlertID, err)
	}
	if alert == nil {
		return fmt.Sprintf("Alert %q not found.", req.AlertID)
	}

	readStr := "unread"
	if alert.IsRead {
		readStr = "read"
	}
	taskStr := ""
	if alert.TaskID != nil {
		taskStr = fmt.Sprintf("\nTask: %s", *alert.TaskID)
	}
	return fmt.Sprintf("Alert: %s\n  ID: %s\n  Type: %s\n  Severity: %s\n  Status: %s\n  Message: %s%s\n  Created: %s",
		alert.Title, alert.ID, alert.Type, alert.Severity, readStr,
		alert.Message, taskStr, alert.CreatedAt.Format("Jan 2, 2006 3:04 PM"))
}

func formatCapabilities(summaries []chatcontrol.ActionSummary) string {
	if len(summaries) == 0 {
		return "No capabilities available in the current mode."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Available capabilities (%d actions):\n", len(summaries)))
	currentDomain := ""
	for _, s := range summaries {
		if s.Domain != currentDomain {
			currentDomain = s.Domain
			sb.WriteString(fmt.Sprintf("\n[%s]\n", currentDomain))
		}
		accessTag := ""
		if s.Access == "write" {
			accessTag = " (write)"
		}
		sb.WriteString(fmt.Sprintf("  - %s%s: %s\n", s.Name, accessTag, s.Description))
	}
	return sb.String()
}

func buildToolMarker(markerName string, input json.RawMessage, hasBody bool) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(markerName))
	if upper == "" {
		return "", fmt.Errorf("marker name is required")
	}
	if !hasBody {
		return "[" + upper + "]", nil
	}
	payload := "{}"
	if len(strings.TrimSpace(string(input))) > 0 {
		var tmp map[string]interface{}
		if err := json.Unmarshal(input, &tmp); err != nil {
			return "", fmt.Errorf("invalid tool input JSON: %w", err)
		}
		b, err := json.Marshal(tmp)
		if err != nil {
			return "", fmt.Errorf("marshal tool input: %w", err)
		}
		payload = string(b)
	}
	return fmt.Sprintf("[%s]\n%s\n[/%s]", upper, payload, upper), nil
}

func toolSummaryFromMarker(marker, updated string) string {
	if marker == "" {
		return strings.TrimSpace(updated)
	}
	summary := strings.TrimPrefix(updated, marker)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "Action completed."
	}
	return summary
}

// buildChatActionToolRuntimeFromDefs creates a RuntimeTools from pre-computed tool
// definitions and the shared executor. Used by processStreamingResponse which
// computes defs from the registry before calling this.
func (h *Handler) buildChatActionToolRuntimeFromDefs(params streamingResponseParams, collector *chatActionSummaryCollector, defs []llmcontracts.RuntimeToolDefinition, mode models.ChatMode, surface chatcontrol.Surface) *llmcontracts.RuntimeTools {
	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

// chatActionToolDefinitions returns tool definitions from the canonical registry.
// Kept for backward compatibility with existing tests.
func chatActionToolDefinitions() []llmcontracts.RuntimeToolDefinition {
	return chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)
}
