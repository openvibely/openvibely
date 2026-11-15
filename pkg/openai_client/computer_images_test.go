package openaiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"image"
	"image/png"
	"io"
	"math/rand"
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

// Use an incompressible PNG so a text-based estimate would exceed the context
// budget. This exercises the real computer envelope, not a hand-built image block.
func largeComputerImageFixture(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)), 255
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(agenttools.ImageResult{Content: []agenttools.ToolImage{{Type: "image", MIME: "image/png", Data: base64.StdEncoding.EncodeToString(data.Bytes())}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agenttools.EncodeImageResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestComputerImageTokenEstimateMatchesReload(t *testing.T) {
	for name, output := range map[string]string{"small": computerImageFixture(t), "large": largeComputerImageFixture(t)} {
		t.Run(name, func(t *testing.T) {
			item := agenticInputItem{"type": "function_call_output", "call_id": "screen", "output": agenttools.ResponsesImageOutput(output)}
			encoded, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var reloaded agenticInputItem
			if err := json.Unmarshal(encoded, &reloaded); err != nil {
				t.Fatal(err)
			}
			fresh, restored := estimateInputItemsTokens([]any{item}), estimateInputItemsTokens([]any{reloaded})
			if fresh != restored {
				t.Errorf("fresh screenshot estimate %d differs from reloaded estimate %d", fresh, restored)
			}
			if fresh < 1800 || fresh > 2200 {
				t.Errorf("screenshot estimated at %d tokens; expected image budget", fresh)
			}
			// Text beside an image must still count in full.
			blocks := item["output"].([]map[string]any)
			blocks = append(blocks, map[string]any{"type": "input_text", "text": strings.Repeat("x", 100000)})
			item["output"] = blocks
			if got := estimateInputItemsTokens([]any{item}); got < fresh+25000 {
				t.Errorf("ordinary text discounted: %d tokens", got)
			}
		})
	}
}

func TestComputerImagesReachResponsesRequest(t *testing.T) {
	count := 0
	found := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses/compact") {
			t.Error("computer screenshot alone triggered compaction")
			http.Error(w, "unexpected compaction", http.StatusBadRequest)
			return
		}
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
	raw := largeComputerImageFixture(t)
	_, err := newHTTPTestAPIKeyClient("test").SendAgentic(t.Context(), "inspect", &AgenticOptions{Model: "gpt-5.3-codex", AutoCompaction: true, CompactionTokenThreshold: 20000, ContextWindow: 32000, MaxOutputTokens: 4096, SkipDefaultTools: true, ToolOutputTokenLimit: 1, ExtraTools: []ToolDefinition{{Type: "function", Name: "computer", Parameters: json.RawMessage(`{"type":"object"}`)}}, ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) { return raw, false, nil }})
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

// All Responses image producers use the same traversal for representation
// fallbacks, while retaining the Responses-specific payload token estimate.
func TestResponsesImageSourcesMatchReloadedBudget(t *testing.T) {
	output := largeComputerImageFixture(t)
	image, ok := agenttools.DecodeImageResult(output)
	if !ok {
		t.Fatal("invalid fixture")
	}
	block := map[string]any{"type": "input_image", "detail": "auto", "image_url": "data:image/png;base64," + image.Content[0].Data}
	for _, source := range []struct {
		name, itemType, field string
		content               any
	}{
		{"computer", "function_call_output", "output", agenttools.ResponsesImageOutput(output)},
		{"view_image", "message", "content", []any{map[string]any{"type": "input_text", "text": "Image returned by view_image"}, block}},
		{"generated image", "function_call_output", "output", []any{map[string]any{"type": "input_text", "text": "Generated image"}, block}},
		{"attachment", "message", "content", []any{block}},
	} {
		t.Run(source.name, func(t *testing.T) {
			item := map[string]any{"type": source.itemType, source.field: source.content}
			encoded, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var restored map[string]any
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			want := estimateInputItemsTokens([]any{restored})
			rawContent, _ := json.Marshal(source.content)
			for name, content := range map[string]any{"fresh": source.content, "raw": json.RawMessage(rawContent)} {
				item[source.field] = content
				if got := estimateInputItemsTokens([]any{item}); got != want || got > 2200 {
					t.Errorf("%s: estimate=%d restored=%d", name, got, want)
				}
			}
		})
	}
}
