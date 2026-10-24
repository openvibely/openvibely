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
    version: 0.0.0-dev
    health_check: [version]
  - name: libreoffice
    version: 0.0.0-local
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
		if err := Validate([]Dependency{{Name: "tool", Version: "1.0.0", HealthCheck: []string{"version"}, Packages: map[string]Package{"linux/amd64": bad}}}); err == nil {
			t.Fatal("accepted unsafe package")
		}
	}
}

func TestDependencyVersionSemVer(t *testing.T) {
	for _, version := range []string{"0.0.0", "26.8.1", "0.0.0-rc6", "1.2.3-rc.1", "1.2.3-alpha-beta.0+build.001", "1.2.3+001"} {
		raw := "---\ndependencies:\n  - name: tool\n    version: " + version + "\n    health_check: [version]\n---\n"
		deps, err := Parse([]byte(raw))
		if err != nil || len(deps) != 1 || deps[0].Version != version {
			t.Fatalf("%q: %v %v", version, deps, err)
		}
	}
	for _, version := range []string{"", "1", "1.2", "development", "local", "v1.2.3", "01.2.3", "1.02.3", "1.2.03", "1.2.3-01", "1.2.3-rc.01", "1.2.3-", "1.2.3+", "1.2.3-a..b", "1.2.3+a..b", "1.2.3_rc1", "../1.2.3", "1.2.3\n", "1.2.3+" + strings.Repeat("x", 96)} {
		err := Validate([]Dependency{{Name: "tool", Version: version, HealthCheck: []string{"version"}}})
		if err == nil || !strings.Contains(err.Error(), "SemVer") {
			t.Fatalf("%q: expected SemVer error, got %v", version, err)
		}
	}
}
