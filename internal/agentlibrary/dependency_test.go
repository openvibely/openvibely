package agentlibrary

import (
	"github.com/openvibely/openvibely/internal/skilldeps"
	"testing"
)

func TestStandaloneRenderRetainsDependencies(t *testing.T) {
	d := &SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: SkillBlock{Key: "documents"}, Dependencies: []skilldeps.Dependency{{Name: "paperworks", Version: "development", HealthCheck: []string{"version"}}}}
	markdown, err := RenderSkillMarkdown(d, "Use the tool.")
	if err != nil {
		t.Fatal(err)
	}
	deps, err := skilldeps.Parse([]byte(markdown))
	if err != nil || len(deps) != 1 || deps[0].Name != "paperworks" {
		t.Fatalf("dependencies lost during render: %v %v", deps, err)
	}
	d.Dependencies[0].Name = "../bad"
	if err := d.Validate(); err == nil {
		t.Fatal("invalid dependency accepted")
	}
}
