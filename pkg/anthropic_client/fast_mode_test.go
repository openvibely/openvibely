package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFastModeRequests(t *testing.T) {
	for _, model := range []string{"claude-opus-4-8", "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-4-7"} {
		for _, tier := range []string{"priority", "default", "", "invalid"} {
			for _, oauth := range []bool{false, true} {
				for _, mode := range []string{"send", "attachment", "agentic"} {
					t.Run(fmt.Sprintf("%s/%s/oauth=%t/%s", model, tier, oauth, mode), func(t *testing.T) {
						client := NewWithAPIKey("fixture-key")
						if oauth {
							client = NewWithOAuthToken("fixture-token", "", time.Now().Add(time.Hour).Unix())
						}
						calls := 0
						client.httpClient = &http.Client{Transport: anthropicRoundTripFunc(func(r *http.Request) (*http.Response, error) {
							calls++
							var body map[string]any
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Fatal(err)
							}
							wantFast := tier == "priority" && (model == "claude-opus-4-8" || model == "claude-opus-5" || model == "claude-opus-5-5")
							speed, present := body["speed"]
							if present != wantFast || (wantFast && speed != "fast") {
								t.Fatalf("speed=%v present=%t wantFast=%t", speed, present, wantFast)
							}
							if _, present := body["service_tier"]; present {
								t.Fatal("OpenAI field leaked into Anthropic request")
							}
							betas := r.Header.Get("anthropic-beta")
							if strings.Contains(betas, FastModeBetaHeader) != wantFast {
								t.Fatalf("beta header=%q", betas)
							}
							if oauth {
								if r.Header.Get("Authorization") != "Bearer fixture-token" || !strings.Contains(betas, OAuthBetaHeader) {
									t.Fatal("OAuth auth/header lost")
								}
							} else if r.Header.Get("x-api-key") != "fixture-key" {
								t.Fatal("API key lost")
							}
							if mode == "agentic" {
								if !strings.Contains(betas, CompactionBetaHeader) {
									t.Fatal("compaction beta lost")
								}
								return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))}, nil
							}
							return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"msg_fixture","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
						})}
						var err error
						if mode == "agentic" {
							_, err = client.sendAgenticTurnOnce(context.Background(), []agenticMessage{{Role: "user", Content: "hello"}}, nil, &AgenticOptions{Model: model, ServiceTier: tier, MaxTokens: 100, AutoCompaction: true})
						} else {
							opts := &SendOptions{Model: model, ServiceTier: tier, MaxTokens: 100}
							if mode == "attachment" {
								opts.Attachments = []*FileAttachment{{FileName: "note.txt", MediaType: "text/plain", Data: []byte("fixture")}}
							}
							_, err = client.Send(context.Background(), "hello", opts)
						}
						if err != nil {
							t.Fatal(err)
						}
						if calls != 1 {
							t.Fatalf("calls=%d", calls)
						}
					})
				}
			}
		}
	}
}

func TestFastModeEligibilityErrorIsSurfaced(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(fmt.Sprint(oauth), func(t *testing.T) {
			client := NewWithAPIKey("fixture")
			if oauth {
				client = NewWithOAuthToken("fixture-token", "", time.Now().Add(time.Hour).Unix())
			}
			calls := 0
			client.httpClient = &http.Client{Transport: anthropicRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"permission_error","message":"Fast mode is not enabled for your organization"}}`))}, nil
			})}
			_, err := client.SendAgentic(context.Background(), "hello", &AgenticOptions{Model: "claude-opus-5-5", ServiceTier: "priority", MaxTokens: 100, DisableTools: true})
			if err == nil || !strings.Contains(err.Error(), "Fast mode is not enabled") {
				t.Fatalf("error=%v", err)
			}
			if calls != 1 {
				t.Fatalf("eligibility rejection retried or silently downgraded: %d calls", calls)
			}
		})
	}
}
