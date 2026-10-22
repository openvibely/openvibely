package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/attachment"
	"github.com/stretchr/testify/require"
)

// Exercise the real request loop, including the boundaries before the first
// request, after tool results, and before accepting pending steering.
func TestCompactionBudgetBoundaryMatrix(t *testing.T) {
	for _, phase := range []string{"preturn", "inturn", "steering"} {
		mediaCases := []string{"text", "image"}
		if phase == "inturn" {
			mediaCases = append(mediaCases, "tool_image")
		}
		for _, media := range mediaCases {
			for _, outcome := range []string{"below_limit", "compact_success", "compact_failure"} {
				t.Run(phase+"/"+media+"/"+outcome, func(t *testing.T) {
					pressure := outcome != "below_limit"
					samples, compactions, tools := 0, 0, 0
					claimed := false
					var persisted []any
					imageURL := "data:image/png;base64," + strings.Repeat("A", 1000000)
					initial := []any{map[string]any{"type": "message", "role": "user", "content": "OLD_CONTEXT"}}
					if media == "image" {
						initial = append(initial, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": imageURL}}})
					}
					if pressure && phase == "preturn" {
						initial = append(initial, map[string]any{"type": "message", "role": "user", "content": strings.Repeat("x", 100000)})
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						raw, _ := json.Marshal(body["input"])
						if strings.HasSuffix(r.URL.Path, "/responses/compact") {
							compactions++
							require.True(t, pressure, "image payload alone must not trigger compaction")
							require.NotContains(t, string(raw), "STEER_MARKER", "pending steering must be appended after compaction")
							if phase == "inturn" {
								require.Contains(t, string(raw), "TOOL_MARKER")
							}
							if outcome == "compact_failure" {
								w.WriteHeader(400)
								fmt.Fprint(w, `{"error":{"message":"compaction rejected","type":"invalid_request_error"}}`)
								return
							}
							w.Header().Set("Content-Type", "application/json")
							// Retain images as native compaction can do. They must not trigger again.
							compacted := []any{map[string]any{"type": "compaction", "encrypted_content": "summary"}}
							if media == "image" {
								compacted = append(compacted, initial[1])
							}
							if media == "tool_image" {
								for _, item := range body["input"].([]any) {
									raw, _ := json.Marshal(item)
									if strings.Contains(string(raw), "input_image") {
										compacted = append(compacted, item)
									}
								}
							}
							require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"output": compacted}))
							return
						}
						samples++
						if samples > 3 {
							t.Error("unexpected repeated continuation")
							w.WriteHeader(400)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if phase == "inturn" && samples == 1 {
							toolUsage := 100
							if media == "tool_image" && pressure {
								toolUsage = 30000
							}
							fmt.Fprint(w, buildSSE([]string{`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"}}`, fmt.Sprintf(`{"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":%d,"output_tokens":5}}}`, toolUsage)}))
							return
						}
						if samples == 2 && phase == "steering" {
							require.Contains(t, string(raw), "STEER_MARKER")
						}
						if samples == 2 && media == "tool_image" && !pressure {
							require.Contains(t, string(raw), "input_image")
						}
						if samples == 2 && phase == "inturn" && !pressure {
							require.Contains(t, string(raw), "TOOL_MARKER")
						}
						usage := 100
						if phase == "steering" && samples == 1 && pressure {
							usage = 30000
						}
						fmt.Fprint(w, buildSSE([]string{`{"type":"response.output_text.delta","delta":"done"}`, fmt.Sprintf(`{"type":"response.completed","response":{"id":"r%d","status":"completed","usage":{"input_tokens":%d,"output_tokens":5}}}`, samples, usage)}))
					}))
					defer server.Close()
					old := OpenAIAPIBaseURL
					OpenAIAPIBaseURL = server.URL + "/"
					defer func() { OpenAIAPIBaseURL = old }()
					client := newHTTPTestAPIKeyClient("test")
					opts := &AgenticOptions{Model: "gpt-5.3-codex", MaxTurns: 3, SkipDefaultTools: true, OmitExecutionEnvironment: true, AutoCompaction: true, CompactionTokenThreshold: 10000, InitialInputItems: initial, ToolOutputTokenLimit: 50000,
						OnHistory: func(_ context.Context, items []any) error { persisted = append([]any(nil), items...); return nil },
					}
					if phase == "inturn" {
						opts.ExtraTools = []ToolDefinition{{Type: "function", Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
						opts.ToolExecutor = func(ctx context.Context, _ string, _ json.RawMessage) (string, bool, error) {
							tools++
							if media == "tool_image" {
								out, err := attachment.StoreToolImage(ctx, attachment.ToolImage{Name: "TOOL_MARKER", MediaType: "image/png", Data: []byte(strings.Repeat("a", 750000))})
								return out, false, err
							}
							out := "TOOL_MARKER"
							if pressure {
								out += strings.Repeat("x", 100000)
							}
							return out, false, nil
						}
					}
					if phase == "steering" {
						opts.HasPendingSteering = func(context.Context) (bool, error) { return !claimed, nil }
						opts.OnLocalSteering = func(context.Context) (LocalSteeringInput, error) {
							if claimed {
								return LocalSteeringInput{}, nil
							}
							claimed = true
							return LocalSteeringInput{Text: "STEER_MARKER"}, nil
						}
					}
					_, err := client.SendAgentic(attachment.WithToolImages(context.Background()), "CURRENT_REQUEST", opts)
					if outcome == "compact_failure" {
						require.Error(t, err)
						want := 1
						if phase == "preturn" {
							want = 0
						}
						require.Equal(t, want, samples, "failure must not continue sampling")
					} else {
						require.NoError(t, err)
						want := 2
						if phase == "preturn" {
							want = 1
						}
						require.Equal(t, want, samples)
						if phase == "steering" {
							raw, _ := json.Marshal(persisted)
							require.Equal(t, 1, strings.Count(string(raw), "STEER_MARKER"), "steering must survive exactly once")
						}
					}
					wantCompactions := 0
					if pressure {
						wantCompactions = 1
					}
					require.Equal(t, wantCompactions, compactions)
					if phase == "inturn" {
						require.Equal(t, 1, tools, "compaction must not rerun the tool")
					}
				})
			}
		}
	}
}
