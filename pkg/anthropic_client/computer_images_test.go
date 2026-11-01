package anthropicclient

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

func TestComputerImagesReachAnthropicRequest(t *testing.T) {
	count := 0
	found := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		body, _ := io.ReadAll(r.Body)
		if count == 2 {
			found = strings.Contains(string(body), `"type":"image"`) && strings.Contains(string(body), `"media_type":"image/png"`)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg","model":"claude-sonnet-4-6","usage":{"input_tokens":10}}}`+"\n\n")
		if count == 1 {
			fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"screen-1","name":"computer"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`+"\n\n")
		}
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = old }()
	raw := computerImageFixture(t)
	_, err := NewWithAPIKey("test").SendAgentic(t.Context(), "inspect", &AgenticOptions{Model: "claude-sonnet-4-6", MaxTokens: 1024, SkipDefaultTools: true, ToolOutputTokenLimit: 1, ExtraTools: []ToolDefinition{{Name: "computer", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) { return raw, false, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("model never received an image block")
	}
}
