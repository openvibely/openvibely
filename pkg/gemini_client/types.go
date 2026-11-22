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
	Text                string            `json:"text,omitempty"`
	Thought             bool              `json:"thought,omitempty"`
	ThoughtSignature    string            `json:"thoughtSignature,omitempty"`
	FileData            *FileData         `json:"fileData,omitempty"`
	VideoMetadata       json.RawMessage   `json:"videoMetadata,omitempty"`
	ExecutableCode      json.RawMessage   `json:"executableCode,omitempty"`
	CodeExecutionResult json.RawMessage   `json:"codeExecutionResult,omitempty"`
	InlineData          *InlineData       `json:"inlineData,omitempty"`
	FunctionCall        *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse    *FunctionResponse `json:"functionResponse,omitempty"`
}

type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type FileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type PartialArg struct {
	JSONPath     string          `json:"jsonPath"`
	StringValue  *string         `json:"stringValue,omitempty"`
	NumberValue  *float64        `json:"numberValue,omitempty"`
	BoolValue    *bool           `json:"boolValue,omitempty"`
	NullValue    json.RawMessage `json:"nullValue,omitempty"`
	WillContinue bool            `json:"willContinue,omitempty"`
}

type FunctionCall struct {
	PartialArgs  []PartialArg    `json:"partialArgs,omitempty"`
	WillContinue bool            `json:"willContinue,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name"`
	Args         json.RawMessage `json:"args,omitempty"`
}

type FunctionResponsePart struct {
	InlineData *InlineData `json:"inlineData,omitempty"`
	FileData   *FileData   `json:"fileData,omitempty"`
}

type FunctionResponse struct {
	Parts      []FunctionResponsePart `json:"parts,omitempty"`
	Scheduling string                 `json:"scheduling,omitempty"`
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name"`
	Response   map[string]any         `json:"response"`
}

// ToolDefinition is a function declaration. Schema takes precedence over Parameters.
type ToolDefinition struct {
	Name               string
	Description        string
	Parameters         json.RawMessage
	Schema             *toolschema.Schema
	ResponseJSONSchema json.RawMessage
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
		out[i] = Content{Role: c.Role, Parts: make([]Part, len(c.Parts))}
		for j, p := range c.Parts {
			q := p
			q.VideoMetadata = append(json.RawMessage(nil), p.VideoMetadata...)
			q.ExecutableCode = append(json.RawMessage(nil), p.ExecutableCode...)
			q.CodeExecutionResult = append(json.RawMessage(nil), p.CodeExecutionResult...)
			if p.InlineData != nil {
				v := *p.InlineData
				q.InlineData = &v
			}
			if p.FileData != nil {
				v := *p.FileData
				q.FileData = &v
			}
			if p.FunctionCall != nil {
				v := *p.FunctionCall
				v.Args = append(json.RawMessage(nil), v.Args...)
				v.PartialArgs = append([]PartialArg(nil), v.PartialArgs...)
				for k, a := range v.PartialArgs {
					if a.StringValue != nil {
						x := *a.StringValue
						a.StringValue = &x
					}
					if a.NumberValue != nil {
						x := *a.NumberValue
						a.NumberValue = &x
					}
					if a.BoolValue != nil {
						x := *a.BoolValue
						a.BoolValue = &x
					}
					a.NullValue = append(json.RawMessage(nil), a.NullValue...)
					v.PartialArgs[k] = a
				}
				q.FunctionCall = &v
			}
			if p.FunctionResponse != nil {
				v := *p.FunctionResponse
				if v.Response != nil {
					v.Response = cloneJSONValue(v.Response).(map[string]any)
				}
				v.Parts = append([]FunctionResponsePart(nil), v.Parts...)
				for k, a := range v.Parts {
					if a.InlineData != nil {
						x := *a.InlineData
						a.InlineData = &x
					}
					if a.FileData != nil {
						x := *a.FileData
						a.FileData = &x
					}
					v.Parts[k] = a
				}
				q.FunctionResponse = &v
			}
			out[i].Parts[j] = q
		}
	}
	return out
}

func cloneJSONValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = cloneJSONValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = cloneJSONValue(v)
		}
		return out
	case json.RawMessage:
		return append(json.RawMessage(nil), x...)
	default:
		return v
	}
}
