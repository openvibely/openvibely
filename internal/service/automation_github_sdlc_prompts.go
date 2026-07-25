package service

import (
	_ "embed"
	"fmt"
	"strings"
)

// githubSDLCPromptTemplates is an Automation-owned mirror of the maintained
// GitHub autonomous SDLC role prompts. The bootstrap skill remains unchanged.
//
//go:embed automation_github_sdlc_prompts.md
var githubSDLCPromptTemplates string

var githubSDLCPromptSectionByRole = map[string]string{
	"offering_manager":    "Offering Manager",
	"bug_finder":          "Bug Finder / Optimization Finder / Redundancy Finder",
	"optimization_finder": "Bug Finder / Optimization Finder / Redundancy Finder",
	"redundancy_finder":   "Bug Finder / Optimization Finder / Redundancy Finder",
	"github_inbox":        "Dev Inbox",
	"loop_auditor":        "Loop Auditor",
}

func githubSDLCRolePrompt(role string) (string, error) {
	section, ok := githubSDLCPromptSectionByRole[strings.TrimSpace(role)]
	if !ok {
		return "", fmt.Errorf("unsupported GitHub SDLC prompt role %q", role)
	}
	marker := "## " + section + "\n"
	sectionStart := strings.Index(githubSDLCPromptTemplates, marker)
	if sectionStart < 0 {
		return "", fmt.Errorf("GitHub SDLC prompt section %q is missing", section)
	}
	fenceOffset := strings.Index(githubSDLCPromptTemplates[sectionStart+len(marker):], "```text\n")
	if fenceOffset < 0 {
		return "", fmt.Errorf("GitHub SDLC prompt section %q has no text fence", section)
	}
	promptStart := sectionStart + len(marker) + fenceOffset + len("```text\n")
	fenceEnd := strings.Index(githubSDLCPromptTemplates[promptStart:], "\n```")
	if fenceEnd < 0 {
		return "", fmt.Errorf("GitHub SDLC prompt section %q has no closing fence", section)
	}
	prompt := strings.TrimSpace(githubSDLCPromptTemplates[promptStart : promptStart+fenceEnd])
	if prompt == "" {
		return "", fmt.Errorf("GitHub SDLC prompt section %q is empty", section)
	}
	return prompt, nil
}
