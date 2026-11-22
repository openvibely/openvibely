package gemini

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"strings"

	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// CompletionsFromContents converts Gemini replay into provider-neutral
// Chat Completions messages, preserving tool call/result pairing by ID.
func CompletionsFromContents(contents []geminiclient.Content) []openaiclient.CompletionsHistoryMessage {
	var out []openaiclient.CompletionsHistoryMessage
	pending := map[string][]string{}
	var toolImages []map[string]interface{}
	flushToolImages := func() {
		if len(toolImages) > 0 {
			out = append(out, openaiclient.CompletionsHistoryMessage{Role: "user", ImageContent: toolImages})
			toolImages = nil
		}
	}
	for ci, c := range contents {
		role := "user"
		if c.Role == "model" {
			flushToolImages()
			role = "assistant"
		}
		var text strings.Builder
		var images []map[string]interface{}
		var calls []openaiclient.CompletionsToolCall
		var results []openaiclient.CompletionsHistoryMessage
		for pi, p := range c.Parts {
			switch {
			case p.Thought:
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					id = syntheticCallID(ci, pi)
				}
				pending[p.FunctionCall.Name] = append(pending[p.FunctionCall.Name], id)
				var call openaiclient.CompletionsToolCall
				call.ID, call.Type = id, "function"
				call.Function.Name = p.FunctionCall.Name
				call.Function.Arguments = string(p.FunctionCall.Args)
				if call.Function.Arguments == "" {
					call.Function.Arguments = "{}"
				}
				calls = append(calls, call)
			case p.FunctionResponse != nil:
				id := p.FunctionResponse.ID
				if queue := pending[p.FunctionResponse.Name]; len(queue) > 0 {
					if id == "" {
						id = queue[0]
					}
					pending[p.FunctionResponse.Name] = queue[1:]
				}
				results = append(results, openaiclient.CompletionsHistoryMessage{Role: "tool", ToolCallID: id, Content: functionResponseText(p.FunctionResponse.Response)})
				for _, media := range p.FunctionResponse.Parts {
					var url string
					if media.InlineData != nil && strings.HasPrefix(strings.ToLower(media.InlineData.MimeType), "image/") {
						url = "data:" + media.InlineData.MimeType + ";base64," + media.InlineData.Data
					} else if media.FileData != nil && (media.FileData.MimeType == "" || strings.HasPrefix(strings.ToLower(media.FileData.MimeType), "image/")) {
						url = media.FileData.FileURI
					}
					if url != "" {
						toolImages = append(toolImages, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": url}})
					}
				}
			case p.InlineData != nil:
				images = append(images, map[string]interface{}{
					"type":      "image_url",
					"image_url": map[string]interface{}{"url": "data:" + p.InlineData.MimeType + ";base64," + p.InlineData.Data},
				})
			case p.FileData != nil && p.FileData.FileURI != "" && (p.FileData.MimeType == "" || strings.HasPrefix(strings.ToLower(p.FileData.MimeType), "image/")):
				images = append(images, map[string]interface{}{
					"type":      "image_url",
					"image_url": map[string]interface{}{"url": p.FileData.FileURI},
				})
			default:
				text.WriteString(p.Text)
			}
		}
		out = append(out, results...)
		// Parallel tool results must precede their image messages, even when
		// the results occupy separate replay rows.
		hasPending := false
		for _, ids := range pending {
			hasPending = hasPending || len(ids) > 0
		}
		if !hasPending {
			flushToolImages()
		}
		if text.Len() > 0 || len(calls) > 0 {
			out = append(out, openaiclient.CompletionsHistoryMessage{Role: role, Content: text.String(), ToolCalls: calls})
		}
		if len(images) > 0 {
			out = append(out, openaiclient.CompletionsHistoryMessage{Role: "user", ImageContent: images})
		}
	}
	flushToolImages()
	return out
}

// ContentsFromCompletions converts provider-neutral messages into Gemini contents.
func ContentsFromCompletions(messages []openaiclient.CompletionsHistoryMessage) []geminiclient.Content {
	names := map[string]string{}
	var out []geminiclient.Content
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			c := geminiclient.Content{Role: "model"}
			if m.Content != "" {
				c.Parts = append(c.Parts, geminiclient.Part{Text: m.Content})
			}
			for _, block := range m.ImageContent {
				if part, ok := imagePart(block); ok {
					c.Parts = append(c.Parts, part)
				}
			}
			for _, call := range m.ToolCalls {
				names[call.ID] = call.Function.Name
				args := json.RawMessage(call.Function.Arguments)
				if !json.Valid(args) {
					args = json.RawMessage("{}")
				}
				c.Parts = append(c.Parts, geminiclient.Part{FunctionCall: &geminiclient.FunctionCall{ID: call.ID, Name: call.Function.Name, Args: args}})
			}
			if len(c.Parts) > 0 {
				out = append(out, c)
			}
		case "tool":
			name := names[m.ToolCallID]
			if name == "" {
				out = append(out, geminiclient.TextContent("user", m.Content))
				continue
			}
			out = append(out, geminiclient.Content{Role: "user", Parts: []geminiclient.Part{{FunctionResponse: &geminiclient.FunctionResponse{
				ID: m.ToolCallID, Name: name, Response: map[string]any{"output": m.Content},
			}}}})
		default:
			c := geminiclient.Content{Role: "user"}
			if m.Content != "" {
				c.Parts = append(c.Parts, geminiclient.Part{Text: m.Content})
			}
			for _, image := range m.ImageContent {
				if part, ok := imagePart(image); ok {
					c.Parts = append(c.Parts, part)
				}
			}
			if len(c.Parts) > 0 {
				out = append(out, c)
			}
		}
	}
	return out
}

// ContentRows serializes contents as replay rows.
func ContentRows(contents []geminiclient.Content) ([]json.RawMessage, error) {
	rows := make([]json.RawMessage, 0, len(contents))
	for _, c := range contents {
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		rows = append(rows, raw)
	}
	return rows, nil
}

func syntheticCallID(content, part int) string {
	return "gemini_call_" + itoa(content) + "_" + itoa(part)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func functionResponseText(response map[string]any) string {
	for _, key := range []string{"output", "error"} {
		if view, ok := response[key].(jsonview.Value); ok {
			if text, err := view.Text(); err == nil {
				return text
			}
		} else if text, ok := response[key].(string); ok {
			return text
		}
	}

	raw, _ := json.Marshal(response)
	return string(raw)
}

func imagePart(image map[string]interface{}) (geminiclient.Part, bool) {
	if image["type"] == "text" {
		text, ok := image["text"].(string)
		return geminiclient.Part{Text: text}, ok
	}
	var url string
	if nested, ok := image["image_url"].(map[string]interface{}); ok {
		url, _ = nested["url"].(string)
	}
	if url == "" {
		return geminiclient.Part{}, false
	}
	rest, ok := strings.CutPrefix(url, "data:")
	if !ok {
		// Shared image URLs do not carry a MIME type. Leave it unspecified
		// rather than guessing from a possibly extensionless or signed URL.
		return geminiclient.Part{FileData: &geminiclient.FileData{FileURI: url}}, true
	}
	mime, data, ok := strings.Cut(rest, ";base64,")
	if !ok {
		return geminiclient.Part{}, false
	}
	return geminiclient.Part{InlineData: &geminiclient.InlineData{MimeType: mime, Data: data}}, true
}
