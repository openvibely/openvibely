package skilldeps

import (
	"strings"
	"testing"
)

func TestParseDependencies(t *testing.T) {
	raw := `---
kind: openvibely.agent_skill
dependencies:
  - name: paperworks
    version: development
    health_check: [version]
  - name: libreoffice
    version: local
    health_check: [--version]
---
Body
`
	deps, err := Parse([]byte(raw))
	if err != nil || len(deps) != 2 {
		t.Fatalf("%v %v", deps, err)
	}
	for _, invalid := range []string{strings.Replace(raw, "name: paperworks", "name: ../bad", 1), strings.Replace(raw, "name: libreoffice", "name: paperworks", 1), strings.Replace(raw, "health_check: [version]", "health_check: []", 1)} {
		if _, err := Parse([]byte(invalid)); err == nil {
			t.Fatal("accepted invalid dependency")
		}
	}
	if deps, err := Parse([]byte("ordinary prose\ndependencies: fake")); err != nil || len(deps) != 0 {
		t.Fatal("parsed prose as configuration")
	}
}

func TestValidatePackage(t *testing.T) {
	p := Package{URL: "https://example.com/tool.zip", SHA256: strings.Repeat("a", 64), Format: "zip", Executable: "bin/tool"}
	for _, modify := range []func(*Package){func(p *Package) { p.URL = "http://example.com/tool.zip" }, func(p *Package) { p.SHA256 = "bad" }, func(p *Package) { p.Executable = "../tool" }, func(p *Package) { p.Executable = "C:/tool" }, func(p *Package) { p.Format = "shell" }} {
		bad := p
		modify(&bad)
		if err := Validate([]Dependency{{Name: "tool", Version: "1", HealthCheck: []string{"version"}, Packages: map[string]Package{"linux/amd64": bad}}}); err == nil {
			t.Fatal("accepted unsafe package")
		}
	}
}
