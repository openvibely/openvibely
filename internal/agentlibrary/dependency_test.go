package agentlibrary

import (
	"github.com/openvibely/openvibely/internal/skilldeps"
	"testing"
)

func TestStandaloneRenderRetainsDependencies(t *testing.T) {
	d := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "documents"}, Dependencies: []skilldeps.Dependency{{Name: "paperworks", Version: "0.0.0-dev", HealthCheck: []string{"version"}}}}
	markdown, err := RenderSkillMarkdown(d, "Use the tool.")
	if err != nil {
		t.Fatal(err)
	}
	deps, err := skilldeps.Parse([]byte(markdown))
	if err != nil || len(deps) != 1 || deps[0].Name != "paperworks" || deps[0].Version != "0.0.0-dev" {
		t.Fatalf("dependencies lost during render: %v %v", deps, err)
	}
	d.Dependencies[0].Name = "../bad"
	if err := d.Validate(); err == nil {
		t.Fatal("invalid dependency accepted")
	}
}

func TestNormalizeStandardFrontmatterRetainsDependencies(t *testing.T) {
	content := "---\nname: report-tool\ndescription: Generate reports.\ndependencies:\n  - name: report-tool\n    version: 1.2.3\n    environment_variable: REPORT_TOOL\n    health_check: [--version]\n---\n# Reports\nUse REPORT_TOOL to generate a report.\n"
	decl, body, err := NormalizeStandaloneSkillPackage(content, "report-tool", "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(decl.Dependencies) != 1 {
		t.Fatalf("dependency count before=1 after=%d", len(decl.Dependencies))
	}
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		t.Fatal(err)
	}
	deps, err := skilldeps.Parse([]byte(rendered))
	if err != nil || len(deps) != 1 || deps[0].EnvironmentVariable != "REPORT_TOOL" || deps[0].Version != "1.2.3" {
		t.Fatalf("dependency lost after normalize and render: %v %v", deps, err)
	}
}
