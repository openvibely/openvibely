package openaiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const standaloneWebSearchToolName = "web.run"

// standaloneWebSearchTool mirrors the Codex Responses Lite web namespace. The
// model calls web.run and the client forwards those commands to alpha/search.
func standaloneWebSearchTool() ToolDefinition {
	return ToolDefinition{
		Type:        "namespace",
		Name:        "web",
		Description: "Tools in the web namespace.",
		Tools: []ToolDefinition{{
			Type:        "function",
			Name:        "run",
			Description: "Access the internet. Supports search queries, opening and finding within pages, PDF screenshots, finance, weather, sports, and time lookups.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"search_query":{"type":"array","items":{"type":"object","properties":{"q":{"type":"string"},"recency":{"type":"integer","minimum":0},"domains":{"type":"array","items":{"type":"string"}}},"required":["q"]}},
					"image_query":{"type":"array","items":{"type":"object","properties":{"q":{"type":"string"},"recency":{"type":"integer","minimum":0},"domains":{"type":"array","items":{"type":"string"}}},"required":["q"]}},
					"open":{"type":"array","items":{"type":"object","properties":{"ref_id":{"type":"string"},"lineno":{"type":"integer","minimum":0}},"required":["ref_id"]}},
					"click":{"type":"array","items":{"type":"object","properties":{"ref_id":{"type":"string"},"id":{"type":"integer","minimum":0}},"required":["ref_id","id"]}},
					"find":{"type":"array","items":{"type":"object","properties":{"ref_id":{"type":"string"},"pattern":{"type":"string"}},"required":["ref_id","pattern"]}},
					"screenshot":{"type":"array","items":{"type":"object","properties":{"ref_id":{"type":"string"},"pageno":{"type":"integer","minimum":0}},"required":["ref_id","pageno"]}},
					"finance":{"type":"array","items":{"type":"object","properties":{"ticker":{"type":"string"},"type":{"type":"string","enum":["equity","fund","crypto","index"]},"market":{"type":"string"}},"required":["ticker","type"]}},
					"weather":{"type":"array","items":{"type":"object","properties":{"location":{"type":"string"},"start":{"type":"string"},"duration":{"type":"integer","minimum":0}},"required":["location"]}},
					"sports":{"type":"array","items":{"type":"object","properties":{"tool":{"type":"string","enum":["sports"]},"fn":{"type":"string","enum":["schedule","standings"]},"league":{"type":"string","enum":["nba","wnba","nfl","nhl","mlb","epl","ncaamb","ncaawb","ipl"]},"team":{"type":"string"},"opponent":{"type":"string"},"date_from":{"type":"string"},"date_to":{"type":"string"},"num_games":{"type":"integer","minimum":0},"locale":{"type":"string"}},"required":["fn","league"]}},
					"time":{"type":"array","items":{"type":"object","properties":{"utc_offset":{"type":"string"}},"required":["utc_offset"]}},
					"response_length":{"type":"string","enum":["short","medium","long"]}
				},
				"additionalProperties":false
			}`),
		}},
	}
}

func (c *Client) standaloneWebSearchEndpoint() (string, error) {
	base := strings.TrimSpace(OpenAIChatGPTAPIBaseURL)
	if base == "" {
		return "", fmt.Errorf("missing ChatGPT Codex base URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse ChatGPT Codex base URL %q: %w", base, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/alpha/search"
	u.RawQuery = ""
	return u.String(), nil
}

func (c *Client) runStandaloneWebSearch(ctx context.Context, model string, inputItems []any, commands json.RawMessage, maxOutputTokens int) (string, bool, error) {
	var commandObject map[string]any
	if len(bytes.TrimSpace(commands)) == 0 {
		commandObject = map[string]any{}
	} else if err := json.Unmarshal(commands, &commandObject); err != nil {
		return "", true, fmt.Errorf("decode web.run commands: %w", err)
	}

	if maxOutputTokens <= 0 {
		maxOutputTokens = openAIToolOutputTokenLimitDefault
	}
	payload := map[string]any{
		"id":                c.sessionID,
		"model":             model,
		"input":             append([]any(nil), inputItems...),
		"commands":          commandObject,
		"settings":          map[string]any{"allowed_callers": []string{"direct"}, "external_web_access": true},
		"max_output_tokens": maxOutputTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", true, fmt.Errorf("marshal standalone web search request: %w", err)
	}
	endpoint, err := c.standaloneWebSearchEndpoint()
	if err != nil {
		return "", true, err
	}
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(req, true)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	}
	resp, err := c.doWithOAuthRecovery(ctx, endpoint, true, buildReq)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", true, fmt.Errorf("POST %q: %w", endpoint, parseAPIError(resp.StatusCode, errBody))
	}
	var result struct {
		Output string `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", true, fmt.Errorf("decode standalone web search response: %w", err)
	}
	return result.Output, false, nil
}
