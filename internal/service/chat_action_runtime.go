package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func supportsRuntimeChatActionTools(agent models.LLMConfig) bool {
	switch agent.Provider {
	case models.ProviderOpenAI:
		return agent.IsOpenAIAPIKey() || agent.IsOpenAIOAuth()
	case models.ProviderAnthropic:
		return agent.IsAnthropicAPIKey() || agent.IsOAuth()
	default:
		return false
	}
}

type channelActionSummaryCollector struct {
	createdLines []string
	editedLines  []string
}

func newChannelActionSummaryCollector() *channelActionSummaryCollector {
	return &channelActionSummaryCollector{
		createdLines: []string{},
		editedLines:  []string{},
	}
}

func (c *channelActionSummaryCollector) addCreated(summary string) {
	c.addMarkerLines(summary, "[TASK_ID:", &c.createdLines)
}

func (c *channelActionSummaryCollector) addEdited(summary string) {
	c.addMarkerLines(summary, "[TASK_EDITED:", &c.editedLines)
}

func (c *channelActionSummaryCollector) addMarkerLines(summary, marker string, target *[]string) {
	if c == nil || summary == "" || marker == "" {
		return
	}
	for _, raw := range strings.Split(summary, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, marker) {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			line = "- " + line
		}
		if containsChannelSummaryLine(*target, line) {
			continue
		}
		*target = append(*target, line)
	}
}

func containsChannelSummaryLine(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (c *channelActionSummaryCollector) appendToOutput(output string) string {
	if c == nil {
		return output
	}
	var blocks []string
	if len(c.createdLines) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Created %d task(s):\n", len(c.createdLines)))
		b.WriteString(strings.Join(c.createdLines, "\n"))
		blocks = append(blocks, b.String())
	}
	if len(c.editedLines) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Edited %d task(s):\n", len(c.editedLines)))
		b.WriteString(strings.Join(c.editedLines, "\n"))
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return output
	}

	summary := "\n\n---\n" + strings.Join(blocks, "\n\n")
	if strings.Contains(output, summary) {
		return output
	}
	return output + summary
}

func decodeRuntimeToolInput(input json.RawMessage, dst interface{}) error {
	payload := input
	if len(strings.TrimSpace(string(payload))) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("invalid tool input JSON: %w", err)
	}
	return nil
}

func buildToolMarker(markerName string, input json.RawMessage, hasBody bool) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(markerName))
	if upper == "" {
		return "", fmt.Errorf("marker name is required")
	}
	if !hasBody {
		return "[" + upper + "]", nil
	}
	payload := "{}"
	if len(strings.TrimSpace(string(input))) > 0 {
		var tmp map[string]interface{}
		if err := json.Unmarshal(input, &tmp); err != nil {
			return "", fmt.Errorf("invalid tool input: %w", err)
		}
		b, err := json.Marshal(tmp)
		if err != nil {
			return "", fmt.Errorf("marshal tool input: %w", err)
		}
		payload = string(b)
	}
	return fmt.Sprintf("[%s]%s[/%s]", upper, payload, upper), nil
}

func toolSummaryFromMarker(marker, updated string) string {
	trimmedMarker := strings.TrimSpace(marker)
	trimmedUpdated := strings.TrimSpace(updated)
	if trimmedMarker == "" {
		return trimmedUpdated
	}
	summary := strings.TrimSpace(strings.Replace(trimmedUpdated, trimmedMarker, "", 1))
	if summary == "" {
		return trimmedUpdated
	}
	return summary
}

// actionToolDefinitions returns tool definitions from the canonical registry
// for channel surfaces (Telegram/Slack). Uses orchestrate mode since channels
// always operate in orchestrate mode.
func actionToolDefinitions(surface chatcontrol.Surface, includeThreadTools bool) []llmcontracts.RuntimeToolDefinition {
	return chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, surface, includeThreadTools)
}
