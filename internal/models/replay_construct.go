package models

import "encoding/json"

// ReplayFromRows retains immutable rows without constructing a serialized history.
// The caller must not mutate row payloads after handing them over. The index is copied.
func ReplayFromRows(key string, items []json.RawMessage) ExecutionReplayMessage {
	rows := &ReplayRows{Format: "array", NullItems: items == nil, Items: append([]json.RawMessage(nil), items...)}
	if key != "" {
		rows.Format = "envelope"
		rows.ItemsKey = key
	}
	return ExecutionReplayMessage{Rows: rows}
}

// EncodeReplayRows freezes each new message once. Unchanged raw rows may be
// passed to ReplayFromRows instead; provider envelopes remain explicit.
func EncodeReplayRows[T any](key string, items []T) (ExecutionReplayMessage, error) {
	var rows []json.RawMessage
	if items != nil {
		rows = make([]json.RawMessage, len(items))
	}
	for i, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return ExecutionReplayMessage{}, err
		}
		rows[i] = raw
	}
	return ReplayFromRows(key, rows), nil
}
