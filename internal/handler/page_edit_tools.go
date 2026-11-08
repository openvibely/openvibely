package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/pageintegration"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

// pageEditTools uses persistent instance-owned files and publishes successful changes automatically.
func (h *Handler) pageEditTools(ctx context.Context, binding pageintegration.Conversation, principal string) (*llmcontracts.RuntimeTools, string, error) {
	var mu sync.Mutex
	dir, _, err := h.pagePlugins.EditWorkspace(ctx, binding.Page.InstanceID)
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
		rt.Definitions = append(rt.Definitions, llmcontracts.RuntimeToolDefinition{Name: def.Name, Description: def.Description + " Work in the current page files. Successful file changes automatically update the page. Files persist across replies; failed updates retain your edits. Build backend code with shell when needed.", Schema: def.Schema, Access: access})
	}
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
		mu.Lock()
		defer mu.Unlock()
		// Resolve the current revision for every tool call: a task may have
		// updated the same source tree since this conversation began.
		_, baseHash, err := h.pagePlugins.EditWorkspace(ctx, binding.Page.InstanceID)
		if err != nil {
			return fail(err)
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
		if name == "shell" || name == "process" {
			// Shell returns its exit status in a terminal trailer, including
			// failures and sessions still running. Never publish partial builds.
			index := strings.LastIndex(output, "\n[process]\n")
			if index < 0 {
				return output, true, true, nil
			}
			status := output[index:]
			if strings.Contains(status, "status: running\n") {
				return output, true, false, nil
			}
			if !strings.Contains(status, "exit_code: 0\n") {
				return output + "\nEdited files retained; live page unchanged.", true, true, nil
			}
		}
		after, hashErr := pageplugins.HashTree(os.DirFS(dir))
		if name != "read_file" && name != "list_files" && name != "grep_search" && (baseHash != after || hashErr != nil) {
			_, applyErr := h.pagePlugins.ApplyEdit(ctx, binding.Page.InstanceID, binding.Page.ProjectID, baseHash, dir, principal)
			if applyErr != nil {
				if strings.Contains(applyErr.Error(), "previous package restored") && h.broadcaster != nil {
					h.broadcaster.Publish(events.TaskEvent{Type: events.PluginPagesChanged, ProjectID: binding.Page.ProjectID, TaskID: binding.Page.InstanceID})
				}
				return fail(fmt.Errorf("files saved; live page update failed (edits retained): %w", applyErr))
			}
			if h.broadcaster != nil {
				h.broadcaster.Publish(events.TaskEvent{Type: events.PluginPagesChanged, ProjectID: binding.Page.ProjectID, TaskID: binding.Page.InstanceID})
			}
			output += "\nPage updated."
		}
		return output, true, false, nil
	}
	return rt, dir, nil
}
