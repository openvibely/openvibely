package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/util"
	"github.com/openvibely/openvibely/pkg/knowledgecache"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

var storeLocks sync.Map

// RuntimeTools keeps all operations in one project's shared managed-memory directory.
// Reads recheck the index at execution time, including memories saved during this turn.
func RuntimeTools(repoPath string, read, write bool) *llmcontracts.RuntimeTools {
	if strings.TrimSpace(repoPath) == "" {
		return nil
	}
	definitions := []llmcontracts.RuntimeToolDefinition{}
	if read {
		definitions = append(definitions, llmcontracts.RuntimeToolDefinition{Name: "memory_view", Description: "Read a permitted memory from the current project's memory index.", Schema: memoryViewSchema, Access: llmcontracts.RuntimeToolAccessRead})
	}
	if write {
		definitions = append(definitions,
			llmcontracts.RuntimeToolDefinition{Name: "memory_save", Description: "Save or correct a durable project memory and its concise index summary. Reuse existing topics; do not save secrets, routine chatter, or duplicates.", Schema: &toolschema.Schema{Type: "object", Properties: map[string]*toolschema.Schema{"handle": {Type: "string"}, "content": {Type: "string"}, "summary": {Type: "string"}}, Required: []string{"handle", "content", "summary"}, AdditionalProperties: toolschema.Pointer(false)}, Access: llmcontracts.RuntimeToolAccessWrite},
			llmcontracts.RuntimeToolDefinition{Name: "memory_delete", Description: "Delete an obsolete or incorrect memory and remove it from the current project's index.", Schema: memoryViewSchema, Access: llmcontracts.RuntimeToolAccessWrite})
	}
	return &llmcontracts.RuntimeTools{Definitions: definitions, Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if name != "memory_view" && name != "memory_save" && name != "memory_delete" {
			return "", false, false, nil
		}
		if err := ctx.Err(); err != nil {
			return "", true, true, err
		}
		if name == "memory_view" && !read || name != "memory_view" && !write {
			return "Memory operation not permitted", true, true, nil
		}
		result, err := executeMemory(ctx, repoPath, name, input)
		if err != nil {
			return err.Error(), true, true, nil
		}
		return result, true, false, nil
	}, Filter: func(name string) (bool, bool) {
		switch name {
		case "memory_view":
			return read, true
		case "memory_save", "memory_delete":
			return write, true
		}
		return false, false
	}}
}

func executeMemory(ctx context.Context, repoPath, name string, input json.RawMessage) (string, error) {
	dir, err := SharedRepoMemoryDir(repoPath)
	if err != nil {
		return "", err
	}
	lock, _ := storeLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resolver, err := NewPathResolver("", "")
	if err != nil {
		return "", err
	}
	if err := resolver.SetProjectDirOverride("runtime", dir); err != nil {
		return "", err
	}
	var params struct {
		Handle  string `json:"handle"`
		Content string `json:"content"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", err
	}
	handle := NormalizeMemoryHandle(params.Handle)
	if handle == "" || !strings.HasSuffix(handle, ".md") || strings.EqualFold(handle, IndexFileName) || strings.EqualFold(handle, LegacyIndexFileName) {
		return "", fmt.Errorf("invalid memory handle")
	}
	path, err := resolver.ResolveSafe("runtime", handle)
	if err != nil {
		return "", err
	}
	indexPath, err := resolver.ResolveSafe("runtime", IndexFileName)
	if err != nil {
		return "", err
	}
	index, err := os.ReadFile(indexPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if name == "memory_view" {
		authorized := map[string]SelectedMemory{}
		for h := range IndexedMemoryHandles(string(index)) {
			authorized[h] = SelectedMemory{File: h}
		}
		return resolveMemoryView(repoPath, authorized, input)
	}
	if name == "memory_save" && (strings.TrimSpace(params.Content) == "" || strings.TrimSpace(params.Summary) == "") {
		return "", fmt.Errorf("content and summary are required")
	}
	entry := "- " + handle + " — " + strings.Join(strings.Fields(params.Summary), " ")
	// A successful save must be addressable through the same index parser used
	// by memory_view and subsequent updates/deletes. Validate before any writes.
	if name == "memory_save" && indexedMemoryLineHandle(entry) != handle {
		return "", fmt.Errorf("memory handle or summary cannot be represented in the index; use a simple markdown filename (for example user_preferences.md) and a plain-text summary")
	}
	var lines []string
	for _, line := range strings.Split(string(index), "\n") {
		if indexedMemoryLineHandle(line) != handle {
			lines = append(lines, line)
		}
	}
	if len(index) == 0 {
		lines = []string{"# Project Memory", ""}
	}
	if name == "memory_save" {
		lines = append(lines, entry)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	old, oldErr := os.ReadFile(path)
	if oldErr != nil && !os.IsNotExist(oldErr) {
		return "", oldErr
	}
	if name == "memory_save" {
		err = util.WriteFileAtomic(path, []byte(params.Content), 0644)
	} else {
		err = os.Remove(path)
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err != nil {
		return "", err
	}
	if err := util.WriteFileAtomic(indexPath, []byte(strings.TrimSpace(strings.Join(lines, "\n"))+"\n"), 0644); err != nil {
		if oldErr == nil {
			_ = util.WriteFileAtomic(path, old, 0644)
		} else {
			_ = os.Remove(path)
		}
		return "", err
	}
	knowledgecache.Invalidate(dir)
	return "Memory updated: " + handle, nil
}
