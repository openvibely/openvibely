package tooling_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageFilterPolicySharedByLocalAndCI(t *testing.T) {
	repoRoot := repoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "filter-coverage.sh")

	scriptInfo, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat filter script: %v", err)
	}
	if scriptInfo.Mode()&0111 == 0 {
		t.Fatalf("filter script is not executable: mode %v", scriptInfo.Mode())
	}

	scriptSource := readFile(t, scriptPath)
	for _, want := range []string{
		`_templ\.go:`,
		`^github.com/openvibely/openvibely/cmd/`,
		`^github.com/openvibely/openvibely/docs/`,
		`^github.com/openvibely/openvibely/internal/database/migrations/`,
		`^github.com/openvibely/openvibely/internal/update/testfixture/`,
		`^github.com/openvibely/openvibely/internal/service/workflow_service\.go:`,
	} {
		if !strings.Contains(scriptSource, want) {
			t.Fatalf("filter script missing exclusion %q", want)
		}
	}

	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "coverage.txt")
	outputPath := filepath.Join(tmpDir, "coverage.filtered.out")
	input := strings.Join([]string{
		"mode: set",
		"github.com/openvibely/openvibely/web/templates/pages/page_templ.go:1.1,2.1 1 1",
		"github.com/openvibely/openvibely/cmd/server/main.go:1.1,2.1 1 1",
		"github.com/openvibely/openvibely/docs/docs.go:1.1,2.1 1 1",
		"github.com/openvibely/openvibely/internal/database/migrations/001_init.go:1.1,2.1 1 1",
		"github.com/openvibely/openvibely/internal/update/testfixture/fixture.go:1.1,2.1 1 1",
		"github.com/openvibely/openvibely/internal/service/workflow_service.go:1.1,2.1 1 1",
		"github.com/openvibely/openvibely/internal/service/task_service.go:1.1,2.1 1 1",
		"github.com/openvibely/openvibely/pkg/keep/keep.go:1.1,2.1 1 1",
		"",
	}, "\n")
	if err := os.WriteFile(inputPath, []byte(input), 0o600); err != nil {
		t.Fatalf("write input coverage profile: %v", err)
	}

	cmd := exec.Command("bash", scriptPath, inputPath, outputPath)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("filter coverage failed: %v\n%s", err, output)
	}

	filtered := readFile(t, outputPath)
	for _, want := range []string{
		"mode: set",
		"github.com/openvibely/openvibely/internal/service/task_service.go",
		"github.com/openvibely/openvibely/pkg/keep/keep.go",
	} {
		if !strings.Contains(filtered, want) {
			t.Fatalf("filtered coverage missing kept line %q in:\n%s", want, filtered)
		}
	}
	for _, excluded := range []string{
		"page_templ.go",
		"cmd/server/main.go",
		"docs/docs.go",
		"internal/database/migrations/001_init.go",
		"internal/update/testfixture/fixture.go",
		"internal/service/workflow_service.go",
	} {
		if strings.Contains(filtered, excluded) {
			t.Fatalf("filtered coverage kept excluded line %q in:\n%s", excluded, filtered)
		}
	}

	makefile := readFile(t, filepath.Join(repoRoot, "Makefile"))
	if !strings.Contains(makefile, "./scripts/filter-coverage.sh coverage.out coverage.filtered.out") {
		t.Fatal("make test-cover does not call the shared coverage filter with local filenames")
	}
	if strings.Contains(makefile, "grep -Ev") {
		t.Fatal("Makefile still owns a coverage exclusion grep")
	}

	workflow := readFile(t, filepath.Join(repoRoot, ".github", "workflows", "test.yml"))
	coverageSummaryBlock := workflowStepBlock(t, workflow, "- name: Show coverage summary", "- name: Upload coverage reports")
	if !strings.Contains(coverageSummaryBlock, "./scripts/filter-coverage.sh coverage.txt coverage.filtered.out") {
		t.Fatal("coverage summary workflow step does not call the shared coverage filter with CI filenames")
	}
	if strings.Contains(coverageSummaryBlock, "grep -Ev") {
		t.Fatal("coverage summary workflow step still owns a coverage exclusion grep")
	}
	for _, want := range []string{
		"files: coverage.filtered.out",
		"disable_search: true",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow missing Codecov setting %q", want)
		}
	}
}

func workflowStepBlock(t *testing.T, workflow, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(workflow, startMarker)
	if start < 0 {
		t.Fatalf("workflow missing %q", startMarker)
	}
	end := strings.Index(workflow[start:], endMarker)
	if end < 0 {
		t.Fatalf("workflow missing %q after %q", endMarker, startMarker)
	}
	return workflow[start : start+end]
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
