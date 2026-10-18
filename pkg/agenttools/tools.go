package agenttools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Bash timeouts are shared by every provider.
const (
	DefaultBashTimeoutSeconds = 120
	MaxBashTimeoutSeconds     = 600
)

// Execute runs a local built-in coding tool and returns the output string.
// It resolves relative paths against workDir.
func Execute(ctx context.Context, workDir, name string, input json.RawMessage) (string, error) {
	switch name {
	case "read_file":
		return execReadFile(workDir, input)
	case "write_file":
		return execWriteFile(workDir, input)
	case "edit_file":
		return execEditFile(workDir, input)
	case "bash", "Bash", "shell":
		return execBash(ctx, workDir, input)
	case "process":
		return execProcess(ctx, workDir, input)
	case "list_files":
		return execListFiles(workDir, input)
	case "grep_search":
		return execGrepSearch(ctx, workDir, input)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// NormalizeBashTimeout applies the shared bash timeout limits.
func NormalizeBashTimeout(seconds int) int {
	if seconds <= 0 {
		return DefaultBashTimeoutSeconds
	}
	if seconds > MaxBashTimeoutSeconds {
		return MaxBashTimeoutSeconds
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
	if err := ctx.Err(); err != nil {
		return "", err
	}

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
	const maxResults = 100
	reader := bufio.NewReaderSize(nil, 32*1024)
	lineBuffer := make([]byte, 0, 128)

	err = filepath.WalkDir(searchDir, func(path string, entry os.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= maxResults {
			return filepath.SkipAll
		}

		if params.Include != "" {
			matched, _ := filepath.Match(params.Include, entry.Name())
			if !matched {
				return nil
			}
		}

		ext := strings.ToLower(filepath.Ext(path))
		if _, skip := binaryExtensions[ext]; skip {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		reader.Reset(file)
		prefix, _ := reader.Peek(binaryCheckSize)
		if bytes.IndexByte(prefix, 0) >= 0 {
			_ = file.Close()
			return nil
		}

		rel, _ := filepath.Rel(workDir, path)
		lineBuffer = lineBuffer[:0]
		lineNumber := 0
		lastWasNewline := true // strings.Split treats an empty file as one empty line.
		var fileResults strings.Builder
		fileMatches := 0
		remainingMatches := maxResults - count
		for {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = file.Close()
				return ctxErr
			}

			fragment, readErr := reader.ReadSlice('\n')
			if bytes.IndexByte(fragment, 0) >= 0 {
				_ = file.Close()
				return nil
			}
			switch readErr {
			case nil:
				line := fragment[:len(fragment)-1]
				if len(lineBuffer) > 0 {
					lineBuffer = append(lineBuffer, line...)
					line = lineBuffer
				}
				lineNumber++
				if fileMatches < remainingMatches && re.Match(line) {
					fmt.Fprintf(&fileResults, "%s:%d: %s\n", rel, lineNumber, truncateLine(string(line), 200))
					fileMatches++
				}
				lineBuffer = lineBuffer[:0]
				lastWasNewline = true
			case bufio.ErrBufferFull:
				lineBuffer = append(lineBuffer, fragment...)
				lastWasNewline = false
				continue
			case io.EOF:
				if len(lineBuffer) > 0 {
					lineBuffer = append(lineBuffer, fragment...)
				}
				if len(lineBuffer) > 0 || len(fragment) > 0 || lastWasNewline {
					line := lineBuffer
					if len(line) == 0 {
						line = fragment
					}
					lineNumber++
					if fileMatches < remainingMatches && re.Match(line) {
						fmt.Fprintf(&fileResults, "%s:%d: %s\n", rel, lineNumber, truncateLine(string(line), 200))
						fileMatches++
					}
				}
				_ = file.Close()
				if fileMatches > 0 {
					results.WriteString(fileResults.String())
					count += fileMatches
				}
				if count >= maxResults {
					return filepath.SkipAll
				}
				return nil
			default:
				_ = file.Close()
				return nil
			}
		}
	})

	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if count == 0 {
		return fmt.Sprintf("No matches found for pattern %q", params.Pattern), nil
	}
	if count >= maxResults {
		results.WriteString(fmt.Sprintf("\n... (showing first %d matches)", maxResults))
	}
	return results.String(), err
}

const binaryCheckSize = 8 * 1024

var binaryExtensions = map[string]struct{}{
	".exe": {}, ".bin": {}, ".so": {}, ".dylib": {},
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {},
	".zip": {}, ".tar": {}, ".gz": {}, ".pdf": {},
	".wasm": {}, ".o": {}, ".a": {},
}

func truncateLine(line string, maxLen int) string {
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen] + "..."
}
