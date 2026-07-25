package service

import (
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/builtinskills"
)

const githubSDLCShippedSkillPath = "builtin/skills/openvibely_github_autonomous_sdlc_bootstrap/SKILL.md"

func githubSDLCRolePrompt(role string) (string, error) {
	data, err := builtinskills.FS.ReadFile(githubSDLCShippedSkillPath)
	if err != nil {
		return "", fmt.Errorf("read shipped GitHub SDLC bootstrap skill: %w", err)
	}
	body := string(data)
	switch strings.TrimSpace(role) {
	case "offering_manager":
		return githubSDLCSkillFencedPrompt(body, "Prompt Pattern For Offering Manager")
	case "bug_finder", "optimization_finder", "redundancy_finder":
		return githubSDLCSkillFencedPrompt(body, "Prompt Pattern For Bug / Optimization / Redundancy Finders")
	case "github_inbox":
		return githubSDLCSkillFencedPrompt(body, "Prompt Pattern For Dev Inbox")
	case "loop_auditor":
		const prefix = "- `GitHub Loop Auditor`, weekly. "
		start := strings.Index(body, prefix)
		if start < 0 {
			return "", fmt.Errorf("shipped GitHub SDLC bootstrap skill has no Loop Auditor prompt")
		}
		start += len(prefix)
		end := strings.IndexByte(body[start:], '\n')
		if end < 0 {
			end = len(body) - start
		}
		prompt := strings.TrimSpace(body[start : start+end])
		if prompt == "" {
			return "", fmt.Errorf("shipped GitHub SDLC bootstrap skill has an empty Loop Auditor prompt")
		}
		return prompt, nil
	default:
		return "", fmt.Errorf("unsupported GitHub SDLC prompt role %q", role)
	}
}

func githubSDLCSkillFencedPrompt(body, heading string) (string, error) {
	marker := "## " + heading + "\n"
	sectionStart := strings.Index(body, marker)
	if sectionStart < 0 {
		return "", fmt.Errorf("shipped GitHub SDLC bootstrap skill section %q is missing", heading)
	}
	fenceOffset := strings.Index(body[sectionStart+len(marker):], "```text\n")
	if fenceOffset < 0 {
		return "", fmt.Errorf("shipped GitHub SDLC bootstrap skill section %q has no text fence", heading)
	}
	promptStart := sectionStart + len(marker) + fenceOffset + len("```text\n")
	fenceEnd := strings.Index(body[promptStart:], "\n```")
	if fenceEnd < 0 {
		return "", fmt.Errorf("shipped GitHub SDLC bootstrap skill section %q has no closing fence", heading)
	}
	prompt := strings.TrimSpace(body[promptStart : promptStart+fenceEnd])
	if prompt == "" {
		return "", fmt.Errorf("shipped GitHub SDLC bootstrap skill section %q is empty", heading)
	}
	return prompt, nil
}
