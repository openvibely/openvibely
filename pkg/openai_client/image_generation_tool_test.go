package openaiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/pkg/attachment"
)

func TestSendAgenticImageGenerationAndEdit(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		name := "api-key"
		if oauth {
			name = "oauth"
		}
		t.Run(name, func(t *testing.T) {
			data := imageTestPNG(t)
			encoded := base64.StdEncoding.EncodeToString(data)
			var imageCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/images/") {
					call := imageCalls.Add(1)
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					if call == 1 && !strings.HasSuffix(r.URL.Path, "/images/generations") {
						t.Error("expected generation endpoint")
					}
					if call == 2 {
						if !strings.HasSuffix(r.URL.Path, "/images/edits") {
							t.Error("expected edit endpoint")
						}
						refs, _ := payload["images"].([]any)
						if len(refs) != 1 {
							t.Errorf("edit references = %#v", refs)
							return
						}
						if refs[0].(map[string]any)["image_url"] != "data:image/png;base64,"+encoded {
							t.Error("edit did not reuse generated image")
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": encoded}}})
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/responses") {
					http.NotFound(w, r)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close(websocket.StatusNormalClosure, "")
				for turn := 0; turn < 3; turn++ {
					_, raw, err := conn.Read(r.Context())
					if err != nil {
						t.Error(err)
						return
					}
					var request map[string]any
					if err := json.Unmarshal(raw, &request); err != nil {
						t.Error(err)
						return
					}
					input, _ := request["input"].([]any)
					if turn == 0 {
						additional := input[0].(map[string]any)
						tools, _ := additional["tools"].([]any)
						found := false
						for _, rawTool := range tools {
							tool := rawTool.(map[string]any)
							if tool["type"] == "namespace" && tool["name"] == "image_gen" {
								children := tool["tools"].([]any)
								found = children[0].(map[string]any)["name"] == "imagegen"
							}
						}
						if !found {
							t.Error("missing image_gen.imagegen")
						}
					} else {
						found := false
						for _, rawItem := range input {
							item := rawItem.(map[string]any)
							if item["type"] != "function_call_output" {
								continue
							}
							blocks, _ := item["output"].([]any)
							if len(blocks) != 2 {
								t.Errorf("expected text and image output, got %#v", item)
								continue
							}
							text := blocks[0].(map[string]any)["text"].(string)
							image := blocks[1].(map[string]any)
							if !strings.Contains(text, "/files/generated-") || strings.Contains(text, encoded) {
								t.Error("invalid artifact metadata")
							}
							if image["image_url"] != "data:image/png;base64,"+encoded {
								t.Error("missing generated image content")
							}
							found = true
						}
						if !found {
							t.Error("missing image tool result")
						}
					}
					var output []any
					if turn < 2 {
						args := `{"prompt":"A goat"}`
						if turn == 1 {
							args = `{"prompt":"Give the goat a hat","num_last_images_to_include":1}`
						}
						output = []any{map[string]any{"type": "function_call", "call_id": fmt.Sprintf("image-%d", turn), "namespace": "image_gen", "name": "imagegen", "arguments": args}}
					} else {
						output = []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "![Goat](/files/generated-2)"}}}}
					}
					if turn == 2 {
						delta, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": "![Goat](/files/generated-2)"})
						if err := conn.Write(r.Context(), websocket.MessageText, delta); err != nil {
							t.Error(err)
							return
						}
					}
					for index, item := range output {
						event, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
						if err := conn.Write(r.Context(), websocket.MessageText, event); err != nil {
							t.Error(err)
							return
						}
					}
					completed, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp-%d", turn), "status": "completed", "model": "gpt-6.1-sol"}})
					if err := conn.Write(r.Context(), websocket.MessageText, completed); err != nil {
						t.Error(err)
						return
					}
				}
			}))
			defer srv.Close()
			oldAPI, oldOAuth := OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL
			OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = srv.URL+"/v1", srv.URL+"/codex"
			defer func() { OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = oldAPI, oldOAuth }()
			client := NewWithAPIKey("key")
			if oauth {
				client = NewWithOAuthToken("token", "refresh", time.Now().Add(24*time.Hour).UnixMilli(), "account")
			}
			root := t.TempDir()
			var temporaryPaths []string
			resp, err := client.SendAgentic(context.Background(), "Generate a goat and put a hat on it", &AgenticOptions{
				Model: "gpt-6.1-sol", MaxTurns: 3,
				ToolFilter: func(name string) bool { return name == "publish_file" },
				PublishGeneratedImage: func(ctx context.Context, path string) (string, error) {
					temporaryPaths = append(temporaryPaths, path)
					pngData, err := os.ReadFile(path)
					if err != nil {
						return "", err
					}
					if !bytes.Equal(pngData, data) {
						t.Error("publisher received different image")
					}
					if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%d.png", len(temporaryPaths))), pngData, 0600); err != nil {
						return "", err
					}
					return fmt.Sprintf(`{"id":"generated-%d","url":"/files/generated-%d"}`, len(temporaryPaths), len(temporaryPaths)), nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if imageCalls.Load() != 2 || len(temporaryPaths) != 2 || len(resp.ToolCalls) != 2 {
				t.Fatalf("calls=%d publications=%d tool calls=%#v", imageCalls.Load(), len(temporaryPaths), resp.ToolCalls)
			}
			for _, call := range resp.ToolCalls {
				if call.Error || strings.Contains(call.Output, encoded) {
					t.Fatalf("bad tool result: %#v", call)
				}
			}
			for _, path := range temporaryPaths {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("temporary image not removed: %s", path)
				}
			}
			if !strings.Contains(resp.Text, "/files/generated-2") {
				t.Errorf("final reply: %s", resp.Text)
			}
		})
	}
}

func TestImageGenerationPermissionAndInvalidReferences(t *testing.T) {
	publisher := func(context.Context, string) (string, error) { t.Error("unexpected publication"); return "", nil }
	for _, opts := range []*AgenticOptions{
		{}, {PublishGeneratedImage: publisher, DisableTools: true},
		{PublishGeneratedImage: publisher, SkipDefaultTools: true},
		{PublishGeneratedImage: publisher, ToolFilter: func(string) bool { return false }},
	} {
		if imageGenerationAllowed(opts) {
			t.Error("image generation allowed without permission")
		}
		if openAIToolAllowed(opts, "image_gen") || openAIToolAllowed(opts, imageGenerationToolName) {
			t.Error("namespace or execution permission bypass")
		}
		_, failed, err := NewWithAPIKey("key").runImageGenerationTool(context.Background(), opts, nil, json.RawMessage(`{"prompt":"goat"}`))
		if !failed || err == nil {
			t.Error("unauthorized generation accepted")
		}
	}
	opts := &AgenticOptions{PublishGeneratedImage: publisher, WorkDir: t.TempDir()}
	for _, input := range []string{
		`{"prompt":""}`, `{"prompt":"goat","num_last_images_to_include":0}`,
		`{"prompt":"goat","num_last_images_to_include":6}`,
		`{"prompt":"goat","num_last_images_to_include":1}`,
		`{"prompt":"goat","referenced_image_paths":["missing.png"]}`,
		`{"prompt":"goat","referenced_image_paths":["a.png"],"num_last_images_to_include":1}`,
		`{"prompt":"goat","unexpected":true}`,
	} {
		_, failed, err := NewWithAPIKey("key").runImageGenerationTool(attachment.WithToolImages(context.Background()), opts, nil, json.RawMessage(input))
		if !failed || err == nil {
			t.Errorf("invalid input accepted: %s", input)
		}
	}
}

func TestImageGenerationLocalReferenceAndPublicationFailure(t *testing.T) {
	data := imageTestPNG(t)
	encoded := base64.StdEncoding.EncodeToString(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/edits" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var payload struct {
			Images []struct {
				URL string `json:"image_url"`
			} `json:"images"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.Images) != 1 || payload.Images[0].URL != "data:image/png;base64,"+encoded {
			t.Errorf("references = %#v", payload.Images)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": encoded}}})
	}))
	defer srv.Close()
	old := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL
	defer func() { OpenAIAPIBaseURL = old }()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "goat.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var temporaryPath string
	opts := &AgenticOptions{WorkDir: root, PublishGeneratedImage: func(_ context.Context, path string) (string, error) {
		temporaryPath = path
		if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, data) {
			t.Error("missing generated PNG")
		}
		return "", fmt.Errorf("publication revoked")
	}}
	_, failed, err := NewWithAPIKey("key").runImageGenerationTool(attachment.WithToolImages(context.Background()), opts, nil, json.RawMessage(`{"prompt":"Add a hat","referenced_image_paths":["goat.png"]}`))
	if !failed || err == nil || !strings.Contains(err.Error(), "publication revoked") {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
	if temporaryPath == "" {
		t.Fatal("publisher was not called")
	}
	if _, err := os.Stat(temporaryPath); !os.IsNotExist(err) {
		t.Error("temporary image not removed after publication failure")
	}
}

func TestRecentImageReferencesAfterHistoryRoundTrip(t *testing.T) {
	items := []any{
		map[string]any{"type": "message", "content": []any{map[string]any{"type": "input_image", "image_url": "first"}}},
		map[string]any{"type": "function_call", "arguments": `{"image_url":"not an image"}`},
		map[string]any{"type": "function_call_output", "output": []any{map[string]any{"type": "input_text", "text": "metadata"}, map[string]any{"type": "input_image", "image_url": "second"}}},
		map[string]any{"type": "message", "content": []any{map[string]any{"type": "input_image", "image_url": "third"}}},
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var restored []any
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	refs := recentImageReferences(restored, 2)
	if len(refs) != 2 || refs[0] != "second" || refs[1] != "third" {
		t.Fatalf("references = %#v", refs)
	}
}
