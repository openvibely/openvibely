package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func computerImageFixture(t *testing.T) string {
	t.Helper()
	raw, err := agenttools.EncodeImageResult([]byte(`{"content":[{"type":"image","mimeType":"image/png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a94UAAAAASUVORK5CYII="}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestComputerImagesReachResponsesRequest(t *testing.T) {
	count := 0
	found := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		body, _ := io.ReadAll(r.Body)
		if count == 2 {
			found = strings.Contains(string(body), `"input_image"`) && strings.Contains(string(body), `data:image/png;base64,iVBOR`)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			fmt.Fprint(w, `data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"screen-1","name":"computer","arguments":"{}"}}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"type":"response.output_text.delta","delta":"Seen"}`+"\n\n")
		}
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"response","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":20,"output_tokens":10}}}`+"\n\n")
	}))
	defer server.Close()
	old := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = server.URL + "/"
	defer func() { OpenAIAPIBaseURL = old }()
	raw := computerImageFixture(t)
	_, err := newHTTPTestAPIKeyClient("test").SendAgentic(t.Context(), "inspect", &AgenticOptions{Model: "gpt-5.3-codex", SkipDefaultTools: true, ToolOutputTokenLimit: 1, ExtraTools: []ToolDefinition{{Type: "function", Name: "computer", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) { return raw, false, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("model never received an image block")
	}
}
func TestComputerImagesReachCompletionsReplay(t *testing.T) {
	found := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		found = strings.Contains(string(body), `"image_url"`) && strings.Contains(string(body), `data:image/png;base64,iVBOR`)
		if strings.Contains(string(body), "computer-images-v1") {
			t.Error("encoded image envelope reached model as text")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Seen\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
	call := CompletionsToolCall{ID: "screen-1", Type: "function"}
	call.Function.Name = "computer"
	call.Function.Arguments = "{}"
	client.SetCompletionsHistory([]CompletionsHistoryMessage{{Role: "assistant", ToolCalls: []CompletionsToolCall{call}}, {Role: "tool", ToolCallID: "screen-1", Content: computerImageFixture(t)}})
	_, err := client.SendCompletions(t.Context(), "continue", &CompletionsOptions{DisableTools: true, ToolOutputTokenLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("screenshot lost during replay")
	}
}
