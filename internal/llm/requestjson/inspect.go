package requestjson

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"unicode/utf8"
)

func Object(raw []byte, visit func(string, json.RawMessage) error) error {
	return jsonview.Object(raw, visit)
}
func Array(raw []byte) ([]json.RawMessage, error) { return jsonview.Array(raw) }

// PrepareRaw borrows an immutable encoded value. Invalid values use the regular
// encoder so its validation and UTF-8 normalization remain authoritative.
func PrepareRaw(key string, raw json.RawMessage) (*Field, error) {
	if !json.Valid(raw) || !utf8.Valid(raw) {
		return Prepare(key, raw)
	}
	return &Field{key: key, raw: raw}, nil
}

// MarshalArrayField preserves immutable native items on any HTTP/WS request.
func MarshalArrayField(payload map[string]any, key string) ([]byte, error) {
	items, ok := payload[key].([]any)
	if !ok {
		return json.Marshal(payload)
	}
	metadata := make(map[string]any, len(payload))
	for k, v := range payload {
		if k != key {
			metadata[k] = v
		}
	}
	return MarshalItems(metadata, key, items)
}
