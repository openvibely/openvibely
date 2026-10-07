package models

import "strings"

// ConversationEfforts is shared by the picker and override validation.
func ConversationEfforts(c LLMConfig) []string {
	if spec, ok := LookupModel(c.Provider, c.Model); ok {
		return spec.ReasoningEfforts
	}
	if c.Provider == ProviderOpenAICompatible {
		switch strings.ToLower(strings.TrimSpace(c.Model)) {
		case "kimi-k3":
			return []string{"low", "high", "max"}
		case "glm-5.2":
			return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
		}
	}
	return nil
}

func ValidConversationEffort(c LLMConfig, effort string) bool {
	for _, value := range ConversationEfforts(c) {
		if value == effort {
			return true
		}
	}
	return false
}
