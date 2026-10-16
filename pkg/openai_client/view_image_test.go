package openaiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/attachment"
)

func TestViewImageProviderRequests(t *testing.T) {
	for _, window := range []int{32768, 2500} {
		for _, transport := range []string{"responses", "completions"} {
			t.Run(fmt.Sprintf("%s/%d", transport, window), func(t *testing.T) {
				image := []byte(strings.Repeat("image bytes", 100000))
				encoded := base64.StdEncoding.EncodeToString(image)
				turns := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					turns++
					if turns == 2 && !strings.Contains(string(body), "data:image/png;base64,"+encoded) {
						t.Error("next request is missing actual image input")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if transport == "responses" {
						if turns == 1 {
							io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"view_image\",\"arguments\":\"{}\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\"}}\n\n")
						} else {
							io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r2\",\"status\":\"completed\"}}\n\n")
						}
					} else {
						if turns == 1 {
							io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"view_image\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
						} else {
							io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						}
					}
				}))
				defer server.Close()
				old := OpenAIAPIBaseURL
				OpenAIAPIBaseURL = server.URL + "/"
				defer func() { OpenAIAPIBaseURL = old }()
				ctx := attachment.WithToolImages(context.Background())
				execute := func(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
					out, err := attachment.StoreToolImage(ctx, attachment.ToolImage{Name: "page.png", MediaType: "image/png", Data: image})
					return out, false, err
				}
				tools := []ToolDefinition{{Type: "function", Name: "view_image", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
				var err error
				if transport == "responses" {
					_, err = newHTTPTestAPIKeyClient("test").SendAgentic(ctx, "inspect", &AgenticOptions{Model: "gpt-test", ContextWindow: window, MaxOutputTokens: 1000, SkipDefaultTools: true, ExtraTools: tools, ToolExecutor: execute})
				} else {
					client := NewWithCompatibleAPIKey("test", server.URL, "", "")
					_, err = client.SendCompletions(ctx, "inspect", &CompletionsOptions{Model: "test", ContextWindow: window, MaxOutputTokens: 1000, SkipDefaultTools: true, ExtraTools: tools, ToolExecutor: execute})
					if err == nil {
						history := client.LastCompletionsTranscript()
						raw, _ := json.Marshal(history)
						if !strings.Contains(string(raw), encoded) {
							t.Error("image missing from persisted transcript")
						}
					}
				}
				if window == 2500 {
					if err == nil || turns != 1 {
						t.Fatalf("oversized image must fail before second request: turns=%d err=%v", turns, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if turns != 2 {
					t.Fatalf("turns = %d", turns)
				}
			})
		}
	}

}
