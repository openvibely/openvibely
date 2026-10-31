package tokenestimate

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"strings"
)

// FromMessagesJSON estimates a compact JSON request, replacing image content
// blocks with a fixed token cost. Only message content and nested tool-result
// content are inspected; tools, text, and arbitrary tool arguments remain text.
// The serialized request is never modified.
func FromMessagesJSON(encoded []byte, imageType string, imageTokens int) (int, error) {
	var request struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		return 0, err
	}
	adjustment := messageImageAdjustment(request.Messages, imageType, imageTokens)
	return FromByteCount(len(encoded) + adjustment), nil
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
			adjustment += ByteBudget(imageTokens) - len(block)
		} else if kind == "tool_result" {
			adjustment += contentImageAdjustment(nested, imageType, imageTokens)
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
