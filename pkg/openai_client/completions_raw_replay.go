package openaiclient

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
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
