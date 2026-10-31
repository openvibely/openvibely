package openaiclient

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	"reflect"
)

// immutableCompletionsReplay borrows an immutable stored row. The decoded
// snapshot detects edits before wire reuse; recovery/truncation must win.
type immutableCompletionsReplay struct {
	raw     json.RawMessage
	message CompletionsHistoryMessage
}

// DecodeCompletionsReplay decodes an immutable native row once and retains its
// wire representation only when no legacy conversion is required.
func DecodeCompletionsReplay(raw json.RawMessage) (CompletionsHistoryMessage, error) {
	var m CompletionsHistoryMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	if len(m.ImageContent) != 0 {
		return m, nil
	}
	d := jsontext.NewDecoder(bytes.NewBuffer(raw), jsontext.AllowDuplicateNames(true))
	if token, err := d.ReadToken(); err != nil || token.Kind() != '{' {
		return m, nil
	}
	seen := map[string]bool{}
	contentString := false
	for d.PeekKind() != '}' {
		key, err := d.ReadToken()
		if err != nil {
			return m, nil
		}
		name := key.String()
		if seen[name] {
			return m, nil
		}
		seen[name] = true
		switch name {
		case "role", "content", "reasoning_content", "tool_calls", "tool_call_id":
		default:
			return m, nil
		}
		if name == "content" {
			contentString = d.PeekKind() == '"'
		}
		if _, err := d.ReadValue(); err != nil {
			return m, nil
		}
	}
	if !contentString {
		return m, nil
	}
	snapshot := m
	snapshot.ToolCalls = append([]CompletionsToolCall(nil), m.ToolCalls...)
	m.rawReplay = &immutableCompletionsReplay{raw: raw, message: snapshot}
	return m, nil
}

func (m CompletionsHistoryMessage) wireReplay() json.RawMessage {
	cached := m.rawReplay
	if cached == nil {
		return nil
	}
	m.rawReplay = nil
	if !reflect.DeepEqual(m, cached.message) {
		return nil
	}
	return cached.raw
}

// DecodeCompletionsHistory applies native immutable-row reuse to either adapter;
// legacy serialized histories keep their existing decoding behavior.
func DecodeCompletionsHistory(replay models.ExecutionReplayMessage) ([]CompletionsHistoryMessage, error) {
	if replay.TranscriptJSON == "" && replay.Rows != nil && replay.Rows.Format == "array" {
		out := make([]CompletionsHistoryMessage, 0, len(replay.Rows.Items))
		for _, raw := range replay.Rows.Items {
			m, err := DecodeCompletionsReplay(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}
	var out []CompletionsHistoryMessage
	err := replay.DecodeTranscript(&out)
	return out, err
}

// PreparedCompletionsHistory keeps tool payloads immutable until truncation or
// conversion requires text. It is separate from the public decoded history API.
type PreparedCompletionsHistory struct{ messages []CompletionsHistoryMessage }

func PrepareCompletionsHistory(replay models.ExecutionReplayMessage) (PreparedCompletionsHistory, error) {
	if replay.TranscriptJSON != "" || replay.Rows == nil || replay.Rows.Format != "array" {
		m, err := DecodeCompletionsHistory(replay)
		return PreparedCompletionsHistory{m}, err
	}
	out := make([]CompletionsHistoryMessage, 0, len(replay.Rows.Items))
	for _, raw := range replay.Rows.Items {
		var role string
		eligible := true
		fields := map[string]json.RawMessage{}
		err := jsonview.Object(raw, func(k string, v json.RawMessage) error {
			if _, exists := fields[k]; exists {
				eligible = false
			}
			switch k {
			case "role", "content", "reasoning_content", "tool_calls", "tool_call_id":
			default:
				eligible = false
			}
			fields[k] = v
			if k == "role" {
				return json.Unmarshal(v, &role)
			}
			return nil
		})
		if err != nil {
			return PreparedCompletionsHistory{}, err
		}
		content := fields["content"]
		if !eligible || role != "tool" || len(content) == 0 || content[0] != '"' || len(fields["image_content"]) > 0 {
			m, err := DecodeCompletionsReplay(raw)
			if err != nil {
				return PreparedCompletionsHistory{}, err
			}
			out = append(out, m)
			continue
		}
		// Decode the small metadata only; preserve the tool-result string verbatim.
		fields["content"] = json.RawMessage(`""`)
		meta, err := json.Marshal(fields)
		if err != nil {
			return PreparedCompletionsHistory{}, err
		}
		var m CompletionsHistoryMessage
		if err := json.Unmarshal(meta, &m); err != nil {
			return PreparedCompletionsHistory{}, err
		}
		view, err := jsonview.Borrow(content)
		if err != nil {
			return PreparedCompletionsHistory{}, err
		}
		m.rawContent = &view
		out = append(out, m)
	}
	return PreparedCompletionsHistory{out}, nil
}
func PreparedCompletionsMessages(messages []CompletionsHistoryMessage) PreparedCompletionsHistory {
	return PreparedCompletionsHistory{messages}
}
func (h PreparedCompletionsHistory) Append(other PreparedCompletionsHistory) PreparedCompletionsHistory {
	return PreparedCompletionsHistory{append(h.messages, other.messages...)}
}
func (c *Client) SetPreparedCompletionsHistory(h PreparedCompletionsHistory) {
	c.completionsHistory = cloneCompletionsHistory(h.messages)
	// SendCompletions reads the structured history. Do not build another decoded
	// text-only history for that same request.
	c.History = nil
}

// EstimatedTokens preserves the message envelope and image fallback used by the
// service estimator, while counting decoded fields and raw tool content directly.
func (h PreparedCompletionsHistory) EstimatedTokens() int {
	if len(h.messages) == 0 {
		return 0
	}
	size := len(`{"messages":[]}`) + len(h.messages) - 1
	for _, m := range h.messages {
		content := m.contentValue()
		if len(m.ImageContent) > 0 {
			content = m.ImageContent
		}
		fields := map[string]any{"role": m.Role, "content": content}
		borrowedText := m.rawContent != nil && m.Content == "" && len(m.ImageContent) == 0
		if borrowedText {
			fields["content"] = nil
		}
		if m.ReasoningContent != "" {
			fields["reasoning_content"] = m.ReasoningContent
		}
		if m.ToolCallID != "" {
			fields["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]any, len(m.ToolCalls))
			for i, t := range m.ToolCalls {
				calls[i] = map[string]any{"id": t.ID, "type": t.Type, "function": map[string]any{"name": t.Function.Name, "arguments": t.Function.Arguments}}
			}
			fields["tool_calls"] = calls
		}
		n, err := requestjson.Size(fields)
		if err != nil {
			return 0
		}
		adjustment, err := tokenestimate.ContentAdjustment(content, "image_url", 2000)
		if err != nil {
			return 0
		}
		if borrowedText {
			canonical, err := m.rawContent.StringSize()
			if err != nil {
				return 0
			}
			n += canonical - len("null")
		}
		size += n + adjustment
	}
	return tokenestimate.FromByteCount(size)
}

// DecodedMessages exports a mutable legacy view only at a consumer boundary.
func (h PreparedCompletionsHistory) DecodedMessages() []CompletionsHistoryMessage {
	out := cloneCompletionsHistory(h.messages)
	for i := range out {
		out[i].Content = out[i].contentText()
		out[i].rawContent = nil
	}
	return out
}
func (h PreparedCompletionsHistory) Empty() bool { return len(h.messages) == 0 }
