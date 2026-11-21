// Package geminiclient implements Google Gemini generateContent streaming with
// OpenVibely's local tool loop, using either an API key or a Google account.
package geminiclient

import (
	"encoding/json"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

// Content is one Gemini conversation turn. Role is "user" or "model".
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

type Part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type FunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type FunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// ToolDefinition is a function declaration. Schema takes precedence over Parameters.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Schema      *toolschema.Schema
}

func (t ToolDefinition) parameterSchema() json.RawMessage {
	return toolschema.JSON(t.Schema, t.Parameters)
}

// Attachment is inline model input such as an image or PDF.
type Attachment struct {
	FileName  string
	MediaType string
	Data      []byte
}

// ToolCall records a single tool invocation and its result.
type ToolCall struct {
	Name   string
	Input  map[string]any
	Output string
	Error  bool
}

// Response summarizes a complete agentic exchange.
type Response struct {
	Text              string
	Model             string
	StopReason        string
	ToolCalls         []ToolCall
	InputTokens       int
	OutputTokens      int
	TotalTokens       int
	CachedInputTokens int
	ReasoningTokens   int
	LastContextTokens int
}

// TextContent returns a single-part content, the shape used for plain turns.
func TextContent(role, text string) Content {
	return Content{Role: role, Parts: []Part{{Text: text}}}
}

func contentText(c Content) string {
	if len(c.Parts) != 1 || c.Parts[0].Thought {
		return ""
	}
	return c.Parts[0].Text
}

// CloneContents deep-copies part slices so callers cannot mutate retained history.
func CloneContents(in []Content) []Content {
	if in == nil {
		return nil
	}
	out := make([]Content, len(in))
	for i, c := range in {
		out[i] = Content{Role: c.Role, Parts: append([]Part(nil), c.Parts...)}
	}
	return out
}
