package builtinskills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FS contains the bundled lifecycle system-agent skills, reusable standalone
// skills, and default AGENTS.md / SKILLS.md index files. Startup syncs this
// tree into the configured global skills root so the normal filesystem catalog
// path discovers the built-ins exactly like user-created skills.
//
//go:embed builtin/agents/AGENTS.md
//go:embed builtin/agents/*/SKILLS.md
//go:embed builtin/agents/*/skills/*/SKILL.md
//go:embed builtin/skills/SKILLS.md
//go:embed builtin/skills/*/SKILL.md
//go:embed builtin/skills/*/templates/*.md
//go:embed builtin/skills/*/references/*.md
var FS embed.FS

const githubSDLCPromptTemplatePath = "builtin/skills/openvibely_github_autonomous_sdlc_bootstrap/templates/github-loop-prompts.md"

var githubSDLCPromptSectionByRole = map[string]string{
	"offering_manager":    "Offering Manager",
	"bug_finder":          "Bug Finder / Optimization Finder / Redundancy Finder",
	"optimization_finder": "Bug Finder / Optimization Finder / Redundancy Finder",
	"redundancy_finder":   "Bug Finder / Optimization Finder / Redundancy Finder",
	"github_inbox":        "Dev Inbox",
	"loop_auditor":        "Loop Auditor",
}

// GitHubSDLCRolePrompt returns the exact maintained prompt used by both the
// bootstrap skill and the GitHub SDLC Automation template.
func GitHubSDLCRolePrompt(role string) (string, error) {
	section, ok := githubSDLCPromptSectionByRole[strings.TrimSpace(role)]
	if !ok {
		return "", fmt.Errorf("unsupported GitHub SDLC prompt role %q", role)
	}
	data, err := FS.ReadFile(githubSDLCPromptTemplatePath)
	if err != nil {
		return "", fmt.Errorf("read GitHub SDLC prompt templates: %w", err)
	}
	body := string(data)
	marker := "## " + section + "\n"
	sectionStart := strings.Index(body, marker)
	if sectionStart < 0 {
		return "", fmt.Errorf("GitHub SDLC prompt section %q is missing", section)
	}
	fenceOffset := strings.Index(body[sectionStart+len(marker):], "```text\n")
	if fenceOffset < 0 {
		return "", fmt.Errorf("GitHub SDLC prompt section %q has no text fence", section)
	}
	promptStart := sectionStart + len(marker) + fenceOffset + len("```text\n")
	fenceEnd := strings.Index(body[promptStart:], "\n```")
	if fenceEnd < 0 {
		return "", fmt.Errorf("GitHub SDLC prompt section %q has no closing fence", section)
	}
	prompt := strings.TrimSpace(body[promptStart : promptStart+fenceEnd])
	if prompt == "" {
		return "", fmt.Errorf("GitHub SDLC prompt section %q is empty", section)
	}
	return prompt, nil
}

// SyncTo writes the embedded built-in files under root.
//
// Bundled skill package files are always overwritten: those are the app's source
// of truth and travel with the binary. AGENTS.md and user-managed per-agent
// SKILLS.md files are only written when missing. Protected built-in system
// agent root declarations are also overwritten because they carry system policy
// and permission grants, not user narrative. The reusable standalone skills
// index is merged so existing user-managed global SKILLS.md content is
// preserved while bundled global skills remain discoverable.
func SyncTo(root string) error {
	if root == "" {
		return nil
	}
	return fs.WalkDir(FS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel("builtin", path)
		if err != nil {
			return err
		}
		data, err := FS.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		base := filepath.Base(dst)
		isIndex := base == "AGENTS.md" || base == "SKILLS.md"
		relSlash := filepath.ToSlash(rel)
		isProtectedSystemDeclaration := relSlash == "agents/skill_curator/SKILLS.md" ||
			relSlash == "agents/memory_curator/SKILLS.md" ||
			relSlash == "agents/goal/SKILLS.md"
		if relSlash == "skills/SKILLS.md" {
			return mergeStandaloneSkillsIndex(dst, string(data))
		}
		if isIndex && !isProtectedSystemDeclaration {
			if _, err := os.Stat(dst); err == nil {
				// User-managed once it exists: don't clobber hand edits.
				return nil
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", dst, err)
			}
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		return nil
	})
}

var skillsIndexHeaderRegexp = regexp.MustCompile(`(?m)^##[ \t]+([A-Za-z0-9][A-Za-z0-9_./-]*)\s*$`)

func mergeStandaloneSkillsIndex(dst, bundled string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	existingBytes, err := os.ReadFile(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(dst, []byte(ensureTrailingNewline(bundled)), 0o644)
		}
		return fmt.Errorf("read %s: %w", dst, err)
	}
	existing := string(existingBytes)
	existingHeaders := indexHeaders(existing)
	var additions []string
	for _, section := range skillIndexSections(bundled) {
		if _, ok := existingHeaders[section.header]; ok {
			continue
		}
		additions = append(additions, strings.TrimSpace(section.body))
	}
	if len(additions) == 0 {
		return nil
	}
	merged := strings.TrimRight(existing, "\r\n")
	if strings.TrimSpace(merged) != "" {
		merged += "\n\n"
	}
	merged += strings.Join(additions, "\n\n")
	merged += "\n"
	return os.WriteFile(dst, []byte(merged), 0o644)
}

type skillIndexSection struct {
	header string
	body   string
}

func skillIndexSections(content string) []skillIndexSection {
	matches := skillsIndexHeaderRegexp.FindAllStringSubmatchIndex(content, -1)
	sections := make([]skillIndexSection, 0, len(matches))
	for i, match := range matches {
		if len(match) < 4 {
			continue
		}
		start := match[0]
		end := len(content)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		header := strings.TrimSpace(content[match[2]:match[3]])
		sections = append(sections, skillIndexSection{header: header, body: content[start:end]})
	}
	return sections
}

func indexHeaders(content string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, match := range skillsIndexHeaderRegexp.FindAllStringSubmatch(content, -1) {
		if len(match) >= 2 {
			out[strings.TrimSpace(match[1])] = struct{}{}
		}
	}
	return out
}

func ensureTrailingNewline(content string) string {
	if strings.HasSuffix(content, "\n") {
		return content
	}
	return content + "\n"
}
