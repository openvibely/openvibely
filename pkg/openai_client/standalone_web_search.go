package openaiclient

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const standaloneWebSearchToolName = "web.run"
const standaloneWebSearchStructuredOutputPrefix = "web-search-json:"

//go:embed web_run_description.md
var standaloneWebSearchDescription string

// WebSearchResult preserves both the model-facing search output and the
// structured result metadata returned by alpha/search.
type WebSearchResult struct {
	CallID   string          `json:"call_id,omitempty"`
	Commands json.RawMessage `json:"commands,omitempty"`
	Output   string          `json:"output"`
	Results  json.RawMessage `json:"results,omitempty"`
}

// DisplayOutput returns a durable, UI-friendly representation without
// changing the text sent back to the model as the function-call result.
func (r WebSearchResult) DisplayOutput() string {
	output := strings.TrimSpace(r.Output)
	if len(bytes.TrimSpace(r.Results)) == 0 || string(bytes.TrimSpace(r.Results)) == "null" {
		return output
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, r.Results, "", "  "); err != nil {
		pretty.Write(r.Results)
	}
	if output == "" {
		return "Search results:\n" + pretty.String()
	}
	return output + "\n\nSearch results:\n" + pretty.String()
}

// StructuredOutput preserves the alpha/search response fields across the
// provider-neutral text stream so the web client can render source metadata.
func (r WebSearchResult) StructuredOutput() string {
	encoded, err := json.Marshal(r)
	if err != nil {
		return r.DisplayOutput()
	}
	return standaloneWebSearchStructuredOutputPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

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
			Description: standaloneWebSearchDescription,
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

func (c *Client) standaloneWebSearchEndpoint(isChatGPTOAuth bool) (string, error) {
	base := strings.TrimSpace(OpenAIAPIBaseURL)
	if isChatGPTOAuth {
		base = strings.TrimSpace(OpenAIChatGPTAPIBaseURL)
	}
	if base == "" {
		return "", fmt.Errorf("missing OpenAI base URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse OpenAI base URL %q: %w", base, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/alpha/search"
	u.RawQuery = ""
	return u.String(), nil
}

func (c *Client) runStandaloneWebSearch(ctx context.Context, model string, inputItems []any, commands json.RawMessage, maxOutputTokens int, isChatGPTOAuth bool) (WebSearchResult, bool, error) {
	var commandObject map[string]any
	if len(bytes.TrimSpace(commands)) == 0 {
		commandObject = map[string]any{}
	} else if err := json.Unmarshal(commands, &commandObject); err != nil {
		return WebSearchResult{}, true, fmt.Errorf("decode web.run commands: %w", err)
	}

	if maxOutputTokens <= 0 {
		maxOutputTokens = openAIToolOutputTokenLimitDefault
	}
	payload := map[string]any{
		"id":                c.sessionID,
		"model":             model,
		"input":             standaloneWebSearchRecentInput(inputItems),
		"commands":          commandObject,
		"settings":          map[string]any{"allowed_callers": []string{"direct"}, "external_web_access": true},
		"max_output_tokens": maxOutputTokens,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return WebSearchResult{}, true, fmt.Errorf("marshal standalone web search request: %w", err)
	}
	endpoint, err := c.standaloneWebSearchEndpoint(isChatGPTOAuth)
	if err != nil {
		return WebSearchResult{}, true, err
	}
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(req, isChatGPTOAuth)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	}
	resp, err := c.doWithOAuthRecovery(ctx, endpoint, isChatGPTOAuth, buildReq)
	if err != nil {
		return WebSearchResult{}, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		return WebSearchResult{}, true, fmt.Errorf("POST %q: %w", endpoint, parseAPIError(resp.StatusCode, errBody))
	}
	var result WebSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return WebSearchResult{}, true, fmt.Errorf("decode standalone web search response: %w", err)
	}
	result.Results = append(json.RawMessage(nil), result.Results...)
	return result, false, nil
}

const standaloneWebSearchAssistantTokenLimit = 1000

// standaloneWebSearchRecentInput mirrors Codex's narrow search context: the
// two most recent visible user messages and assistant text between them.
func standaloneWebSearchRecentInput(inputItems []any) []any {
	visible := make([]map[string]any, 0, len(inputItems))
	userIndexes := make([]int, 0, 2)
	for _, raw := range inputItems {
		item, ok := raw.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(stringFromAny(item["type"])), "message") {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringFromAny(item["role"])))
		if role != "user" && role != "assistant" {
			continue
		}
		text := visibleSearchMessageText(item["content"], role)
		if role == "user" {
			text = visibleSearchUserText(text)
		}
		if text == "" {
			continue
		}
		contentType := "input_text"
		if role == "assistant" {
			contentType = "output_text"
		}
		visible = append(visible, map[string]any{
			"type":    "message",
			"role":    role,
			"content": []any{map[string]any{"type": contentType, "text": text}},
		})
		if role == "user" {
			userIndexes = append(userIndexes, len(visible)-1)
		}
	}
	if len(userIndexes) == 0 {
		return nil
	}
	lastUser := userIndexes[len(userIndexes)-1]
	first := lastUser
	if len(userIndexes) > 1 {
		first = userIndexes[len(userIndexes)-2]
	}
	recent := visible[first : lastUser+1]
	remainingAssistantTokens := standaloneWebSearchAssistantTokenLimit
	out := make([]any, 0, len(recent))
	for _, item := range recent {
		cloned := cloneAgenticMap(item)
		if cloned["role"] == "assistant" {
			text := visibleSearchMessageText(cloned["content"], "assistant")
			if remainingAssistantTokens <= 0 {
				continue
			}
			text = truncateTextToOpenAITokenBudget(text, remainingAssistantTokens)
			if text == "" {
				continue
			}
			remainingAssistantTokens -= approxOpenAITokenCount(text)
			cloned["content"] = []any{map[string]any{"type": "output_text", "text": text}}
		}
		out = append(out, cloned)
	}
	return out
}

func visibleSearchMessageText(content any, role string) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		wantType := "input_text"
		if role == "assistant" {
			wantType = "output_text"
		}
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok || !strings.EqualFold(strings.TrimSpace(stringFromAny(block["type"])), wantType) {
				continue
			}
			if text := strings.TrimSpace(firstNonEmpty(stringFromAny(block["text"]), stringFromAny(block["content"]))); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func visibleSearchUserText(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "# Context from my IDE setup:") {
		const requestMarker = "\n## My request:"
		marker := strings.Index(text, requestMarker)
		if marker < 0 {
			return ""
		}
		text = strings.TrimSpace(text[marker+len(requestMarker):])
	}
	for {
		trimmed := strings.TrimSpace(text)
		removed := false
		for _, tag := range []string{"environment_context", "permissions instructions", "apps_instructions", "plugins_instructions"} {
			opening := "<" + tag + ">"
			closing := "</" + tag + ">"
			if !strings.HasPrefix(trimmed, opening) {
				continue
			}
			end := strings.Index(trimmed, closing)
			if end < 0 {
				return ""
			}
			text = strings.TrimSpace(trimmed[end+len(closing):])
			removed = true
			break
		}
		if !removed {
			return trimmed
		}
	}
}
