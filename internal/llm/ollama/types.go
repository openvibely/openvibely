package ollama

import (
	"encoding/json"
	"time"
)

// Tool represents a tool/function available for the model to call.
type Tool struct {
	Type     string   `json:"type"` // "function"
	Function Function `json:"function"`
}

// Function describes a callable function.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall represents a tool call made by the model.
type ToolCall struct {
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the function name and arguments from a tool call.
type ToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ModelList is the response from GET /api/tags.
type ModelList struct {
	Models []ModelInfo `json:"models"`
}

// ModelInfo describes a model available on the Ollama instance.
type ModelInfo struct {
	Name       string       `json:"name"`
	Model      string       `json:"model"`
	ModifiedAt time.Time    `json:"modified_at"`
	Size       int64        `json:"size"`
	Digest     string       `json:"digest"`
	Details    ModelDetails `json:"details"`
}

// ModelDetails holds metadata about an Ollama model.
type ModelDetails struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// chatRequest is the request body for /api/chat.
type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	Stream    bool          `json:"stream"`
	Options   *options      `json:"options,omitempty"`
	Format    json.RawMessage `json:"format,omitempty"`     // structured output ("json" or JSON schema)
	Tools     []Tool        `json:"tools,omitempty"`      // available tools for function calling
	Think     bool          `json:"think,omitempty"`      // enable thinking/reasoning
	KeepAlive string        `json:"keep_alive,omitempty"` // how long to keep model loaded
}

// chatMessage represents a message in the Ollama chat format.
type chatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Images    []string   `json:"images,omitempty"`     // base64-encoded images for multimodal
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // tool calls from assistant
	Thinking  string     `json:"thinking,omitempty"`   // reasoning content (think mode)
}

// options are optional model parameters.
type options struct {
	Temperature      float64  `json:"temperature,omitempty"`
	NumPredict       int      `json:"num_predict,omitempty"`       // Ollama equivalent of max_tokens
	TopK             int      `json:"top_k,omitempty"`             // top-K sampling
	TopP             float64  `json:"top_p,omitempty"`             // nucleus sampling
	MinP             float64  `json:"min_p,omitempty"`             // min probability threshold
	Stop             []string `json:"stop,omitempty"`              // stop sequences
	Seed             int      `json:"seed,omitempty"`              // random seed for reproducibility
	RepeatPenalty    float64  `json:"repeat_penalty,omitempty"`    // penalty for repeated tokens
	PresencePenalty  float64  `json:"presence_penalty,omitempty"`  // presence penalty
	FrequencyPenalty float64  `json:"frequency_penalty,omitempty"` // frequency penalty
	NumCtx           int      `json:"num_ctx,omitempty"`           // context window size override
}

// chatResponse is the response body for /api/chat (non-streaming).
type chatResponse struct {
	Model           string      `json:"model"`
	Message         chatMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason,omitempty"`
	TotalDur        int64       `json:"total_duration"`
	LoadDur         int64       `json:"load_duration"`
	PromptEvalCount int         `json:"prompt_eval_count"` // input tokens processed
	PromptEvalDur   int64       `json:"prompt_eval_duration"`
	EvalCount       int         `json:"eval_count"` // output tokens generated
	EvalDur         int64       `json:"eval_duration"`
}

// chatStreamChunk is a single streaming chunk from /api/chat.
type chatStreamChunk struct {
	Model           string      `json:"model"`
	Message         chatMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason,omitempty"`
	TotalDur        int64       `json:"total_duration"`       // final chunk only
	LoadDur         int64       `json:"load_duration"`        // final chunk only
	PromptEvalCount int         `json:"prompt_eval_count"`    // final chunk only
	PromptEvalDur   int64       `json:"prompt_eval_duration"` // final chunk only
	EvalCount       int         `json:"eval_count"`           // final chunk only
	EvalDur         int64       `json:"eval_duration"`        // final chunk only
}

// generateRequest is the request body for /api/generate.
type generateRequest struct {
	Model   string   `json:"model"`
	Prompt  string   `json:"prompt"`
	Stream  bool     `json:"stream"`
	Options *options `json:"options,omitempty"`
}

// generateResponse is the response body for /api/generate (non-streaming).
type generateResponse struct {
	Model     string `json:"model"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
	TotalDur  int64  `json:"total_duration"`
	EvalCount int    `json:"eval_count"`
}

// generateStreamChunk is a single streaming chunk from /api/generate.
type generateStreamChunk struct {
	Model     string `json:"model"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
	TotalDur  int64  `json:"total_duration"`
	EvalCount int    `json:"eval_count"`
}

// errorResponse is the error response from Ollama API.
type errorResponse struct {
	Error string `json:"error"`
}
