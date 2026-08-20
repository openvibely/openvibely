package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// BashPolicy configures provider-specific bash timeout behavior.
type BashPolicy struct {
	DefaultTimeoutSeconds int
	MaxTimeoutSeconds     int
}

// Execute runs a local built-in coding tool and returns the output string.
// It resolves relative paths against workDir.
func Execute(ctx context.Context, workDir, name string, input json.RawMessage, bashPolicy BashPolicy) (string, error) {
	switch name {
	case "read_file":
		return execReadFile(workDir, input)
	case "write_file":
		return execWriteFile(workDir, input)
	case "edit_file":
		return execEditFile(workDir, input)
	case "bash":
		return execBash(ctx, workDir, input, bashPolicy)
	case "list_files":
		return execListFiles(workDir, input)
	case "grep_search":
		return execGrepSearch(ctx, workDir, input)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// NormalizeBashTimeout applies a provider-specific bash timeout policy.
func NormalizeBashTimeout(seconds int, policy BashPolicy) int {
	if seconds <= 0 {
		return policy.DefaultTimeoutSeconds
	}
	if policy.MaxTimeoutSeconds > 0 && seconds > policy.MaxTimeoutSeconds {
		return policy.MaxTimeoutSeconds
	}
	return seconds
}

func resolvePath(workDir, filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(workDir, filePath)
}

func execReadFile(workDir string, input json.RawMessage) (string, error) {
	var params struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if params.Limit <= 0 {
		params.Limit = 5000
	}
	if params.Limit > 10000 {
		params.Limit = 10000
	}

	path := resolvePath(workDir, params.FilePath)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", params.FilePath, err)
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

	var sb strings.Builder
	startLine := params.Offset + 1
	for i, line := range lines {
		fmt.Fprintf(&sb, "%d\t%s\n", startLine+i, line)
	}
	if truncated {
		fmt.Fprintf(&sb, "\n... (truncated, showing %d of %d lines from offset %d)", params.Limit, len(strings.Split(string(data), "\n")), params.Offset)
	}

	return sb.String(), nil
}

func execWriteFile(workDir string, input json.RawMessage) (string, error) {
	var params struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	path := resolvePath(workDir, params.FilePath)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create directories: %w", err)
	}

	if err := os.WriteFile(path, []byte(params.Content), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", params.FilePath, err)
	}

	lines := strings.Count(params.Content, "\n") + 1
	return fmt.Sprintf("Successfully wrote %d bytes (%d lines) to %s", len(params.Content), lines, params.FilePath), nil
}

func execEditFile(workDir string, input json.RawMessage) (string, error) {
	var params struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if params.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	path := resolvePath(workDir, params.FilePath)

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", params.FilePath, err)
	}

	content := string(data)

	var newContent string
	if strings.Contains(content, params.OldString) {
		count := strings.Count(content, params.OldString)
		if count > 1 && !params.ReplaceAll {
			return "", fmt.Errorf("old_string found %d times in %s — set replace_all=true to replace all, or provide a more specific string", count, params.FilePath)
		}
		if params.ReplaceAll {
			newContent = strings.ReplaceAll(content, params.OldString, params.NewString)
		} else {
			newContent = strings.Replace(content, params.OldString, params.NewString, 1)
		}
		if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
			return "", fmt.Errorf("write %s: %w", params.FilePath, err)
		}
		if params.ReplaceAll {
			return fmt.Sprintf("Replaced %d occurrences in %s", count, params.FilePath), nil
		}
		return fmt.Sprintf("Successfully edited %s (1 replacement)", params.FilePath), nil
	}

	newContent, count, ok := applyEditWithLineMatching(content, params.OldString, params.NewString, params.ReplaceAll)
	if !ok {
		return "", fmt.Errorf("old_string not found in %s", params.FilePath)
	}
	if count > 1 && !params.ReplaceAll {
		return "", fmt.Errorf("old_string found %d times in %s — set replace_all=true to replace all, or provide a more specific string", count, params.FilePath)
	}

	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", params.FilePath, err)
	}

	if params.ReplaceAll {
		return fmt.Sprintf("Replaced %d occurrences in %s", count, params.FilePath), nil
	}
	return fmt.Sprintf("Successfully edited %s (1 replacement)", params.FilePath), nil
}

func applyEditWithLineMatching(content, oldString, newString string, replaceAll bool) (string, int, bool) {
	contentLines, starts := splitLinesWithStarts(content)
	targetLines := splitLinesForMatch(oldString)
	if len(targetLines) == 0 || len(targetLines) > len(contentLines) {
		return "", 0, false
	}

	comparators := []func(string, string) bool{
		func(a, b string) bool { return a == b },
		func(a, b string) bool { return strings.TrimRight(a, " \t\r") == strings.TrimRight(b, " \t\r") },
		func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) },
		func(a, b string) bool { return normalizeLineForMatch(a) == normalizeLineForMatch(b) },
	}

	var matches []int
	for _, cmp := range comparators {
		matches = findLineBlockMatches(contentLines, targetLines, cmp)
		if len(matches) > 0 {
			break
		}
	}
	if len(matches) == 0 {
		return "", 0, false
	}
	if len(matches) > 1 && !replaceAll {
		return "", len(matches), true
	}

	replaced := replaceLineBlockMatches(content, starts, matches, len(targetLines), newString, replaceAll)
	if replaceAll {
		return replaced, len(matches), true
	}
	return replaced, 1, true
}

func splitLinesWithStarts(content string) ([]string, []int) {
	lines := strings.Split(content, "\n")
	starts := make([]int, len(lines))
	idx := 0
	for i := range lines {
		starts[i] = idx
		idx += len(lines[i])
		if i < len(lines)-1 {
			idx++
		}
	}
	return lines, starts
}

func splitLinesForMatch(s string) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func findLineBlockMatches(contentLines, targetLines []string, cmp func(string, string) bool) []int {
	if len(targetLines) == 0 || len(targetLines) > len(contentLines) {
		return nil
	}
	var matches []int
	for i := 0; i <= len(contentLines)-len(targetLines); i++ {
		ok := true
		for j := range targetLines {
			if !cmp(contentLines[i+j], targetLines[j]) {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, i)
		}
	}
	return matches
}

func replaceLineBlockMatches(content string, starts []int, matches []int, targetLen int, newString string, replaceAll bool) string {
	if len(matches) == 0 {
		return content
	}
	if !replaceAll {
		matches = matches[:1]
	}
	var sb strings.Builder
	last := 0
	for _, startLine := range matches {
		startByte := starts[startLine]
		endLine := startLine + targetLen
		endByte := len(content)
		if endLine < len(starts) {
			endByte = starts[endLine]
		}
		if startByte < last {
			continue
		}
		sb.WriteString(content[last:startByte])
		sb.WriteString(newString)
		last = endByte
	}
	sb.WriteString(content[last:])
	return sb.String()
}

func normalizeLineForMatch(s string) string {
	var b strings.Builder
	for _, c := range strings.TrimSpace(s) {
		switch c {
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			b.WriteRune('-')
		case '\u2018', '\u2019', '\u201A', '\u201B':
			b.WriteRune('\'')
		case '\u201C', '\u201D', '\u201E', '\u201F':
			b.WriteRune('"')
		case '\u00A0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000':
			b.WriteRune(' ')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func execBash(ctx context.Context, workDir string, input json.RawMessage, bashPolicy BashPolicy) (string, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if params.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	params.Timeout = NormalizeBashTimeout(params.Timeout, bashPolicy)

	timeout := time.Duration(params.Timeout) * time.Second
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", params.Command)
	cmd.Dir = workDir

	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = filtered

	output, err := cmd.CombinedOutput()
	result := string(output)

	const maxOutput = 50000
	if len(result) > maxOutput {
		result = result[:maxOutput] + "\n... (output truncated)"
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return result + fmt.Sprintf("\n(command timed out after %ds)", params.Timeout), fmt.Errorf("command timed out")
		}
		return result + fmt.Sprintf("\n(exit code: %v)", err), nil
	}

	return result, nil
}

func execListFiles(workDir string, input json.RawMessage) (string, error) {
	var params struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
		Pattern   string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}

	dir := workDir
	if params.Path != "" {
		dir = resolvePath(workDir, params.Path)
	}

	if params.Recursive {
		return listFilesRecursive(dir, params.Pattern)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read directory %s: %w", dir, err)
	}

	var sb strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if params.Pattern != "" {
			matched, _ := filepath.Match(params.Pattern, name)
			if !matched {
				continue
			}
		}
		if entry.IsDir() {
			sb.WriteString(name + "/\n")
		} else {
			sb.WriteString(name + "\n")
		}
	}

	return sb.String(), nil
}

func listFilesRecursive(root, pattern string) (string, error) {
	var sb strings.Builder
	count := 0
	maxEntries := 500
	maxDepth := 4

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if count >= maxEntries {
			return filepath.SkipAll
		}

		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		name := info.Name()
		if strings.HasPrefix(name, ".") && info.IsDir() {
			return filepath.SkipDir
		}
		if name == "node_modules" || name == "vendor" || name == "__pycache__" {
			return filepath.SkipDir
		}

		if pattern != "" {
			matched, _ := filepath.Match(pattern, name)
			if !matched && !info.IsDir() {
				return nil
			}
		}

		if info.IsDir() {
			sb.WriteString(rel + "/\n")
		} else {
			sb.WriteString(rel + "\n")
		}
		count++
		return nil
	})

	if count >= maxEntries {
		sb.WriteString(fmt.Sprintf("\n... (truncated at %d entries)", maxEntries))
	}

	return sb.String(), err
}

func execGrepSearch(ctx context.Context, workDir string, input json.RawMessage) (string, error) {
	var params struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if params.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	searchDir := workDir
	if params.Path != "" {
		searchDir = resolvePath(workDir, params.Path)
	}

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern %q: %w", params.Pattern, err)
	}

	var results strings.Builder
	count := 0
	maxResults := 100

	err = filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				name := info.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if count >= maxResults {
			return filepath.SkipAll
		}

		if params.Include != "" {
			matched, _ := filepath.Match(params.Include, info.Name())
			if !matched {
				return nil
			}
		}

		ext := strings.ToLower(filepath.Ext(path))
		binaryExts := map[string]bool{
			".exe": true, ".bin": true, ".so": true, ".dylib": true,
			".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
			".zip": true, ".tar": true, ".gz": true, ".pdf": true,
			".wasm": true, ".o": true, ".a": true,
		}
		if binaryExts[ext] {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(workDir, path)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if count >= maxResults {
				break
			}
			if re.MatchString(line) {
				fmt.Fprintf(&results, "%s:%d: %s\n", rel, i+1, truncateLine(line, 200))
				count++
			}
		}
		return nil
	})

	if count == 0 {
		return fmt.Sprintf("No matches found for pattern %q", params.Pattern), nil
	}
	if count >= maxResults {
		results.WriteString(fmt.Sprintf("\n... (showing first %d matches)", maxResults))
	}
	return results.String(), err
}

func truncateLine(line string, maxLen int) string {
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen] + "..."
}
