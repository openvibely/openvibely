package gitdiff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
)

// Capture runs `git diff HEAD` in the given directory to capture
// all uncommitted changes made during task execution. Returns the diff
// output or empty string if no changes or git is not available.
func Capture(workDir string) string {
	if workDir == "" {
		return ""
	}
	// First check if this is a git repo
	checkCmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	checkCmd.Dir = workDir
	if err := checkCmd.Run(); err != nil {
		return ""
	}

	// Capture both staged and unstaged changes
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		// If HEAD doesn't exist (new repo), try just `git diff`
		cmd2 := exec.Command("git", "diff")
		cmd2.Dir = workDir
		out, err = cmd2.Output()
		if err != nil {
			applog.Infof("[agent-svc] captureGitDiff error: %v", err)
			return ""
		}
	}

	// Also capture untracked files with full diff content
	// Use -z so names are emitted verbatim (no core.quotePath escaping) and
	// split on NUL only, preserving any leading/trailing whitespace in names.
	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z")
	untrackedCmd.Dir = workDir
	untrackedOut, _ := untrackedCmd.Output()

	result := string(out)

	// Generate proper unified diff for each untracked file
	for _, f := range strings.Split(string(untrackedOut), "\x00") {
		if f == "" {
			continue
		}
		fileDiff := generateNewFileDiff(workDir, f)
		if fileDiff != "" {
			result += fileDiff
		}
	}

	return result
}

// generateNewFileDiff creates a unified diff for a new (untracked) file by reading
// its content and formatting it as a git diff with all lines as additions.
func generateNewFileDiff(workDir, relPath string) string {
	absPath := filepath.Join(workDir, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return ""
	}

	// Skip directories
	if info.IsDir() {
		return ""
	}

	// For binary files, just show a notice
	content, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	if isBinaryContent(content) {
		return fmt.Sprintf("\ndiff --git %s %s\nnew file mode 100644\nBinary files /dev/null and %s differ\n", QuoteHeaderPath("a/", relPath), QuoteHeaderPath("b/", relPath), QuoteHeaderPath("b/", relPath))
	}

	lines := strings.Split(string(content), "\n")
	// Remove trailing empty line from Split if file ends with newline
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return fmt.Sprintf("\ndiff --git %s %s\nnew file mode 100644\n", QuoteHeaderPath("a/", relPath), QuoteHeaderPath("b/", relPath))
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("\ndiff --git %s %s\n", QuoteHeaderPath("a/", relPath), QuoteHeaderPath("b/", relPath)))
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	buf.WriteString(fmt.Sprintf("+++ %s\n", QuoteHeaderPath("b/", relPath)))
	buf.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
	for _, l := range lines {
		buf.WriteString("+" + l + "\n")
	}

	return buf.String()
}

// QuoteHeaderPath returns prefix+path as it must appear in a generated diff
// header. Like git, names containing control characters, double quotes, or
// backslashes are wrapped in double quotes with C-style escapes so the header
// stays on one line and parses back to the exact filename bytes. Other names,
// including ones with spaces or accented characters, are emitted verbatim.
func QuoteHeaderPath(prefix, path string) string {
	name := prefix + path
	needsQuote := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c == 0x7f || c == '"' || c == '\\' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return name
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch c {
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if c < 0x20 || c == 0x7f {
				b.WriteString(fmt.Sprintf(`\%03o`, c))
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// isBinaryContent checks if the content appears to be binary by looking for null bytes
// in the first 8000 bytes (same heuristic as git).
func isBinaryContent(data []byte) bool {
	checkLen := len(data)
	if checkLen > 8000 {
		checkLen = 8000
	}
	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
