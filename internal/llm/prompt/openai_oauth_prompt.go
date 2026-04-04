package prompt

import (
	_ "embed"
	"strings"
)

//go:embed openai_oauth_working_with_user.txt
var openAIOAuthWorkingWithUserSection string

// BuildOpenAIOAuthSystemPrompt appends the OpenAI OAuth-specific "Working with
// the user" prompt section to the provided base prompt.
func BuildOpenAIOAuthSystemPrompt(base string) string {
	section := strings.TrimSpace(openAIOAuthWorkingWithUserSection)
	base = strings.TrimSpace(base)

	if section == "" {
		return base
	}
	if strings.Contains(base, section) {
		return base
	}
	if base == "" {
		return section
	}
	return base + "\n\n" + section
}
