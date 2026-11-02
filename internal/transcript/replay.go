// Package transcript defines the persisted item representation independently of
// provider clients and SQLite, so migration and normal reads use the same codec.
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/llm/jsonview"

	"github.com/openvibely/openvibely/internal/models"
)

// ReplayHeader contains envelope and interpretation metadata, never a serialized
// conversation. An empty ItemsKey denotes an array; "none" is a text-only frame.
// Unknown valid JSON is preserved as one opaque item for lossless migration.
type ReplayHeader struct {
	Format                 string                     `json:"format"`
	ItemsKey               string                     `json:"items_key,omitempty"`
	NullItems              bool                       `json:"null_items,omitempty"`
	Envelope               map[string]json.RawMessage `json:"envelope,omitempty"`
	UserContent            string                     `json:"user_content,omitempty"`
	AssistantContent       string                     `json:"assistant_content,omitempty"`
	ReasoningContent       string                     `json:"reasoning_content,omitempty"`
	ReasoningAuthoritative bool                       `json:"reasoning_authoritative,omitempty"`
}

func SplitReplay(message models.ExecutionReplayMessage) (ReplayHeader, []json.RawMessage, error) {
	h := ReplayHeader{UserContent: message.UserContent, AssistantContent: message.AssistantContent, ReasoningContent: message.ReasoningContent}
	if message.TranscriptJSON == "" && message.Rows != nil {
		r := message.Rows
		h.Format, h.ItemsKey, h.NullItems, h.Envelope = r.Format, r.ItemsKey, r.NullItems, r.Envelope
		return h, r.Items, nil
	}
	raw := bytes.TrimSpace([]byte(message.FullTranscriptJSON()))
	if len(raw) == 0 {
		h.Format = "none"
		return h, nil, nil
	}
	if !json.Valid(raw) {
		return h, nil, fmt.Errorf("invalid replay JSON")
	}
	if raw[0] == '[' {
		h.Format = "array"
		items, err := jsonview.Array(raw)
		return h, items, err
	}
	if raw[0] == '{' {
		envelope := map[string]json.RawMessage{}
		if err := jsonview.Object(raw, func(k string, v json.RawMessage) error { envelope[k] = v; return nil }); err != nil {
			return h, nil, err
		}
		for _, key := range []string{"responses_input", "responses_delta", "messages"} {
			if value, ok := envelope[key]; ok {
				items, err := jsonview.Array(value)
				if err != nil {
					return h, nil, fmt.Errorf("replay %s: %w", key, err)
				}
				h.NullItems = bytes.Equal(bytes.TrimSpace(value), []byte("null"))
				delete(envelope, key)
				h.Format = "envelope"
				h.ItemsKey = key
				h.Envelope = envelope
				return h, items, nil
			}
		}
	}
	h.Format = "opaque"
	return h, []json.RawMessage{append(json.RawMessage(nil), raw...)}, nil
}

// JoinReplay is the serialized compatibility boundary. Restoration can retain
// the same validated immutable rows with JoinReplayRows instead.
func JoinReplay(h ReplayHeader, items []json.RawMessage) (models.ExecutionReplayMessage, error) {
	m, err := JoinReplayRows(h, items)
	if err == nil {
		m.TranscriptJSON = m.FullTranscriptJSON()
		m.Rows = nil
	}
	return m, err
}
func JoinReplayRows(h ReplayHeader, items []json.RawMessage) (models.ExecutionReplayMessage, error) {
	m := models.ExecutionReplayMessage{UserContent: h.UserContent, AssistantContent: h.AssistantContent, ReasoningContent: h.ReasoningContent}
	for _, item := range items {
		if !json.Valid(item) {
			return m, fmt.Errorf("invalid replay item JSON")
		}
	}
	var envelope map[string]json.RawMessage
	switch h.Format {
	case "none":
		if len(items) != 0 {
			return m, fmt.Errorf("text-only replay has provider items")
		}
	case "array":
	case "envelope":
		if h.ItemsKey == "" {
			return m, fmt.Errorf("replay envelope missing item key")
		}
		envelope = make(map[string]json.RawMessage, len(h.Envelope))
		for key, value := range h.Envelope {
			if key != h.ItemsKey {
				envelope[key] = value
			}
		}
		for _, raw := range envelope {
			if raw != nil && !json.Valid(raw) {
				return m, fmt.Errorf("invalid replay envelope JSON")
			}
		}
	case "opaque":
		if len(items) != 1 {
			return m, fmt.Errorf("opaque replay requires one item")
		}
	default:
		return m, fmt.Errorf("unknown replay format %q", h.Format)
	}
	m.Rows = &models.ReplayRows{Format: h.Format, ItemsKey: h.ItemsKey, Envelope: envelope, Items: items, NullItems: h.NullItems && h.Format == "envelope"}
	return m, nil
}
