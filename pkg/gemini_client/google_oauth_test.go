package geminiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/llm/streamdecode"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleAccountRefreshKeepsIssuer(t *testing.T) {
	for _, client := range []OAuthClient{BuiltInOAuthClient(), BusinessOAuthClient()} {
		t.Run(client.ID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.Form.Get("client_id") != client.ID || r.Form.Get("refresh_token") != "original" {
					t.Error("wrong issuer or encoded token sent to Google")
				}
				fmt.Fprint(w, `{"access_token":"new","refresh_token":"rotated","expires_in":3600}`)
			}))
			defer server.Close()
			old := OAuthTokenURL
			OAuthTokenURL = server.URL
			defer func() { OAuthTokenURL = old }()
			tokens, err := RefreshTokenContext(context.Background(), encodeRefreshCredential(client.ID, "original"))
			if err != nil {
				t.Fatal(err)
			}
			if tokens.RefreshToken != encodeRefreshCredential(client.ID, "rotated") {
				t.Fatal("lost issuer on rotation")
			}
		})
	}
}

func TestGoogleAccountContinuation(t *testing.T) {
	requests := 0
	firstID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		req := body["request"].(map[string]any)
		if requests == 1 {
			firstID = body["requestId"].(string)
			fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"first\"}]},\"finishReason\":\"MAX_TOKENS\",\"continuationToken\":\"dG9rZW4=\"}]}}\n\n")
		} else {
			if req["continuationToken"] != "dG9rZW4=" || body["requestId"] == firstID {
				t.Error("bad continuation envelope")
			}
			for _, key := range []string{"contents", "tools", "systemInstruction", "generationConfig"} {
				if _, ok := req[key]; ok {
					t.Errorf("continuation resends %s", key)
				}
			}
			fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"second\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
		}
	}))
	defer server.Close()
	c := NewWithOAuth("token", "project")
	c.SetCodeAssistURL(server.URL + "/v1internal")
	opts := &Options{Model: "gemini-4-neon", ReasoningEffort: "high", System: "system"}
	body, err := c.buildRequestBody([]Content{TextContent("user", "hello")}, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	var partial streamdecode.Text
	result, err := c.sendTurn(context.Background(), body, opts, &partial)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || result.text != "firstsecond" || partial.String() != result.text || result.finishReason != "STOP" {
		t.Fatalf("requests=%d result=%+v partial=%s", requests, result, partial.String())
	}
}

func TestStreamedArguments(t *testing.T) {
	stream := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"id":"call","name":"write","partialArgs":[{"jsonPath":"$.text","stringValue":"hel","willContinue":true}],"willContinue":true}}]}}]}

data: {"candidates":[{"content":{"parts":[{"functionCall":{"id":"call","partialArgs":[{"jsonPath":"$.text","stringValue":"lo"}]}}]},"finishReason":"STOP"}]}

`
	result, err := parseStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := result.calls()
	if len(calls) != 1 || string(calls[0].Args) != `{"text":"hello"}` || calls[0].Name != "write" {
		t.Fatalf("calls=%+v", calls)
	}
	incomplete := strings.Split(stream, "\n\ndata:")[0] + "\n\ndata: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"
	if _, err := parseStream(strings.NewReader(incomplete), nil, nil); err == nil {
		t.Fatal("accepted incomplete tool call")
	}
}

func TestGeminiReasoningRequests(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		c := NewWithAPIKey("key")
		if oauth {
			c = NewWithOAuth("token", "project")
		}
		body, err := c.buildRequestBody(nil, nil, &Options{Model: "gemini-4-hydrogen", ReasoningEffort: "xhigh"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thinkingLevel":"XHIGH"`) {
			t.Fatalf("body=%s", body)
		}
	}
	if _, err := thinkingLevel("gemini-4-carbon", "xhigh"); err == nil {
		t.Fatal("accepted unsupported effort")
	}
}

func TestContinuationPreservesToolAssemblyAndBounds(t *testing.T) {
	for _, mode := range []string{"complete", "repeat", "limit", "error"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"id":"c","name":"write","partialArgs":[{"jsonPath":"$.text","stringValue":"hello","willContinue":true}],"willContinue":true}}]},"finishReason":"MAX_TOKENS","continuationToken":"YWJj"}]}`+"\n\n")
					return
				}
				switch mode {
				case "complete":
					fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"id":"c","partialArgs":[{"jsonPath":"$.text","stringValue":" world"}]}}]},"finishReason":"STOP"}]}`+"\n\n")
				case "repeat":
					fmt.Fprint(w, `data: {"candidates":[{"finishReason":"MAX_TOKENS","continuationToken":"YWJj"}]}`+"\n\n")
				case "limit":
					fmt.Fprint(w, `data: {"candidates":[{"finishReason":"MAX_TOKENS","continuationToken":"ZGVm"}]}`+"\n\n")
				case "error":
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			c := NewWithOAuth("token", "project")
			c.SetCodeAssistURL(server.URL + "/v1internal")
			opts := &Options{Model: "gemini-4-neon", MaxContinuationRequests: 1}
			body, err := c.buildRequestBody(nil, nil, opts)
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.sendTurn(context.Background(), body, opts, nil)
			if mode == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				calls := result.calls()
				if len(calls) != 1 || string(calls[0].Args) != `{"text":"hello world"}` {
					t.Fatalf("calls=%+v", calls)
				}
			} else if err == nil {
				t.Fatal("expected bounded failure")
			}
			if requests != 2 {
				t.Fatalf("requests=%d", requests)
			}
		})
	}
}

func TestNestedToolMediaClone(t *testing.T) {
	response := &FunctionResponse{
		Name:     "screenshot",
		Response: map[string]any{"items": []any{map[string]any{"ok": true}}},
		Parts:    []FunctionResponsePart{{InlineData: &InlineData{MimeType: "image/png", Data: "abc"}}},
	}
	original := []Content{{Role: "user", Parts: []Part{{FunctionResponse: response}}}}
	copy := CloneContents(original)
	copy[0].Parts[0].FunctionResponse.Parts[0].InlineData.Data = "changed"
	copy[0].Parts[0].FunctionResponse.Response["items"].([]any)[0].(map[string]any)["ok"] = false
	if original[0].Parts[0].FunctionResponse.Parts[0].InlineData.Data != "abc" || original[0].Parts[0].FunctionResponse.Response["items"].([]any)[0].(map[string]any)["ok"] != true {
		t.Fatal("clone aliases nested response")
	}
}

func TestGoogleAccountOnboardingMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["metadata"].(map[string]any)["ideType"] != "\x41\x4e\x54\x49\x47\x52\x41\x56\x49\x54\x59" {
			t.Error("lost Google onboarding identity")
		}
		if strings.HasSuffix(r.URL.Path, ":loadCodeAssist") {
			fmt.Fprint(w, `{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`)
		} else {
			fmt.Fprint(w, `{"done":true,"response":{"cloudaicompanionProject":{"id":"project"}}}`)
		}
	}))
	defer server.Close()
	old := CodeAssistBaseURL
	CodeAssistBaseURL = server.URL + "/v1internal"
	defer func() { CodeAssistBaseURL = old }()
	project, err := SetupGoogleAccountWithCredential(context.Background(), "token", "", "")
	if err != nil || project != "project" {
		t.Fatalf("project=%q err=%v", project, err)
	}
}

func TestLegacyOAuthEnvelope(t *testing.T) {
	if !UsesLegacyOAuth("old-refresh") || !UsesLegacyOAuth(encodeRefreshCredential(LegacyOAuthClientID, "old-refresh")) || UsesLegacyOAuth(encodeRefreshCredential(OAuthClientID, "new-refresh")) {
		t.Fatal("incorrect credential classification")
	}
	c := NewWithOAuth("token", "project")
	c.SetLegacyOAuth(true)
	body, err := c.buildRequestBody(nil, nil, &Options{Model: "gemini-3.8-flash"})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 3 || envelope["model"] != "gemini-3.8-flash" {
		t.Fatalf("legacy envelope=%s", body)
	}
}

func TestBrokenStreamRetriesTurn(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			sse(w, `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`)
			return
		}
		sse(w, `{"candidates":[{"content":{"parts":[{"text":"complete"}]},"finishReason":"STOP"}]}`)
	}))
	defer server.Close()
	c := NewWithAPIKey("key")
	c.SetBaseURL(server.URL)
	var output strings.Builder
	resp, err := c.Send(context.Background(), "hello", &Options{Model: "gemini-4-neon", DisableTools: true, OmitExecutionEnvironment: true, OnText: func(s string) { output.WriteString(s) }})
	if err != nil || requests != 2 || resp.Text != "complete" || output.String() != "partialcomplete" {
		t.Fatalf("err=%v requests=%d response=%+v output=%q", err, requests, resp, output.String())
	}
}

func parseStream(body io.Reader, onText func(string), partial *streamdecode.Text) (*turnResult, error) {
	result := &turnResult{}
	assembler := &callAssembler{}
	if err := parseStreamInto(body, onText, partial, result, assembler); err != nil {
		return nil, err
	}
	if len(assembler.pending) > 0 {
		return nil, fmt.Errorf("gemini incomplete streamed function arguments")
	}
	return result, nil
}
