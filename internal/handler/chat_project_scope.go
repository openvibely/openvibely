package handler

import (
	"context"
	"encoding/json"
	"fmt"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
)

// projectSelectedChatHandlers resolves explicit project selections before calling
// the existing handlers. Rebuilding the handler map with a copied request keeps
// project credentials, agent validation, and mutations on the same project and
// avoids shared mutable conversation state during parallel tool calls.
func (h *Handler) projectSelectedChatHandlers(params streamingResponseParams, collector *chatActionSummaryCollector, mode models.ChatMode, surface chatcontrol.Surface) map[string]chatcontrol.RuntimeActionHandler {
	handlers := h.chatActionHandlers(params, collector, mode, surface)
	// Channels keep their existing handlers and project-assertion semantics.
	// Project selection is an interactive web/API capability only.
	if surface != chatcontrol.SurfaceWeb && surface != chatcontrol.SurfaceAPI {
		return handlers
	}
	for name, handler := range handlers {
		if !chatcontrol.SupportsProjectSelection(name) {
			continue
		}
		handlers[name] = func(ctx context.Context, input json.RawMessage) (string, error) {
			projectID, forwarded, err := decodeChatProjectSelection(name, input)
			if err != nil {
				return "", err
			}
			if projectID == models.AllProjectsID || (params.ProjectID == models.AllProjectsID && projectID == "") {
				return "", fmt.Errorf("%s: select a real project_id from list_projects; for multiple projects call this tool once per selected project", name)
			}
			if projectID == "" || projectID == params.ProjectID {
				return handler(ctx, forwarded)
			}
			if params.ProjectID != models.AllProjectsID || params.IsTaskFollowup || params.AutomationContext != nil {
				return "", fmt.Errorf("%s: this conversation is restricted to its current project", name)
			}
			if h.projectSvc == nil {
				return "", fmt.Errorf("%s: project service unavailable", name)
			}
			project, err := h.projectSvc.GetByID(ctx, projectID)
			if err != nil {
				return "", err
			}
			if project == nil {
				return "", fmt.Errorf("%s: unknown project_id %q; use list_projects to select a project", name, projectID)
			}
			scoped := params
			scoped.ProjectID = project.ID
			return h.chatActionHandlers(scoped, collector, mode, surface)[name](ctx, forwarded)
		}
	}

	return handlers
}

func decodeChatProjectSelection(name string, input json.RawMessage) (string, json.RawMessage, error) {
	if len(strings.TrimSpace(string(input))) == 0 {
		return "", input, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return "", nil, fmt.Errorf("%s: invalid tool input: %w", name, err)
	}
	value, exists := fields["project_id"]
	if !exists {
		return "", input, nil
	}
	var projectID string
	if string(value) == "null" {
		return "", nil, fmt.Errorf("%s: project_id must be a string", name)
	}
	if err := json.Unmarshal(value, &projectID); err != nil {
		return "", nil, fmt.Errorf("%s: project_id must be a string", name)
	}
	projectID = strings.TrimSpace(projectID)
	// Some handlers already own a project assertion field. Preserve it so their
	// existing validation remains authoritative after selecting the context.
	def := chatcontrol.Get(name)
	if def == nil || def.Schema == nil || def.Schema.Properties["project_id"] == nil {
		delete(fields, "project_id")
	} else {
		fields["project_id"], _ = json.Marshal(projectID)
	}
	forwarded, err := json.Marshal(fields)
	return projectID, forwarded, err
}

func chatToolDefsForProject(params streamingResponseParams, mode models.ChatMode, surface chatcontrol.Surface, includeThread bool) []llmcontracts.RuntimeToolDefinition {
	if params.ProjectID == models.AllProjectsID && !params.IsTaskFollowup && params.AutomationContext == nil {
		return chatcontrol.ToolDefsForAllProjectsContext(mode, surface, includeThread)
	}
	return chatcontrol.ToolDefsForContext(mode, surface, includeThread)
}
