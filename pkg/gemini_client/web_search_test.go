package geminiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/stretchr/testify/require"
)

func TestWebSearchToolRoundTrip(t *testing.T) {
	for _, tc := range []struct{ oauth, oversized bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		oauth := tc.oauth
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			calls, searches, refreshed, callbacks := 0, 0, false, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				req := body
				if oauth {
					req = body["request"].(map[string]any)
					require.Equal(t, "project", body["project"])
				} else {
					require.Equal(t, "key", r.Header.Get("x-goog-api-key"))
				}
				tools, _ := req["tools"].([]any)
				isSearch := false
				for _, tool := range tools {
					if _, ok := tool.(map[string]any)["googleSearch"]; ok {
						isSearch = true
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if isSearch {
					searches++
					if oauth && !refreshed {
						w.WriteHeader(401)
						return
					}
					if oauth {
						require.Equal(t, "Bearer fresh", r.Header.Get("Authorization"))
						require.Equal(t, "web_search", body["requestType"])
						require.Equal(t, webSearchModel, body["model"])
					} else {
						require.Contains(t, r.URL.Path, webSearchModel)
					}
					raw, _ := json.Marshal(req)
					require.NotContains(t, string(raw), "PRIVATE_HISTORY")
					require.NotContains(t, string(raw), "functionDeclarations")
					require.Contains(t, string(raw), "weather in Paris")
					chunk := `{"candidates":[{"content":{"role":"model","parts":[{"text":"Sunny."}]},"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["Paris weather"],"groundingChunks":[{"web":{"uri":"https://example.com/weather","title":"Weather"}}],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":6,"text":"Sunny."},"groundingChunkIndices":[0]}],"searchEntryPoint":{"renderedContent":"<div>Search</div>"}}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`
					if tc.oversized {
						chunk = strings.ReplaceAll(chunk, "Sunny.", strings.Repeat("Sunny. ", 8000))
						chunk = strings.ReplaceAll(chunk, "<div>Search</div>", strings.Repeat("<div>Search</div>", 8000))
					}
					if oauth {
						chunk = `{"response":` + chunk + `}`
					}
					fmt.Fprint(w, "data: "+chunk+"\n\n")
					return
				}
				calls++
				raw, _ := json.Marshal(req)
				if calls == 1 {
					require.Contains(t, string(raw), `"name":"web_search"`)
					fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"search1","name":"web_search","args":{"query":"weather in Paris"}}}]},"finishReason":"STOP"}]}`+"\n\n")
				} else {
					require.Contains(t, string(raw), "https://example.com/weather")
					if !tc.oversized {
						require.Contains(t, string(raw), "Sunny.[1]")
					}
					contentsJSON, _ := json.Marshal(req["contents"])
					var contents []Content
					require.NoError(t, json.Unmarshal(contentsJSON, &contents))
					assertSearchReplay(t, contents, tc.oversized)
					fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Final answer"}]},"finishReason":"STOP"}]}`+"\n\n")
				}
			}))
			defer server.Close()
			c := NewWithAPIKey("key")
			c.SetBaseURL(server.URL)
			if oauth {
				c = NewWithOAuth("old", "project")
				c.SetCodeAssistURL(server.URL + "/v1internal")
				c.SetUnauthorizedHandler(func(_ context.Context, token string) (string, bool, error) {
					require.Equal(t, "old", token)
					refreshed = true
					return "fresh", true, nil
				})
			}
			c.SetHistory([]Content{TextContent("user", "PRIVATE_HISTORY")})
			var text strings.Builder
			response, err := c.Send(context.Background(), "Search", &Options{Model: "gemini-3.8-flash", WebSearchEnabled: true, OmitExecutionEnvironment: true, OnText: func(s string) { text.WriteString(s) }, OnToolResult: func(name, output string, isError bool) {
				callbacks++
				require.Equal(t, "web_search", name)
				require.False(t, isError)
				require.Contains(t, output, "[1] [Weather](https://example.com/weather)")
			}})
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			require.GreaterOrEqual(t, searches, 1)
			require.Equal(t, 1, callbacks)
			require.Equal(t, "Final answer", text.String())
			require.Equal(t, 15, response.TotalTokens)
			raw, err := json.Marshal(c.LastExchange())
			require.NoError(t, err)
			require.Contains(t, string(raw), "https://example.com/weather")
			assertSearchReplay(t, c.LastExchange(), tc.oversized)
		})
	}
}

func TestWebSearchDisabledOrOverridden(t *testing.T) {
	for _, mode := range []string{"disabled", "filtered", "skip_defaults", "not_enabled", "runtime_override"} {
		t.Run(mode, func(t *testing.T) {
			n := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n++
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				raw, _ := json.Marshal(body)
				if mode == "runtime_override" {
					require.Contains(t, string(raw), "Runtime search")
				} else {
					require.NotContains(t, string(raw), `"name":"web_search"`)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Done"}]},"finishReason":"STOP"}]}`+"\n\n")
			}))
			defer server.Close()
			c := NewWithAPIKey("key")
			c.SetBaseURL(server.URL)
			opts := &Options{Model: "gemini-3.8-flash", WebSearchEnabled: true, OmitExecutionEnvironment: true}
			switch mode {
			case "disabled":
				opts.DisableTools = true
			case "filtered":
				opts.ToolFilter = func(name string) bool { return name != "web_search" }
			case "skip_defaults":
				opts.SkipDefaultTools = true
			case "not_enabled":
				opts.WebSearchEnabled = false
			case "runtime_override":
				opts.ExtraTools = []ToolDefinition{{Name: "web_search", Description: "Runtime search", Parameters: json.RawMessage(`{"type":"object"}`)}}
			}
			_, err := c.Send(context.Background(), "hello", opts)
			require.NoError(t, err)
			require.Equal(t, 1, n)
		})
	}
}

func TestWebSearchErrorBecomesToolResult(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 2 {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"error":{"message":"search unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"web_search","args":{"query":"example"}}}]},"finishReason":"STOP"}]}`+"\n\n")
		} else {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			raw, _ := json.Marshal(body)
			require.Contains(t, string(raw), "search unavailable")
			fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"Search unavailable"}]},"finishReason":"STOP"}]}`+"\n\n")
		}
	}))
	defer server.Close()
	c := NewWithAPIKey("key")
	c.SetBaseURL(server.URL)
	response, err := c.Send(context.Background(), "search", &Options{Model: "gemini-3.8-flash", WebSearchEnabled: true, OmitExecutionEnvironment: true})
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.True(t, response.ToolCalls[0].Error)
}

func TestWebSearchRetryAndInvalidResponses(t *testing.T) {
	for _, mode := range []string{"transient", "partial", "empty", "unexpected_tool", "missing_query"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "transient" && calls == 1 {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch mode {
				case "partial":
					fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`+"\n\n")
				case "empty":
					fmt.Fprint(w, `data: {"candidates":[{"finishReason":"STOP"}]}`+"\n\n")
				case "unexpected_tool":
					fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"shell","args":{}}}]},"finishReason":"STOP"}]}`+"\n\n")
				default:
					fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"Result"}]},"finishReason":"STOP"}]}`+"\n\n")
				}
			}))
			defer server.Close()
			c := NewWithAPIKey("key")
			c.SetBaseURL(server.URL)
			input := json.RawMessage(`{"query":"news"}`)
			if mode == "missing_query" {
				input = json.RawMessage(`{"query":" "}`)
			}
			result, err := c.webSearch(context.Background(), input)
			if mode == "transient" {
				require.NoError(t, err)
				require.Equal(t, 2, calls)
				require.Contains(t, result.output, "Result")
			} else {
				require.Error(t, err)
				if mode == "missing_query" {
					require.Zero(t, calls)
				} else {
					require.Equal(t, 1, calls)
				}
			}
		})
	}
}

func assertSearchReplay(t *testing.T, contents []Content, truncated bool) {
	t.Helper()
	found := false
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.FunctionResponse == nil || part.FunctionResponse.Name != "web_search" {
				continue
			}
			found = true
			output, ok := part.FunctionResponse.Response["output"].(string)
			require.True(t, ok)
			require.LessOrEqual(t, len(output), 40000)
			require.Contains(t, output, "https://example.com/weather")
			require.NotContains(t, output, "groundingMetadata")
			if truncated {
				require.Contains(t, output, "Tool output truncated")
			} else {
				require.Contains(t, output, "Sunny.[1]")
			}
		}
	}
	require.True(t, found)
}

func TestFormatWebSearchResult(t *testing.T) {
	metadata := json.RawMessage(`{"groundingChunks":[{"web":{"uri":"https://example.com/a(b) c","title":" A  [title]\n here "}},{"web":{"uri":"https://example.com/2","title":"Two"}}],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":6,"text":"First."},"groundingChunkIndices":[0,0,1,-1,99]},{"segment":{"text":"Second."},"groundingChunkIndices":[1]}],"searchEntryPoint":{"renderedContent":"UNUSED_HTML"}}`)
	output := formatWebSearchResult("First. Second.", metadata)
	require.Equal(t, "First.[1][2] Second.[2]\n\nSources:\n[1] [A \\[title\\] here](https://example.com/a%28b%29%20c)\n[2] [Two](https://example.com/2)", output)
	require.Equal(t, "Answer", formatWebSearchResult("Answer", nil))
	require.Equal(t, "Answer", formatWebSearchResult("Answer", json.RawMessage(`{broken`)))
}

func TestSearchCitationOffset(t *testing.T) {
	for _, tc := range []struct {
		segment searchSegment
		want    int
	}{
		{searchSegment{0, 6, "世界"}, 6},
		{searchSegment{0, 2, "世界"}, 6},
		{searchSegment{99, 100, "world"}, 12},
		{searchSegment{-1, 99, "absent"}, -1},
		{searchSegment{0, 1, ""}, -1},
	} {
		require.Equal(t, tc.want, searchCitationOffset("世界 world", tc.segment))
	}
}

func TestWebSearchUsesGenericOutputLimit(t *testing.T) {
	output := formatWebSearchResult(strings.Repeat("Answer ", 10000), json.RawMessage(`{"groundingChunks":[{"web":{"uri":"https://example.com/source","title":"Source"}}]}`))
	for _, limit := range []int{0, 200, 1} {
		got, images := toolOutputForModel(output, limit)
		require.Equal(t, tokenestimate.ToolOutputForModelInput(output, limit), got)
		require.Empty(t, images)
	}
}
