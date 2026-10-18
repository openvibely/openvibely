package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

// SelectedMemory identifies one memory entry selected for the current model turn.
type SelectedMemory struct {
	File    string
	Topic   string
	Summary string
	Snippet string
}

// SelectedMemoryRuntimeTools returns memory_view scoped to the already selected
// memory handles for this turn. It intentionally omits list/search tools so the
// task cannot discover or load memories the lifecycle router did not select.
func SelectedMemoryRuntimeTools(repoPath string, memories []SelectedMemory) *llmcontracts.RuntimeTools {
	authorized := authorizedMemoryMap(memories)
	if strings.TrimSpace(repoPath) == "" || len(authorized) == 0 {
		return nil
	}

	return &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{
				Name:        "memory_view",
				Description: "Load an authorized managed memory selected for this turn. Use only handles listed in <selected_memories>, for example memory_view(\"provider_architecture.md\").",
				Schema:      memoryViewSchema,
				Access:      llmcontracts.RuntimeToolAccessRead,
			},
		},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if !isMemoryViewToolName(name) {
				return "", false, false, nil
			}
			body, err := resolveMemoryView(repoPath, authorized, input)
			if err != nil {
				return err.Error(), true, true, nil
			}
			return body, true, false, nil
		},
		Filter: func(name string) (bool, bool) {
			if isMemoryViewToolName(name) {
				return true, true
			}
			return false, false
		},
	}
}

func isMemoryViewToolName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "memory_view")
}

func resolveMemoryView(repoPath string, authorized map[string]SelectedMemory, input json.RawMessage) (string, error) {
	var params struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("memory_view: invalid input: %w", err)
	}
	handle := cleanMemoryHandle(params.Handle)
	if handle == "" {
		return "", fmt.Errorf("memory_view: handle is required")
	}
	if _, ok := authorized[handle]; !ok {
		return "", fmt.Errorf("memory_view: handle %q is not in this turn's authorized memory index", handle)
	}
	memoryDir, err := SharedRepoMemoryDir(repoPath)
	if err != nil {
		return "", fmt.Errorf("memory_view: %w", err)
	}
	resolver, err := NewPathResolver("", "")
	if err != nil {
		return "", fmt.Errorf("memory_view: %w", err)
	}
	const projectID = "memory-view"
	if err := resolver.SetProjectDirOverride(projectID, memoryDir); err != nil {
		return "", fmt.Errorf("memory_view: %w", err)
	}
	abs, err := resolver.ResolveSafe(projectID, handle)
	if err != nil {
		return "", fmt.Errorf("memory_view: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("memory_view: read %q: %w", handle, err)
	}
	view := memoryViewResponse{
		Handle: handle,
		File:   handle,
		Body:   string(data),
	}
	encoded, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory_view: encode %q: %w", handle, err)
	}
	return string(encoded), nil
}

type memoryViewResponse struct {
	Handle string `json:"handle"`
	File   string `json:"file"`
	Body   string `json:"body"`
}

// IndexedMemoryHandles extracts safe memory file handles listed by the compact
// MEMORIES.md index. It intentionally recognizes only markdown file references,
// which keeps route-selected memory authorization tied to explicit index entries.
func IndexedMemoryHandles(index string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(index, "\n") {
		candidate := indexedMemoryLineHandle(line)
		if candidate == "" {
			continue
		}
		handle := cleanMemoryHandle(candidate)
		if handle == "" {
			continue
		}
		out[handle] = struct{}{}
	}
	return out
}

func indexedMemoryLineHandle(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimLeft(line, "-*+"))
	if dot := strings.Index(line, ". "); dot > 0 {
		if allDigits(line[:dot]) {
			line = strings.TrimSpace(line[dot+2:])
		}
	}
	if target := markdownLinkTarget(line); target != "" {
		return target
	}
	first := line
	if fields := strings.Fields(line); len(fields) > 0 {
		first = fields[0]
	}
	return markdownHandleCandidate(first)
}

func markdownLinkTarget(line string) string {
	open := strings.Index(line, "](")
	if open < 0 {
		return ""
	}
	start := open + len("](")
	end := strings.Index(line[start:], ")")
	if end < 0 {
		return ""
	}
	return markdownHandleCandidate(line[start : start+end])
}

func markdownHandleCandidate(token string) string {
	lower := strings.ToLower(token)
	idx := strings.Index(lower, ".md")
	if idx < 0 {
		return ""
	}
	end := idx + len(".md")
	start := 0
	for i := end - 1; i >= 0; i-- {
		ch := token[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' || ch == '.' || ch == '/' || ch == '\\' {
			start = i
			continue
		}
		start = i + 1
		break
	}
	return token[start:end]
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func authorizedMemoryMap(memories []SelectedMemory) map[string]SelectedMemory {
	out := map[string]SelectedMemory{}
	for _, entry := range normalizeSelectedMemories(memories) {
		handle := memoryHandle(entry)
		if handle == "" {
			continue
		}
		out[handle] = entry
	}
	return out
}

func normalizeSelectedMemories(memories []SelectedMemory) []SelectedMemory {
	if len(memories) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]SelectedMemory, 0, len(memories))
	for _, entry := range memories {
		entry.File = cleanMemoryHandle(entry.File)
		entry.Topic = strings.TrimSpace(entry.Topic)
		entry.Summary = strings.TrimSpace(entry.Summary)
		entry.Snippet = strings.TrimSpace(entry.Snippet)
		handle := memoryHandle(entry)
		if handle == "" {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return memoryHandle(out[i]) < memoryHandle(out[j]) })
	return out
}

func memoryHandle(entry SelectedMemory) string {
	return cleanMemoryHandle(entry.File)
}

// NormalizeMemoryHandle returns the canonical safe form of a memory handle or
// an empty string when the handle is absolute, traversing, or otherwise unsafe.
func NormalizeMemoryHandle(handle string) string {
	return cleanMemoryHandle(handle)
}

func cleanMemoryHandle(handle string) string {
	handle = strings.TrimSpace(strings.ReplaceAll(handle, "\\", "/"))
	if handle == "" || strings.HasPrefix(handle, "/") || strings.HasPrefix(handle, "./") || strings.Contains(handle, ":") {
		return ""
	}
	if handle == "." || handle == ".." || strings.HasPrefix(handle, "../") || strings.Contains(handle, "/../") || strings.HasSuffix(handle, "/..") {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(handle))
}

var memoryViewSchema = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"handle": {
			Type:        "string",
			Description: "Selected memory handle/file, e.g. provider_architecture.md",
		},
	},
	Required:             []string{"handle"},
	AdditionalProperties: toolschema.Pointer(false),
}
