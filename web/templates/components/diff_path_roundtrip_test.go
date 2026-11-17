package components

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/gitdiff"
)

func TestParseDiffOutput_CapturedPathsPreserveWhitespace(t *testing.T) {
	for _, name := range []string{" leading.txt", "trailing.txt ", "tab\tfile.txt\t", "résumé.txt"} {
		for kind, content := range map[string]string{"text": "content\n", "empty": "", "binary": "\x00data"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				runGit := func(args ...string) {
					t.Helper()
					cmd := exec.Command("git", args...)
					cmd.Dir = dir
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git %v: %v\n%s", args, err, out)
					}
				}
				runGit("init", "-q")
				runGit("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial")
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				check := func() {
					t.Helper()
					files := ParseDiffOutput(gitdiff.Capture(dir))
					if len(files) != 1 {
						t.Fatalf("got %d files, want 1", len(files))
					}
					if files[0].Path != name {
						t.Errorf("path = %q, want %q", files[0].Path, name)
					}
				}
				check() // Synthetic untracked diff.
				runGit("add", "--", name)
				check() // Native Git headers can include a tab delimiter.
			})
		}
	}
}
