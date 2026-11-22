package geminiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
}

func TestSendAPIKeyToolLoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi there"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "key" {
			t.Errorf("missing api key header")
		}
		if !strings.HasSuffix(r.URL.Path, "/models/gemini-2.5-pro:streamGenerateContent") || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("unexpected path %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			sse(w,
				`{"candidates":[{"content":{"role":"model","parts":[{"text":"Reading. "}]}}]}`,
				`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"file_path":"hello.txt"}},"thoughtSignature":"sig-1"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`)
			return
		}
		sse(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":25},"modelVersion":"gemini-2.5-pro"}`)
	}))
	defer server.Close()

	client := NewWithAPIKey("key")
	client.SetBaseURL(server.URL)
	var streamed strings.Builder
	var saved [][]Content
	resp, err := client.Send(context.Background(), "read hello", &Options{
		Model: "gemini-2.5-pro", System: "be brief", WorkDir: dir, OnText: func(s string) { streamed.WriteString(s) },
		OnReplayChanged: func(_ context.Context, items []Content, _ int) error { saved = append(saved, items); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Reading. Done." || streamed.String() != resp.Text {
		t.Fatalf("text=%q streamed=%q", resp.Text, streamed.String())
	}
	if resp.InputTokens != 30 || resp.OutputTokens != 10 || resp.ReasoningTokens != 3 || resp.TotalTokens != 40 {
		t.Fatalf("usage %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Output != "hi there" && !strings.Contains(resp.ToolCalls[0].Output, "hi there") {
		t.Fatalf("tool calls %+v", resp.ToolCalls)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests=%d", len(bodies))
	}
	first := bodies[0]
	if first["systemInstruction"] == nil || first["tools"] == nil {
		t.Fatalf("first request missing system/tools: %v", first)
	}
	raw, _ := json.Marshal(bodies[1]["contents"])
	second := string(raw)
	if !strings.Contains(second, `"thoughtSignature":"sig-1"`) || !strings.Contains(second, `"functionResponse"`) || !strings.Contains(second, "hi there") {
		t.Fatalf("second request contents: %s", second)
	}
	if len(saved) == 0 || len(saved[len(saved)-1]) < 3 {
		t.Fatalf("replay not saved: %v", saved)
	}
	var callID, resultID string
	for _, c := range saved[len(saved)-1] {
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				callID = p.FunctionCall.ID
			}
			if p.FunctionResponse != nil {
				resultID = p.FunctionResponse.ID
			}
		}
	}
	if !strings.HasPrefix(callID, "gemini_call_") || resultID != callID {
		t.Fatalf("saved call id=%q result id=%q", callID, resultID)
	}
	if !strings.Contains(second, `"id":"`+callID+`"`) {
		t.Fatalf("second request missing call id %q: %s", callID, second)
	}
}

func TestSendOAuthCodeAssistWrapperAndUnauthorizedRecovery(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1internal:streamGenerateContent" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer old" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Model   string          `json:"model"`
			Project string          `json:"project"`
			Request json.RawMessage `json:"request"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "gemini-2.5-flash" || body.Project != "proj-1" || !strings.Contains(string(body.Request), `"contents"`) {
			t.Errorf("wrapper %+v", body)
		}
		sse(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`)
	}))
	defer server.Close()
	client := NewWithOAuth("old", "proj-1")
	client.SetCodeAssistURL(server.URL + "/v1internal")
	client.SetUnauthorizedHandler(func(_ context.Context, used string) (string, bool, error) {
		if used != "old" {
			t.Errorf("token used %q", used)
		}
		return "new", true, nil
	})
	resp, err := client.Send(context.Background(), "hi", &Options{Model: "gemini-2.5-flash", DisableTools: true, OmitExecutionEnvironment: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" || calls != 2 {
		t.Fatalf("text=%q calls=%d", resp.Text, calls)
	}
}

func TestToolFilterHidesAndDeniesTools(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Tools json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(raw, &body)
		if strings.Contains(string(body.Tools), `"name":"shell"`) || !strings.Contains(string(body.Tools), `"read_file"`) {
			t.Errorf("filtered tool advertised")
		}
		if requests == 1 {
			sse(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"shell","args":{"command":"touch x"}}}]},"finishReason":"STOP"}]}`)
			return
		}
		if !strings.Contains(string(raw), "not allowed") {
			t.Errorf("denial not returned: %s", raw)
		}
		sse(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	client := NewWithAPIKey("key")
	client.SetBaseURL(server.URL)
	_, err := client.Send(context.Background(), "go", &Options{Model: "m", WorkDir: dir, ToolFilter: func(name string) bool { return name != "shell" }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Fatal("denied tool executed")
	}
}

func TestWireContentsAddsPlaceholderSignatureAndMergesRoles(t *testing.T) {
	out := wireContents([]Content{
		TextContent("user", "a"), TextContent("user", "b"),
		{Role: "model", Parts: []Part{{FunctionCall: &FunctionCall{Name: "x"}}, {FunctionCall: &FunctionCall{Name: "y"}}}},
	})
	if len(out) != 2 || len(out[0].Parts) != 2 {
		t.Fatalf("merge: %+v", out)
	}
	if out[1].Parts[0].ThoughtSignature != SkipThoughtSignature || out[1].Parts[1].ThoughtSignature != "" {
		t.Fatalf("signatures: %+v", out[1].Parts)
	}
}

func TestMaxTokensFinishReasonAndAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"The input token count exceeds the maximum number of tokens allowed"}}`))
			return
		}
		sse(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}]}`)
	}))
	defer server.Close()
	client := NewWithAPIKey("key")
	client.SetBaseURL(server.URL)
	resp, err := client.Send(context.Background(), "x", &Options{Model: "m", DisableTools: true, OmitExecutionEnvironment: true})
	if err != nil || resp.StopReason != "MAX_TOKENS" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	_, err = client.Send(context.Background(), "x", &Options{Model: "bad", DisableTools: true, OmitExecutionEnvironment: true})
	if err == nil || !strings.Contains(err.Error(), "context_window_exceeded") {
		t.Fatalf("err=%v", err)
	}
}

func TestEarlyStopFinishReasons(t *testing.T) {
	cases := []struct {
		name, event, wantText, wantErr string
	}{
		{"safety with text keeps text and notes reason",
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"SAFETY"}]}`,
			"partial\n\n[Gemini stopped the response early: SAFETY]", ""},
		{"safety without text errors",
			`{"candidates":[{"finishReason":"SAFETY"}]}`, "", "SAFETY"},
		{"thought-only safety errors",
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"hmm","thought":true}]},"finishReason":"SAFETY"}]}`, "", "SAFETY"},
		{"malformed call with text errors",
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`, "", "MALFORMED_FUNCTION_CALL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				sse(w, tc.event)
			}))
			defer server.Close()
			client := NewWithAPIKey("key")
			client.SetBaseURL(server.URL)
			var streamed strings.Builder
			resp, err := client.Send(context.Background(), "x", &Options{Model: "m", DisableTools: true, OmitExecutionEnvironment: true, OnText: func(s string) { streamed.WriteString(s) }})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v resp=%+v", err, resp)
				}
				return
			}
			if err != nil || resp.Text != tc.wantText || streamed.String() != tc.wantText {
				t.Fatalf("err=%v text=%q streamed=%q", err, resp.Text, streamed.String())
			}
		})
	}
}

func TestRefreshInvalidGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") == "dead" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"a","expires_in":3600}`))
	}))
	defer server.Close()
	old := OAuthTokenURL
	OAuthTokenURL = server.URL
	defer func() { OAuthTokenURL = old }()
	if _, err := RefreshTokenContext(context.Background(), "dead"); err == nil || !strings.Contains(err.Error(), "reauthentication") {
		t.Fatalf("err=%v", err)
	}
	tokens, err := RefreshTokenContext(context.Background(), "live")
	if err != nil || tokens.AccessToken != "a" || tokens.RefreshToken != "live" {
		t.Fatalf("tokens=%+v err=%v", tokens, err)
	}
}

func TestSetupCodeAssistFreeTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`))
		case "/v1internal:onboardUser":
			_, _ = w.Write([]byte(`{"done":true,"response":{"cloudaicompanionProject":{"id":"auto-proj"}}}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	old := CodeAssistBaseURL
	CodeAssistBaseURL = server.URL + "/v1internal"
	defer func() { CodeAssistBaseURL = old }()
	project, err := SetupCodeAssist(context.Background(), "tok", "")
	if err != nil || project != "auto-proj" {
		t.Fatalf("project=%q err=%v", project, err)
	}
}

func TestRefreshFallsBackToBuiltInClient(t *testing.T) {
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "hosted-id")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "hosted-secret")
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		seen = append(seen, r.Form.Get("client_id"))
		if r.Form.Get("client_id") == "hosted-id" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized_client"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"a","expires_in":3600}`))
	}))
	defer server.Close()
	old := OAuthTokenURL
	OAuthTokenURL = server.URL
	defer func() { OAuthTokenURL = old }()
	tokens, err := RefreshTokenContext(context.Background(), "r")
	if err != nil || tokens.AccessToken != "a" {
		t.Fatalf("tokens=%+v err=%v", tokens, err)
	}
	if len(seen) != 2 || seen[0] != "hosted-id" || seen[1] != OAuthClientID {
		t.Fatalf("clients tried = %v", seen)
	}
}
