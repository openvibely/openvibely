package models

import "strings"

// ChatMode controls how /chat should behave for orchestration calls.
type ChatMode string

const (
	ChatModeOrchestrate ChatMode = "orchestrate"
	ChatModePlan        ChatMode = "plan"
)

// NormalizeChatMode returns a valid chat mode, defaulting to orchestrate.
func NormalizeChatMode(raw string) ChatMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ChatModePlan):
		return ChatModePlan
	default:
		return ChatModeOrchestrate
	}
}

func (m ChatMode) IsPlan() bool {
	return m == ChatModePlan
}
