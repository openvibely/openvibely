package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// ReplayBuilder assembles validated persisted JSON directly at the provider DTO
// boundary without retaining a second array of per-item JSON copies.
type ReplayBuilder struct {
	header ReplayHeader
	out    strings.Builder
	count  int
}

func NewReplayBuilder(header ReplayHeader, capacity int) (*ReplayBuilder, error) {
	b := &ReplayBuilder{header: header}
	b.out.Grow(capacity)
	switch header.Format {
	case "array":
		b.out.WriteByte('[')
	case "envelope":
		if header.ItemsKey == "" {
			return nil, fmt.Errorf("replay envelope missing item key")
		}
		envelope := make(map[string]json.RawMessage, len(header.Envelope))
		for key, value := range header.Envelope {
			if key != header.ItemsKey {
				envelope[key] = value
			}
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		key, err := json.Marshal(header.ItemsKey)
		if err != nil {
			return nil, err
		}
		b.out.Write(raw[:len(raw)-1])
		if len(envelope) > 0 {
			b.out.WriteByte(',')
		}
		b.out.Write(key)
		b.out.WriteByte(':')
		b.out.WriteByte('[')
	case "none", "opaque":
	default:
		return nil, fmt.Errorf("unknown replay format %q", header.Format)
	}
	return b, nil
}

// AppendValidatedJSON accepts records whose store has already validated JSON.
// SQLite enforces json_valid(payload); validating again would allocate an extra
// byte copy of every provider item before the provider's own decode.
func (b *ReplayBuilder) AppendValidatedJSON(raw string) error {
	if b.header.Format == "none" || (b.header.Format == "opaque" && b.count > 0) {
		return fmt.Errorf("unexpected replay item")
	}
	if b.count > 0 {
		b.out.WriteByte(',')
	}
	b.out.WriteString(raw)
	b.count++
	return nil
}

func (b *ReplayBuilder) Finish() (models.ExecutionReplayMessage, error) {
	h := b.header
	switch h.Format {
	case "array":
		b.out.WriteByte(']')
	case "envelope":
		b.out.WriteString("]}")
	case "opaque":
		if b.count != 1 {
			return models.ExecutionReplayMessage{}, fmt.Errorf("opaque replay requires one item")
		}
	}
	// A null envelope array is uncommon and has no large payload to copy.
	if h.Format == "envelope" && h.NullItems && b.count == 0 {
		return JoinReplay(h, nil)
	}
	return models.ExecutionReplayMessage{UserContent: h.UserContent, AssistantContent: h.AssistantContent, ReasoningContent: h.ReasoningContent, TranscriptJSON: b.out.String()}, nil
}
