package jsonview

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// ResponseItem owns an immutable view of a large saved Responses message.
// Metadata inspection and retransmission do not materialize its content.
// Callers must not modify the borrowed raw bytes.
type ResponseItem struct {
	raw    json.RawMessage
	fields map[string]json.RawMessage
	header map[string]any
}

func (r *ResponseItem) RawJSON() json.RawMessage     { return r.raw }
func (r *ResponseItem) MarshalJSON() ([]byte, error) { return r.raw, nil }

// Header is read-only and excludes content. Non-message application context
// remains on the ordinary decoded path.
func (r *ResponseItem) Header() map[string]any { return r.header }

// Object returns a fresh editable projection, borrowing large immutable values.
func (r *ResponseItem) Object() (map[string]any, error) {
	return projectMessage(r.fields, bytes.TrimSpace(r.fields["content"]))
}

// SavedResponse keeps large message entries raw. Small entries and unusual
// shapes use the existing projection, avoiding extra indirection for them.
func SavedResponse(raw json.RawMessage) (any, error) {
	if len(raw) < 64*1024 || !utf8.Valid(raw) {
		return ToolOutput(raw)
	}
	fields, err := Fields(raw)
	if err != nil {
		return ToolOutput(raw)
	}
	var typ string
	_ = json.Unmarshal(fields["type"], &typ)
	content := bytes.TrimSpace(fields["content"])
	if typ != "message" || len(content) == 0 || content[0] != '[' {
		return ToolOutput(raw)
	}
	header := make(map[string]any, len(fields)-1)
	for key, value := range fields {
		if key == "content" {
			continue
		}
		var decoded any
		if err = json.Unmarshal(value, &decoded); err != nil {
			return nil, err
		}
		header[key] = decoded
	}
	return &ResponseItem{raw: raw, fields: fields, header: header}, nil
}

func SavedResponses(rows []json.RawMessage) ([]any, error) {
	var items []any
	for _, row := range rows {
		item, err := SavedResponse(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ResponseObject is used by consumers that need content, rather than metadata.
func ResponseObject(value any) (map[string]any, bool) {
	if raw, ok := value.(*ResponseItem); ok {
		if raw == nil {
			return nil, false
		}
		object, err := raw.Object()
		return object, err == nil
	}
	object, ok := value.(map[string]any)
	return object, ok
}

// ResponseHeader is for read-only routing/type checks that do not use content.
func ResponseHeader(value any) (map[string]any, bool) {
	if raw, ok := value.(*ResponseItem); ok {
		if raw == nil {
			return nil, false
		}
		return raw.Header(), true
	}
	object, ok := value.(map[string]any)
	return object, ok
}
