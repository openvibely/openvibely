// Package transcript defines the persisted item representation independently of
// provider clients and SQLite, so migration and normal reads use the same codec.
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

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
		var items []json.RawMessage
		err := json.Unmarshal(raw, &items)
		return h, items, err
	}
	if raw[0] == '{' {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return h, nil, err
		}
		for _, key := range []string{"responses_input", "messages"} {
			if value, ok := envelope[key]; ok {
				var items []json.RawMessage
				if err := json.Unmarshal(value, &items); err != nil {
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

// JoinReplay is used at a provider/legacy DTO boundary, not at checkpoints.
// Provider request encoding still needs its complete active conversation.
func JoinReplay(h ReplayHeader, items []json.RawMessage) (models.ExecutionReplayMessage, error) {
	m := models.ExecutionReplayMessage{UserContent: h.UserContent, AssistantContent: h.AssistantContent, ReasoningContent: h.ReasoningContent}
	// Items are already JSON records. Assemble them directly instead of asking
	// encoding/json to copy a large array, then copy it again into an envelope.
	var out strings.Builder
	size := 2
	for _, item := range items {
		if !json.Valid(item) {
			return m, fmt.Errorf("invalid replay item JSON")
		}
		size += len(item) + 1
	}
	writeItems := func() {
		out.WriteByte('[')
		for i, item := range items {
			if i != 0 {
				out.WriteByte(',')
			}
			out.Write(item)
		}
		out.WriteByte(']')
	}
	switch h.Format {
	case "none":
		if len(items) != 0 {
			return m, fmt.Errorf("text-only replay has provider items")
		}
	case "array":
		out.Grow(size)
		writeItems()
	case "envelope":
		if h.ItemsKey == "" {
			return m, fmt.Errorf("replay envelope missing item key")
		}
		// Marshal only small envelope metadata, excluding the provider history.
		envelope := make(map[string]json.RawMessage, len(h.Envelope))
		for key, value := range h.Envelope {
			if key != h.ItemsKey {
				envelope[key] = value
			}
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return m, err
		}
		key, err := json.Marshal(h.ItemsKey)
		if err != nil {
			return m, err
		}
		out.Grow(size + len(raw) + len(key) + 2)
		out.Write(raw[:len(raw)-1])
		if len(envelope) > 0 {
			out.WriteByte(',')
		}
		out.Write(key)
		out.WriteByte(':')
		if h.NullItems && len(items) == 0 {
			out.WriteString("null")
		} else {
			writeItems()
		}
		out.WriteByte('}')
	case "opaque":
		if len(items) != 1 {
			return m, fmt.Errorf("opaque replay requires one item")
		}
		out.Write(items[0])
	default:
		return m, fmt.Errorf("unknown replay format %q", h.Format)
	}
	m.TranscriptJSON = out.String()
	return m, nil
}
