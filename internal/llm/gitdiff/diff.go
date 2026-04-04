package gitdiff

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
			log.Printf("[agent-svc] captureGitDiff error: %v", err)
			return ""
		}
	}

	// Also capture untracked files with full diff content
	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	untrackedCmd.Dir = workDir
	untrackedOut, _ := untrackedCmd.Output()

	result := string(out)

	// Generate proper unified diff for each untracked file
	if len(untrackedOut) > 0 {
		untracked := strings.TrimSpace(string(untrackedOut))
		if untracked != "" {
			for _, f := range strings.Split(untracked, "\n") {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				fileDiff := generateNewFileDiff(workDir, f)
				if fileDiff != "" {
					result += fileDiff
				}
			}
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
		return fmt.Sprintf("\ndiff --git a/%s b/%s\nnew file mode 100644\nBinary files /dev/null and b/%s differ\n", relPath, relPath, relPath)
	}

	lines := strings.Split(string(content), "\n")
	// Remove trailing empty line from Split if file ends with newline
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return fmt.Sprintf("\ndiff --git a/%s b/%s\nnew file mode 100644\n", relPath, relPath)
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("\ndiff --git a/%s b/%s\n", relPath, relPath))
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", relPath))
	buf.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
	for _, l := range lines {
		buf.WriteString("+" + l + "\n")
	}

	return buf.String()
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
