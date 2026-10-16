// Package skilldeps resolves versioned executable dependencies declared by skills.
package skilldeps

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Dependency struct {
	Name                string             `yaml:"name" json:"name"`
	EnvironmentVariable string             `yaml:"environment_variable,omitempty" json:"environment_variable,omitempty"`
	Version             string             `yaml:"version" json:"version"`
	HealthCheck         []string           `yaml:"health_check,omitempty" json:"health_check,omitempty"`
	Packages            map[string]Package `yaml:"packages,omitempty" json:"packages,omitempty"`
}

type Package struct {
	URL        string `yaml:"url" json:"url"`
	SHA256     string `yaml:"sha256" json:"sha256"`
	Format     string `yaml:"format" json:"format"`
	Executable string `yaml:"executable" json:"executable"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

func Validate(deps []Dependency) error {
	seen := map[string]bool{}
	environments := map[string]bool{}
	for _, d := range deps {
		if !namePattern.MatchString(d.Name) || !versionPattern.MatchString(d.Version) || seen[d.Name] {
			return fmt.Errorf("invalid or duplicate dependency %q", d.Name)
		}
		seen[d.Name] = true
		if d.EnvironmentVariable != "" {
			if environments[d.EnvironmentVariable] {
				return fmt.Errorf("duplicate dependency environment variable %s", d.EnvironmentVariable)
			}
			environments[d.EnvironmentVariable] = true
		}
		if d.EnvironmentVariable != "" && !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(d.EnvironmentVariable) {
			return fmt.Errorf("invalid environment variable for %s", d.Name)
		}
		if len(d.HealthCheck) == 0 {
			return fmt.Errorf("dependency %s requires health_check arguments", d.Name)
		}
		for platform, p := range d.Packages {
			switch platform {
			case "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64":
			default:
				return fmt.Errorf("unsupported platform key %q", platform)
			}
			u, err := url.Parse(p.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("dependency %s requires an HTTPS package URL", d.Name)
			}
			digest, err := hex.DecodeString(p.SHA256)
			if err != nil || len(digest) != 32 {
				return fmt.Errorf("dependency %s requires SHA-256", d.Name)
			}
			switch p.Format {
			case "raw", "zip", "tar.gz", "dmg":
			default:
				return fmt.Errorf("unsupported package format %q", p.Format)
			}
			if p.Format == "dmg" && !strings.HasPrefix(platform, "darwin/") {
				return fmt.Errorf("DMG requires darwin")
			}
			if !safeRelative(p.Executable) {
				return fmt.Errorf("invalid executable path for %s", d.Name)
			}
		}
	}
	return nil
}

func safeRelative(s string) bool {
	return s != "" && s != "." && !strings.ContainsAny(s, "\\:\x00") && !strings.HasPrefix(s, "/") && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}

// Parse reads only frontmatter; document contents cannot declare dependencies.
func Parse(markdown []byte) ([]Dependency, error) {
	text := strings.ReplaceAll(string(markdown), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, nil
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated skill frontmatter")
	}
	var header struct {
		Dependencies []Dependency `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &header); err != nil {
		return nil, err
	}
	return header.Dependencies, Validate(header.Dependencies)
}

func OverrideVariable(name string) string {
	return "OPENVIBELY_TOOL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}
