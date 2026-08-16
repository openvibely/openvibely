package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
)

type scopedFilesPermissionSet struct {
	read   bool
	write  bool
	delete bool
}

type scopedFilesScope struct {
	projectID   string
	directory   string
	absoluteDir string
	perms       scopedFilesPermissionSet
}

type scopedFilesToolSession struct {
	scopes []scopedFilesScope

	mu      sync.Mutex
	touched map[string]struct{}
}

func buildScopedFilesRuntimeTools(ctx context.Context, projectID string, repoPath string, cfg models.AgentToolConfig) (string, *llmcontracts.RuntimeTools, error) {
	scopes, err := buildScopedFileScopes(projectID, repoPath, cfg.ScopedFiles)
	if err != nil {
		return "", nil, err
	}
	if len(scopes) == 0 {
		return "", nil, nil
	}
	for _, scope := range scopes {
		if err := os.MkdirAll(scope.absoluteDir, 0o755); err != nil {
			return "", nil, fmt.Errorf("scoped files: create scope %s: %w", scope.directory, err)
		}
	}
	session := &scopedFilesToolSession{scopes: scopes, touched: map[string]struct{}{}}
	return scopes[0].absoluteDir, session.runtimeTools(cfg.SkipDefaultTools), nil
}

func buildScopedFileScopes(projectID string, repoPath string, configs []models.ScopedFilesConfig) ([]scopedFilesScope, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil, fmt.Errorf("scoped files: project %s has no local repo_path", projectID)
	}
	repoRoot, err := filepath.Abs(filepath.Clean(repoPath))
	if err != nil {
		return nil, fmt.Errorf("scoped files: resolve repo path: %w", err)
	}
	if err := assertExistingDirectory(repoRoot); err != nil {
		return nil, err
	}

	out := make([]scopedFilesScope, 0, len(configs))
	for _, cfg := range configs {
		dir := filepath.ToSlash(filepath.Clean(strings.TrimSpace(cfg.Directory)))
		if dir == "" || dir == "." {
			return nil, fmt.Errorf("scoped files: directory is required")
		}
		if filepath.IsAbs(dir) || strings.HasPrefix(dir, "../") || dir == ".." {
			return nil, fmt.Errorf("scoped files: directory must be project-relative: %s", cfg.Directory)
		}
		abs := filepath.Join(repoRoot, filepath.FromSlash(dir))
		if err := memory.AssertPathWithin(repoRoot, abs); err != nil {
			return nil, fmt.Errorf("scoped files: directory escapes repo: %w", err)
		}
		out = append(out, scopedFilesScope{
			projectID:   projectID,
			directory:   dir,
			absoluteDir: abs,
			perms:       normalizeScopedFilesPermissions(cfg.Permissions),
		})
	}
	return out, nil
}

func assertExistingDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("scoped files: stat repo path %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("scoped files: repo path is not a directory: %s", path)
	}
	return nil
}

func normalizeScopedFilesPermissions(in []string) scopedFilesPermissionSet {
	var p scopedFilesPermissionSet
	for _, raw := range in {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "read":
			p.read = true
		case "write":
			p.write = true
		case "delete":
			p.delete = true
		case "read_write":
			p.read = true
			p.write = true
		case "read_write_delete", "admin":
			p.read = true
			p.write = true
			p.delete = true
		}
	}
	return p
}

func (s *scopedFilesToolSession) runtimeTools(skipDefaultTools bool) *llmcontracts.RuntimeTools {
	return &llmcontracts.RuntimeTools{
		Definitions:      scopedFilesToolDefinitions(),
		Executor:         s.execute,
		Metadata:         s,
		SkipDefaultTools: skipDefaultTools,
		Filter: func(name string) (bool, bool) {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "list_files", "read_file", "write_file", "edit_file", "grep_search", "delete_file":
				return true, true
			default:
				return false, true
			}
		},
	}
}

func scopedFilesToolDefinitions() []llmcontracts.RuntimeToolDefinition {
	const scopePrefixDoc = " When multiple scopes are configured, prefix the path with '<scope_label>/' to address a specific scope explicitly; without a prefix the first matching scope is used."
	return []llmcontracts.RuntimeToolDefinition{
		{Name: "list_files", Description: "List files inside the configured scoped directory. Paths are relative to that directory." + scopePrefixDoc, Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"recursive":{"type":"boolean"},"pattern":{"type":"string"}},"additionalProperties":false}`)},
		{Name: "read_file", Description: "Read a file from the configured scoped directory." + scopePrefixDoc, Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["file_path"],"additionalProperties":false}`)},
		{Name: "write_file", Description: "Write a file inside the configured scoped directory. Overwrites the file." + scopePrefixDoc, Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}},"required":["file_path","content"],"additionalProperties":false}`)},
		{Name: "edit_file", Description: "Edit a file inside the configured scoped directory by replacing old_string with new_string." + scopePrefixDoc, Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["file_path","old_string","new_string"],"additionalProperties":false}`)},
		{Name: "grep_search", Description: "Search files inside the configured scoped directory with a regular expression." + scopePrefixDoc, Parameters: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"include":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`)},
		{Name: "delete_file", Description: "Delete a file inside the configured scoped directory." + scopePrefixDoc, Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"],"additionalProperties":false}`)},
	}
}

func (s *scopedFilesToolSession) execute(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "list_files":
		out, err := s.listFiles(input)
		return out, true, err != nil, err
	case "read_file":
		out, err := s.readFile(input)
		return out, true, err != nil, err
	case "write_file":
		out, err := s.writeFile(input)
		return out, true, err != nil, err
	case "edit_file":
		out, err := s.editFile(input)
		return out, true, err != nil, err
	case "grep_search":
		out, err := s.grepSearch(ctx, input)
		return out, true, err != nil, err
	case "delete_file":
		out, err := s.deleteFile(input)
		return out, true, err != nil, err
	default:
		return "tool not available for scoped files", true, true, nil
	}
}

// scopedPath resolves a model-supplied relative path against the configured
// scopes. When the session has more than one configured scope, the path can
// optionally be prefixed with "<scope_label>/" to address a specific scope
// explicitly. Without a prefix, the first scope that permits the operation wins,
// matching the legacy single-scope behavior.
func (s *scopedFilesToolSession) scopedPath(rel string, allowRoot bool, need scopedFilesPermissionSet) (string, string, *scopedFilesScope, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		if !allowRoot {
			return "", "", nil, fmt.Errorf("scoped files: file path is required")
		}
		scope, err := s.scopeForRoot(need)
		if err != nil {
			return "", "", nil, err
		}
		return scope.absoluteDir, ".", scope, nil
	}
	if filepath.IsAbs(rel) {
		return "", "", nil, fmt.Errorf("scoped files: absolute paths not allowed: %s", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", nil, fmt.Errorf("scoped files: path escapes scope: %s", rel)
	}

	// Explicit scope routing: "<scope_directory>/<path>" picks a specific
	// scope. Match the longest configured scope directory prefix so nested
	// scopes such as "configs/secrets" win over parent scopes such as
	// "configs" before falling back to first permitted scope behavior.
	var explicitScope *scopedFilesScope
	var explicitRest string
	for i := range s.scopes {
		scope := &s.scopes[i]
		rest, ok := explicitScopeRest(clean, scope.directory, allowRoot)
		if !ok {
			continue
		}
		if explicitScope != nil && len(scope.directory) <= len(explicitScope.directory) {
			continue
		}
		explicitScope = scope
		explicitRest = rest
	}
	if explicitScope != nil {
		if !explicitScope.hasPermissions(need) {
			return "", "", nil, fmt.Errorf("scoped files: scope %q does not permit %s", explicitScope.directory, rel)
		}
		abs := filepath.Join(explicitScope.absoluteDir, filepath.FromSlash(explicitRest))
		if err := memory.AssertPathWithin(explicitScope.absoluteDir, abs); err != nil {
			return "", "", nil, fmt.Errorf("scoped files: path escapes scope %q", explicitScope.directory)
		}
		return abs, explicitRest, explicitScope, nil
	}

	for i := range s.scopes {
		scope := &s.scopes[i]
		if !scope.hasPermissions(need) {
			continue
		}
		abs := filepath.Join(scope.absoluteDir, filepath.FromSlash(clean))
		if err := memory.AssertPathWithin(scope.absoluteDir, abs); err != nil {
			continue
		}
		return abs, clean, scope, nil
	}
	return "", "", nil, fmt.Errorf("scoped files: no configured scope permits %s", rel)
}

func explicitScopeRest(clean, scopeDirectory string, allowRoot bool) (string, bool) {
	if clean == scopeDirectory {
		if !allowRoot {
			return "", false
		}
		return ".", true
	}
	prefix := scopeDirectory + "/"
	if !strings.HasPrefix(clean, prefix) {
		return "", false
	}
	return strings.TrimPrefix(clean, prefix), true
}

func (s *scopedFilesToolSession) scopeForRoot(need scopedFilesPermissionSet) (*scopedFilesScope, error) {
	for i := range s.scopes {
		if s.scopes[i].hasPermissions(need) {
			return &s.scopes[i], nil
		}
	}
	return nil, fmt.Errorf("scoped files: no configured scope permits this operation")
}

func (s *scopedFilesScope) hasPermissions(need scopedFilesPermissionSet) bool {
	if need.read && !s.perms.read {
		return false
	}
	if need.write && !s.perms.write {
		return false
	}
	if need.delete && !s.perms.delete {
		return false
	}
	return true
}

func readPerm() scopedFilesPermissionSet   { return scopedFilesPermissionSet{read: true} }
func writePerm() scopedFilesPermissionSet  { return scopedFilesPermissionSet{write: true} }
func deletePerm() scopedFilesPermissionSet { return scopedFilesPermissionSet{delete: true} }

func (s *scopedFilesToolSession) listFiles(input json.RawMessage) (string, error) {
	var params struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
		Pattern   string `json:"pattern"`
	}
	if len(input) > 0 {
		_ = json.Unmarshal(input, &params)
	}
	abs, rel, _, err := s.scopedPath(params.Path, true, readPerm())
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return rel, nil
	}
	var out []string
	if params.Recursive {
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == abs {
				return nil
			}
			entryRel, err := filepath.Rel(abs, path)
			if err != nil {
				return err
			}
			fullRel := filepath.ToSlash(filepath.Join(rel, entryRel))
			fullRel = strings.TrimPrefix(fullRel, "./")
			if d.IsDir() {
				fullRel += "/"
			}
			if params.Pattern == "" || matchGlob(params.Pattern, filepath.Base(fullRel)) {
				out = append(out, fullRel)
			}
			if len(out) >= 500 {
				return filepath.SkipAll
			}
			return nil
		})
	} else {
		entries, readErr := os.ReadDir(abs)
		err = readErr
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			if params.Pattern == "" || matchGlob(params.Pattern, strings.TrimSuffix(name, "/")) {
				out = append(out, name)
			}
		}
	}
	if err != nil {
		return "", err
	}
	sort.Strings(out)
	return strings.Join(out, "\n"), nil
}

func (s *scopedFilesToolSession) readFile(input json.RawMessage) (string, error) {
	var params struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", err
	}
	abs, _, _, err := s.scopedPath(params.FilePath, false, readPerm())
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if params.Limit <= 0 {
		params.Limit = 5000
	}
	if params.Limit > 10000 {
		params.Limit = 10000
	}
	lines := strings.Split(string(data), "\n")
	if params.Offset > 0 {
		if params.Offset >= len(lines) {
			return fmt.Sprintf("(file has %d lines, offset %d is past end)", len(lines), params.Offset), nil
		}
		lines = lines[params.Offset:]
	}
	truncated := false
	if len(lines) > params.Limit {
		lines = lines[:params.Limit]
		truncated = true
	}
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d\t%s\n", params.Offset+i+1, line)
	}
	if truncated {
		fmt.Fprintf(&b, "\n... (truncated, showing %d lines from offset %d)", params.Limit, params.Offset)
	}
	return b.String(), nil
}

func (s *scopedFilesToolSession) writeFile(input json.RawMessage) (string, error) {
	var params struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", err
	}
	abs, rel, _, err := s.scopedPath(params.FilePath, false, writePerm())
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := atomicWriteService(abs, []byte(memory.Redact(params.Content))); err != nil {
		return "", err
	}
	s.recordTouched(rel)
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(params.Content), rel), nil
}

func (s *scopedFilesToolSession) editFile(input json.RawMessage) (string, error) {
	var params struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", err
	}
	if params.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}
	abs, rel, _, err := s.scopedPath(params.FilePath, false, writePerm())
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)
	if !strings.Contains(content, params.OldString) {
		return "", fmt.Errorf("old_string not found in %s", rel)
	}
	count := strings.Count(content, params.OldString)
	if count > 1 && !params.ReplaceAll {
		return "", fmt.Errorf("old_string found %d times in %s; set replace_all=true or provide a more specific string", count, rel)
	}
	var next string
	if params.ReplaceAll {
		next = strings.ReplaceAll(content, params.OldString, params.NewString)
	} else {
		next = strings.Replace(content, params.OldString, params.NewString, 1)
	}
	if err := atomicWriteService(abs, []byte(memory.Redact(next))); err != nil {
		return "", err
	}
	s.recordTouched(rel)
	return fmt.Sprintf("Successfully edited %s", rel), nil
}

func (s *scopedFilesToolSession) grepSearch(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", err
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", err
	}
	abs, baseRel, scope, err := s.scopedPath(params.Path, true, readPerm())
	if err != nil {
		return "", err
	}
	var matches []string
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			return nil
		}
		if err := memory.AssertPathWithin(scope.absoluteDir, path); err != nil {
			return nil
		}
		if params.Include != "" && !matchGlob(params.Include, filepath.Base(path)) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(abs, path)
		fullRel := filepath.ToSlash(filepath.Join(baseRel, rel))
		fullRel = strings.TrimPrefix(fullRel, "./")
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", fullRel, i+1, line))
				if len(matches) >= 200 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return strings.Join(matches, "\n"), nil
}

func (s *scopedFilesToolSession) deleteFile(input json.RawMessage) (string, error) {
	var params struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", err
	}
	abs, rel, _, err := s.scopedPath(params.FilePath, false, deletePerm())
	if err != nil {
		return "", err
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	s.recordTouched(rel)
	return "Deleted " + rel, nil
}

func (s *scopedFilesToolSession) recordTouched(rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched[rel] = struct{}{}
}

func matchGlob(pattern, name string) bool {
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}

func atomicWriteService(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpName)
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	return os.Rename(tmpName, path)
}
