package anthropicclient

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

func TestViewImageNextRequestIsMultimodalAndBudgeted(t *testing.T) {
	image := []byte(strings.Repeat("image bytes", 100000))
	encoded := base64.StdEncoding.EncodeToString(image)
	turns := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		turns++
		if turns == 2 && (!strings.Contains(string(body), encoded) || !strings.Contains(string(body), `"type":"image"`)) {
			t.Error("tool result did not contain image bytes")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{`{"type":"message_start","message":{"id":"msg","model":"test","usage":{"input_tokens":20}}}`}
		if turns == 1 {
			events = append(events,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call1","name":"view_image"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8}}`,
			)
		} else {
			events = append(events,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			)
		}
		events = append(events, `{"type":"message_stop"}`)
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = old }()
	ctx := attachment.WithToolImages(context.Background())
	_, err := NewWithAPIKey("test").SendAgentic(ctx, "inspect", &AgenticOptions{Model: "test", ContextWindow: 32768, MaxTokens: 1000, SkipDefaultTools: true,
		ExtraTools: []ToolDefinition{{Name: "view_image", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}},
		ToolExecutor: func(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
			out, err := attachment.StoreToolImage(ctx, attachment.ToolImage{Name: "page.png", MediaType: "image/png", Data: image})
			return out, false, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("turns=%d", turns)
	}
}
