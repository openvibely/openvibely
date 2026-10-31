package openai_compatible

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	"unicode/utf8"
)

// EstimateHistoryTokens uses the same replay representation as the adapter,
// avoiding duplicate execution fields and counting images as images, not base64 text.
func EstimateHistoryTokens(history []models.Execution) int {
	return EstimateHistoryTokensContext(context.Background(), history)
}

func EstimateHistoryTokensContext(ctx context.Context, history []models.Execution) int {
	messages := preparedhistory.Read(ctx, history, buildClientHistory)
	if len(messages) == 0 {
		return 0
	}
	// Text-only messages can be counted without encoding and then decoding all
	// their text again. Keep multimodal/nested image semantics on the existing path.
	textOnly := true
	for _, message := range messages {
		if len(message.ImageContent) > 0 {
			textOnly = false
			break
		}
	}
	if textOnly {
		size := len(`{"messages":[]}`) + len(messages) - 1
		for _, message := range messages {
			content, reasoning := message.Content, message.ReasoningContent
			message.Content = ""
			if reasoning != "" {
				message.ReasoningContent = "x"
			}
			raw, err := json.Marshal(message)
			if err != nil {
				return 0
			}
			size += len(raw) + jsonStringBytes(content) - 2
			if reasoning != "" {
				size += jsonStringBytes(reasoning) - 3
			}
		}
		return tokenestimate.FromByteCount(size)
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		return 0
	}
	tokens, _ := tokenestimate.FromMessageArray(raw, "image_url", 2000)
	return tokens
}

// Match encoding/json's default string escaping, including HTML, invalid UTF-8,
// and JavaScript line separators. Count bytes without allocating escaped text.
func jsonStringBytes(s string) int {
	n := 2
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				n += 2
			case '<', '>', '&':
				n += 6
			default:
				if c < 0x20 {
					n += 6
				} else {
					n++
				}
			}
			i++
			continue
		}
		r, width := utf8.DecodeRuneInString(s[i:])
		if width == 1 {
			// Let the encoder normalize malformed UTF-8; its replacement spelling
			// can differ between JSON implementations. Valid text stays allocation-free.
			raw, _ := json.Marshal(s)
			return len(raw)
		}
		if r == '\u2028' || r == '\u2029' {
			n += 6
		} else {
			n += width
		}
		i += width
	}
	return n
}
