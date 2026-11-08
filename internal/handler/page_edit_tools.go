package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/memory"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/pageintegration"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

// pageEditTools adapts the existing coding tools to a copy of this page's bundle.
// File writes cannot modify the installed package; apply_page_changes is the only
// publication path and rechecks the binding and current revision.
func (h *Handler) pageEditTools(ctx context.Context, binding pageintegration.Conversation, principal string) (*llmcontracts.RuntimeTools, func(), string, error) {
	var mu sync.Mutex
	dir, baseHash, err := h.pagePlugins.EditWorkspace(ctx, binding.Page.InstanceID)
	if err != nil {
		return nil, nil, "", err
	}
	cleanup := func() {
		mu.Lock()
		defer mu.Unlock()
		if dir != "" {
			os.RemoveAll(dir)
			dir = ""
		}
	}
	rt := &llmcontracts.RuntimeTools{}
	names := map[string]bool{}
	for _, def := range agenttools.Definitions() {
		names[def.Name] = true
		access := llmcontracts.RuntimeToolAccessWrite
		if def.Name == "read_file" || def.Name == "list_files" || def.Name == "grep_search" {
			access = llmcontracts.RuntimeToolAccessRead
		}
		rt.Definitions = append(rt.Definitions, llmcontracts.RuntimeToolDefinition{Name: def.Name, Description: def.Description + " Work in the current page's draft bundle. Apply completed edits with apply_page_changes.", Schema: def.Schema, Access: access})
	}
	names["apply_page_changes"] = true
	rt.Definitions = append(rt.Definitions, llmcontracts.RuntimeToolDefinition{Name: "apply_page_changes", Description: "Validate and apply the current page draft. Build and test first if needed. Preserves URL, data and chat; failed backend startup restores the previous version. No separate task or Settings approval is needed.", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Access: llmcontracts.RuntimeToolAccessWrite})
	rt.Filter = func(name string) (bool, bool) { return names[name], names[name] }
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
		mu.Lock()
		defer mu.Unlock()
		if dir == "" {
			var err error
			dir, baseHash, err = h.pagePlugins.EditWorkspace(ctx, binding.Page.InstanceID)
			if err != nil {
				return fail(err)
			}
		}
		if name == "apply_page_changes" {
			result, err := h.pagePlugins.ApplyEdit(ctx, binding.Page.InstanceID, binding.Page.ProjectID, baseHash, dir, principal)
			if err != nil {
				if strings.Contains(err.Error(), "previous package restored") && h.broadcaster != nil {
					h.broadcaster.Publish(events.TaskEvent{Type: events.PluginPagesChanged, ProjectID: binding.Page.ProjectID, TaskID: binding.Page.InstanceID})
				}
				return fail(err)
			}
			baseHash = result.Instance.ContentHash
			if h.broadcaster != nil {
				h.broadcaster.Publish(events.TaskEvent{Type: events.PluginPagesChanged, ProjectID: binding.Page.ProjectID, TaskID: binding.Page.InstanceID})
			}
			return "Page updated successfully. The page URL, data and conversation are unchanged.", true, false, nil
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
				return fail(fmt.Errorf("use a path relative to the page draft"))
			}
			if err := memory.AssertPathWithin(dir, filepath.Join(dir, path)); err != nil {
				return fail(fmt.Errorf("page draft path: %w", err))
			}
		}
		output, err := agenttools.Execute(ctx, dir, name, input)
		if err != nil {
			return fail(err)
		}
		return output, true, false, nil
	}
	return rt, cleanup, dir, nil
}
