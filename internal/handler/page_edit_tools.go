package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/memory"
	"path/filepath"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/pageintegration"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

// pageEditTools uses persistent instance-owned files and publishes only on an explicit publish_custom_page call.
func (h *Handler) pageEditTools(ctx context.Context, binding pageintegration.Conversation, principal string) (*llmcontracts.RuntimeTools, string, error) {
	dir, err := h.pagePlugins.EditWorkspace(ctx, binding.Page.InstanceID)
	if err != nil {
		return nil, "", err
	}
	rt := &llmcontracts.RuntimeTools{}
	names := map[string]bool{}
	for _, def := range agenttools.Definitions() {
		names[def.Name] = true
		access := llmcontracts.RuntimeToolAccessWrite
		if def.Name == "read_file" || def.Name == "list_files" || def.Name == "grep_search" {
			access = llmcontracts.RuntimeToolAccessRead
		}
		rt.Definitions = append(rt.Definitions, llmcontracts.RuntimeToolDefinition{Name: def.Name, Description: def.Description + " Work in the current page files. Files persist across replies. Edit and build first, then call publish_custom_page to validate and refresh the page. Editing tools do not publish. There is no rollback copy.", Schema: def.Schema, Access: access})
	}
	names["publish_custom_page"] = true
	rt.Definitions = append(rt.Definitions, llmcontracts.RuntimeToolDefinition{
		Name: "publish_custom_page", Description: "Validate and publish the current page files after editing and building. Restarts its backend and refreshes its frame. Bound to this page; no manual approval. Failed edits remain in place; no rollback.",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Access: llmcontracts.RuntimeToolAccessWrite,
	})
	rt.Filter = func(name string) (bool, bool) {
		return names[name], names[name]
	}
	rt.Executor = func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if !names[name] {
			return "", false, false, nil
		}
		fail := func(err error) (string, bool, bool, error) { return err.Error(), true, true, nil }
		if err := h.authorizePageTurn(ctx, principal, binding.Page.ProjectID); err != nil {
			return fail(err)
		}
		if _, err := binding.Resolve(ctx); err != nil {
			return fail(err)
		}
		if name == "publish_custom_page" {
			var args map[string]json.RawMessage
			if err := json.Unmarshal(input, &args); err != nil {
				return fail(err)
			}
			if len(args) != 0 {
				return fail(fmt.Errorf("publish_custom_page uses this page's files and accepts no arguments"))
			}
			result, err := h.pagePlugins.PublishEdit(ctx, binding.Page.InstanceID, binding.Page.ProjectID, principal)
			if err != nil {
				return fail(fmt.Errorf("publication failed; files remain edited: %w", err))
			}
			if h.broadcaster != nil {
				h.broadcaster.Publish(events.TaskEvent{Type: events.PluginPagesChanged, ProjectID: binding.Page.ProjectID, TaskID: binding.Page.InstanceID})
			}
			return fmt.Sprintf("Page published (%s). Frame refresh requested.", result.Instance.Version), true, false, nil
		}
		// Reuse standard tool implementations while constraining their file paths.
		if name != "shell" && name != "process" {
			var args map[string]json.RawMessage
			if err := json.Unmarshal(input, &args); err != nil {
				return fail(err)
			}
			key := "file_path"
			if name == "list_files" || name == "grep_search" {
				key = "path"
			}
			path := "."
			if raw, ok := args[key]; ok {
				if err := json.Unmarshal(raw, &path); err != nil {
					return fail(err)
				}
			}
			if filepath.IsAbs(path) {
				return fail(fmt.Errorf("use a path relative to the page files"))
			}
			if err := memory.AssertPathWithin(dir, filepath.Join(dir, path)); err != nil {
				return fail(fmt.Errorf("page files path: %w", err))
			}
		}
		output, err := agenttools.Execute(ctx, dir, name, input)
		if err != nil {
			return fail(err)
		}

		return output, true, false, nil
	}
	return rt, dir, nil
}
