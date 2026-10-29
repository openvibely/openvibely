package openaiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func imageTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 220, G: 180, B: 110, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGenerateImageCodexRequest(t *testing.T) {
	data := imageTestPNG(t)
	encoded := base64.StdEncoding.EncodeToString(data)
	for _, oauth := range []bool{false, true} {
		for _, edit := range []bool{false, true} {
			name := "api-key"
			if oauth {
				name = "oauth"
			}
			if edit {
				name += "/edit"
			} else {
				name += "/generate"
			}
			t.Run(name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					operation := "generations"
					if edit {
						operation = "edits"
					}
					if r.Method != "POST" || r.URL.Path != "/base/images/"+operation {
						t.Errorf("request = %s %s", r.Method, r.URL.Path)
					}
					if oauth && r.URL.Query().Get("client_version") != "0.0.0" {
						t.Error("missing Codex client_version")
					}
					if !oauth && r.URL.RawQuery != "" {
						t.Error("unexpected API-key query")
					}
					if r.Header.Get("Authorization") == "" {
						t.Error("missing auth")
					}
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					for key, expected := range map[string]any{"model": "gpt-image-2", "prompt": "A goat", "background": "auto", "quality": "auto", "size": "auto"} {
						if payload[key] != expected {
							t.Errorf("%s = %v, want %v", key, payload[key], expected)
						}
					}
					if edit {
						images, _ := payload["images"].([]any)
						if len(images) != 1 {
							t.Errorf("images = %#v", images)
							return
						}
						if images[0].(map[string]any)["image_url"] != "data:image/png;base64,"+encoded {
							t.Error("reference changed")
						}
					} else if _, exists := payload["images"]; exists {
						t.Error("unexpected edit references")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": encoded}}})
				}))
				defer srv.Close()
				oldAPI, oldOAuth := OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL
				OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = srv.URL+"/base", srv.URL+"/base"
				defer func() { OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = oldAPI, oldOAuth }()
				client := NewWithAPIKey("key")
				if oauth {
					client = NewWithOAuthToken("token", "refresh", time.Now().Add(24*time.Hour).UnixMilli(), "account")
				}
				var refs []string
				if edit {
					refs = []string{"data:image/png;base64," + encoded}
				}
				got, err := client.GenerateImage(context.Background(), "A goat", refs)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, data) {
					t.Error("generated PNG changed")
				}
			})
		}
	}
}

func TestGenerateImageRejectsBadResponses(t *testing.T) {
	for name, body := range map[string]string{
		"empty":        `{"data":[]}`,
		"missing data": `{}`,
		"url only":     `{"data":[{"url":"https://example.com/image.png"}]}`,
		"base64":       `{"data":[{"b64_json":"not base64"}]}`,
		"not image":    `{"data":[{"b64_json":"aGVsbG8="}]}`,
		"malformed":    `{`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			old := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = srv.URL
			defer func() { OpenAIAPIBaseURL = old }()
			if _, err := NewWithAPIKey("key").GenerateImage(context.Background(), "Goat", nil); err == nil {
				t.Fatal("expected invalid image error")
			}
		})
	}
}

func TestGenerateImageOAuthRecovery(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(imageTestPNG(t))
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") == "Bearer expired" {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fresh" {
			t.Error("missing refreshed auth")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": encoded}}})
	}))
	defer srv.Close()
	old := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = old }()
	client := NewWithOAuthToken("expired", "refresh", time.Now().Add(24*time.Hour).UnixMilli(), "account")
	client.SetOAuthUnauthorizedHandler(func(ctx context.Context, token string) (OAuthTokens, bool, error) {
		return OAuthTokens{AccessToken: "fresh"}, true, nil
	})
	if _, err := client.GenerateImage(context.Background(), "Goat", nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestGenerateImageInvalidInputAndCancellation(t *testing.T) {
	client := NewWithAPIKey("key")
	for _, tc := range []struct {
		prompt string
		refs   []string
	}{
		{" ", nil}, {"goat", []string{"https://example.com/image.png"}},
		{"goat", []string{"data:image/png;base64,AAAA"}},
		{"goat", make([]string, 6)},
	} {
		if _, err := client.GenerateImage(context.Background(), tc.prompt, tc.refs); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled request was sent") }))
	defer srv.Close()
	old := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL
	defer func() { OpenAIAPIBaseURL = old }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.GenerateImage(ctx, "Goat", nil); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled request: %v", err)
	}
}
