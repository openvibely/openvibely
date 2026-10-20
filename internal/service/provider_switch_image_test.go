package service

import (
	"context"
	"encoding/json"
	"fmt"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"testing"

	llmanthropic "github.com/openvibely/openvibely/internal/llm/anthropic"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestProviderSwitchParallelImages(t *testing.T) {
	// view_image adds a user image immediately after its own result, before
	// the remaining parallel tool results have necessarily been appended.
	for _, multiple := range []bool{false, true} {
		transcript := `{"responses_input":[
			{"type":"function_call","call_id":"a","name":"view_image","arguments":"{}"},
			{"type":"function_call","call_id":"b","name":"view_image","arguments":"{}"},
			{"type":"function_call","call_id":"c","name":"read_file","arguments":"{}"},
			{"type":"function_call_output","call_id":"b","output":"B"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"image B"},{"type":"input_image","image_url":"data:image/png;base64,Qg=="}]},
			{"type":"function_call_output","call_id":"a","output":"A"},`
		if multiple {
			transcript += `{"type":"message","role":"user","content":[{"type":"input_text","text":"image A"},{"type":"input_image","image_url":"data:image/png;base64,QQ=="}]},`
		}
		transcript += `{"type":"function_call_output","call_id":"c","output":"C"},
			{"type":"message","role":"assistant","content":"first round done"},
			{"type":"function_call","call_id":"d","name":"read_file","arguments":"{}"},
			{"type":"function_call_output","call_id":"d","output":"D"}]}`
		for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible, models.ProviderOllama, models.ProviderAnthropic} {
			for _, mode := range []string{"summary", "followup"} {
				name := string(provider) + "/" + mode
				if multiple {
					name += "/multiple"
				}
				t.Run(name, func(t *testing.T) {
					history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}
					var input []json.RawMessage
					if mode == "summary" {
						var err error
						input, err = summaryHistoryInput(provider, history)
						require.NoError(t, err)
					} else {
						calls := 0
						_, err := (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
							calls++
							require.Nil(t, req.SummaryInput)
							var outgoing any = compatible.SummaryHistory(req.ChatHistory)
							if provider == models.ProviderAnthropic {
								outgoing = llmanthropic.SummaryHistory(req.ChatHistory)
							}
							raw, err := json.Marshal(outgoing)
							require.NoError(t, err)
							require.NoError(t, json.Unmarshal(raw, &input))
							return llmcontracts.AgentResult{Output: "done"}, nil
						}), llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true, Message: "continue", ChatHistory: history, Agent: models.LLMConfig{Provider: provider, Model: "test", ContextWindow: 200000}})
						require.NoError(t, err)
						require.Equal(t, 1, calls)
					}
					// Flatten only for assertions: preserve call groups, result
					// order, exact image payloads, and the next tool round.
					var events []string
					for _, raw := range input {
						var m struct {
							ContentBlocks json.RawMessage `json:"content_blocks"`
							Role          string
							Content       json.RawMessage
							ToolCallID    string                `json:"tool_call_id"`
							ToolCalls     []struct{ ID string } `json:"tool_calls"`
						}
						require.NoError(t, json.Unmarshal(raw, &m))
						if len(m.ContentBlocks) > 0 {
							m.Content = m.ContentBlocks
						}
						for _, call := range m.ToolCalls {
							events = append(events, "call:"+call.ID)
						}
						if m.Role == "tool" {
							events = append(events, "result:"+m.ToolCallID)
							continue
						}
						var text string
						if json.Unmarshal(m.Content, &text) == nil {
							if text != "" {
								events = append(events, text)
							}
							continue
						}
						var blocks []struct {
							Type      string
							Text      string
							ID        string
							ToolUseID string               `json:"tool_use_id"`
							ImageURL  struct{ URL string } `json:"image_url"`
							Source    struct{ Data string }
						}
						require.NoError(t, json.Unmarshal(m.Content, &blocks))
						for _, b := range blocks {
							switch b.Type {
							case "tool_use":
								events = append(events, "call:"+b.ID)
							case "tool_result":
								events = append(events, "result:"+b.ToolUseID)
							case "text":
								events = append(events, b.Text)
							case "image_url":
								events = append(events, b.ImageURL.URL)
							case "image":
								events = append(events, "data:image/png;base64,"+b.Source.Data)
							}
						}
					}
					expected := []string{"call:a", "call:b", "call:c", "result:b", "result:a", "result:c", "image B", "data:image/png;base64,Qg=="}
					if multiple {
						expected = append(expected, "image A", "data:image/png;base64,QQ==")
					}
					expected = append(expected, "first round done", "call:d", "result:d")
					require.Equal(t, expected, events)
				})
			}
		}
	}
}

// Inspect structured content only: base64 embedded inside a JSON string is not an image.
func containsStructuredImage(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if v["type"] == "image" || v["type"] == "image_url" || v["type"] == "input_image" {
			return true
		}
		for _, child := range v {
			if containsStructuredImage(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsStructuredImage(child) {
				return true
			}
		}
	}
	return false
}

func TestAnthropicToolResultImageConversion(t *testing.T) {
	transcript := `{"messages":[
 {"role":"assistant","content_blocks":[{"type":"tool_use","id":"a","name":"view_image","input":{}},{"type":"tool_use","id":"b","name":"view_image","input":{}}]},
 {"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"image A"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QQ=="}}]},{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"image B"},{"type":"image","source":{"type":"url","url":"https://example.com/b.png"}}]}]},
 {"role":"assistant","content":"done"}]}`
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderOpenAICompatible} {
		for _, summary := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/summary=%t", provider, summary), func(t *testing.T) {
				history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}
				converted := normalizeProviderReplay(provider, history, summary)
				var messages []openaiclient.CompletionsHistoryMessage
				require.NoError(t, json.Unmarshal([]byte(converted[0].ReplayMessages[0].TranscriptJSON), &messages))
				require.Len(t, messages, 5)
				require.Len(t, messages[0].ToolCalls, 2)
				require.Equal(t, "tool", messages[1].Role)
				require.Equal(t, "a", messages[1].ToolCallID)
				require.Equal(t, "image A", messages[1].Content)
				require.Equal(t, "tool", messages[2].Role)
				require.Equal(t, "b", messages[2].ToolCallID)
				require.Equal(t, "image B", messages[2].Content)
				require.Equal(t, "user", messages[3].Role)
				require.Equal(t, []map[string]interface{}{
					{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64,QQ=="}},
					{"type": "image_url", "image_url": map[string]interface{}{"url": "https://example.com/b.png"}},
				}, messages[3].ImageContent)
				require.Equal(t, "done", messages[4].Content)
				require.Equal(t, transcript, history[0].ReplayMessages[0].TranscriptJSON)
				input, err := summaryHistoryInput(provider, history)
				require.NoError(t, err)
				raw, err := json.Marshal(input)
				require.NoError(t, err)
				var body any
				require.NoError(t, json.Unmarshal(raw, &body))
				require.True(t, containsStructuredImage(body))
			})
		}
	}
}
