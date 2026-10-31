package tokenestimate

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"io"
	"strings"
)

// FromMessagesJSON estimates a compact JSON request, replacing image content
// blocks with a fixed token cost. Only message content and nested tool-result
// content are inspected; tools, text, and arbitrary tool arguments remain text.
// The serialized request is never modified.
func FromMessagesJSON(encoded []byte, imageType string, imageTokens int) (int, error) {
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return FromByteCount(len(encoded)), nil
	}
	var messages json.RawMessage
	err := jsonview.Object(encoded, func(k string, v json.RawMessage) error {
		if strings.EqualFold(k, "messages") {
			messages = v
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(messages) == 0 {
		return FromByteCount(len(encoded)), nil
	}
	adjustment, err := MessageArrayAdjustment(messages, imageType, imageTokens)
	return FromByteCount(len(encoded) + adjustment), err
}

// MessageArrayAdjustment returns the image byte adjustment for a prepared
// messages array, independently of the provider's request envelope.
func MessageArrayAdjustment(encoded []byte, imageType string, imageTokens int) (int, error) {
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return 0, nil
	}
	d := jsontext.NewDecoder(bytes.NewBuffer(encoded), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	token, err := d.ReadToken()
	if err != nil {
		return 0, err
	}
	if token.Kind() != '[' {
		return 0, fmt.Errorf("messages must be an array")
	}
	adjustment := 0
	for d.PeekKind() != ']' {
		message, err := d.ReadValue()
		if err != nil {
			return 0, err
		}
		content, _, err := contentFields(message, false)
		if err != nil {
			return 0, err
		}
		adjustment += contentImageAdjustment(content, imageType, imageTokens)
	}
	if _, err = d.ReadToken(); err != nil {
		return 0, err
	}
	if _, err = d.ReadValue(); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("trailing JSON after messages")
		}
		return 0, err
	}
	return adjustment, nil
}

// ReadValue aliases the input bytes.Buffer. Large text/tool contents are scanned
// but never copied into RawMessage fields solely to count image blocks.
func contentFields(raw []byte, block bool) (content []byte, kind string, err error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, "", nil
	}
	d := jsontext.NewDecoder(bytes.NewBuffer(raw), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	token, err := d.ReadToken()
	if err != nil {
		return nil, "", err
	}
	if token.Kind() != '{' {
		return nil, "", fmt.Errorf("message/block must be an object")
	}
	for d.PeekKind() != '}' {
		key, e := d.ReadToken()
		if e != nil {
			return nil, "", e
		}
		name := key.String()
		isContent, isType := strings.EqualFold(name, "content"), strings.EqualFold(name, "type")
		value, e := d.ReadValue()
		if e != nil {
			return nil, "", e
		}
		if isContent {
			end := int(d.InputOffset())
			content = raw[end-len(value) : end]
		}
		if block && isType {
			if e = json.Unmarshal(value, &kind); e != nil {
				return nil, "", e
			}
		}
	}
	_, err = d.ReadToken()
	return content, kind, err
}

func contentImageAdjustment(content []byte, imageType string, imageTokens int) int {
	return contentImageAdjustmentSized(content, imageType, imageTokens, false)
}
func contentImageAdjustmentSized(content []byte, imageType string, imageTokens int, normalize bool) int {
	d := jsontext.NewDecoder(bytes.NewBuffer(content), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	token, err := d.ReadToken()
	if err != nil || token.Kind() != '[' {
		return 0
	}
	adjustment := 0
	for d.PeekKind() != ']' {
		block, err := d.ReadValue()
		if err != nil {
			return 0
		}
		nested, kind, err := contentFields(block, true)
		if err != nil {
			continue
		}
		if kind == imageType {
			size := len(block)
			if normalize {
				if n, err := jsonview.EncodedSize(block); err == nil {
					size = n
				}
			}
			adjustment += ByteBudget(imageTokens) - size
		} else if kind == "tool_result" {
			adjustment += contentImageAdjustmentSized(nested, imageType, imageTokens, normalize)
		}
	}
	return adjustment
}

func messageImageAdjustment(messages []struct {
	Content json.RawMessage `json:"content"`
}, imageType string, imageTokens int) int {
	bytes := 0
	var countContent func(json.RawMessage)
	countContent = func(content json.RawMessage) {
		var blocks []json.RawMessage
		if json.Unmarshal(content, &blocks) != nil {
			return // Plain text content is already included in the byte count.
		}
		for _, block := range blocks {
			var fields struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(block, &fields) != nil {
				continue
			}
			if fields.Type == imageType {
				bytes += ByteBudget(imageTokens) - len(block)
			} else if fields.Type == "tool_result" {
				countContent(fields.Content)
			}
		}
	}
	for _, message := range messages {
		countContent(message.Content)
	}
	return bytes
}

// FromMessageArray estimates an already encoded array without constructing an
// envelope or copying content blocks merely to inspect images.
func FromMessageArray(encoded []byte, imageType string, imageTokens int) (int, error) {
	adjustment, err := MessageArrayAdjustment(encoded, imageType, imageTokens)
	if err != nil {
		return 0, err
	}
	return FromByteCount(len(encoded) + len(`{"messages":}`) + adjustment), nil
}

// FromContentBlocks applies the same estimate to one message content array.
func FromContentBlocks(encoded []byte, imageType string, imageTokens int) int {
	if !json.Valid(encoded) {
		return FromByteCount(len(encoded))
	}
	adjustment := contentImageAdjustment(encoded, imageType, imageTokens)
	return FromByteCount(len(encoded) + len(`{"messages":[{"content":}]}`) + adjustment)
}

// FromLegacyTranscript preserves the old image_content normalization and
// request-envelope size, while borrowing message fields instead of copying and
// re-encoding the entire stored transcript.
func FromLegacyTranscript(raw []byte, imageType string, imageTokens int) (int, error) {
	rows, err := jsonview.Array(raw)
	if err != nil || rows == nil {
		return FromMessagesJSON(raw, imageType, imageTokens)
	}
	return FromRows(rows, imageType, imageTokens, "image_content")
}

// FromRows budgets immutable messages directly. Aliases are normalized in order
// without constructing another complete JSON transcript.
func FromRows(rows []json.RawMessage, imageType string, imageTokens int, aliases ...string) (int, error) {
	size := len(`{"messages":[]}`)
	adjustment := 0
	for i, row := range rows {
		if i > 0 {
			size++
		}
		if bytes.Equal(bytes.TrimSpace(row), []byte("null")) {
			size += 4
			continue
		}
		fields := map[string]json.RawMessage{}
		if err := jsonview.Object(row, func(k string, v json.RawMessage) error { fields[k] = v; return nil }); err != nil {
			return 0, err
		}
		for _, alias := range aliases {
			if value := fields[alias]; len(value) > 0 {
				fields["content"] = value
				delete(fields, alias)
			}
		}
		size += 2
		j := 0
		// The legacy encoder sorts map keys; its case-insensitive content decoder
		// consequently selects the last spelling. Match that choice for adjustment.
		selected := ""
		var content json.RawMessage
		for k, v := range fields {
			if j > 0 {
				size++
			}
			j++
			key, _ := json.Marshal(k)
			n, err := jsonview.EncodedSize(v)
			if err != nil {
				return 0, err
			}
			size += len(key) + 1 + n
			if strings.EqualFold(k, "content") && (content == nil || k > selected) {
				selected = k
				content = v
			}
		}
		adjustment += contentImageAdjustmentSized(content, imageType, imageTokens, true)
	}
	return FromByteCount(size + adjustment), nil
}
