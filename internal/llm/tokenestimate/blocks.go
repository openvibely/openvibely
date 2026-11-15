package tokenestimate

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

// ContentBlock is a borrowed JSON object or an in-memory content block. Visitors
// must not modify it. Raw blocks are inspected without decoding the full object.
type ContentBlock struct {
	value map[string]any
	raw   json.RawMessage
}

// DecodedObject returns an existing in-memory object. It does not decode raw
// JSON; providers that consume encoded blocks use ContentFields and Size instead.
func (b ContentBlock) DecodedObject() (map[string]any, bool) {
	return b.value, b.raw == nil
}

// ContentFields uses the message APIs' case-insensitive field-name rules.
func (b ContentBlock) ContentFields() (any, string, error) {
	if b.raw != nil {
		content, kind, err := contentFields(b.raw, true)
		return json.RawMessage(content), kind, err
	}
	// Preserve the JSON encoder's ordering for unusual field spellings.
	for key := range b.value {
		if (strings.EqualFold(key, "type") && key != "type") || (strings.EqualFold(key, "content") && key != "content") {
			raw, err := json.Marshal(b.value)
			if err != nil {
				return nil, "", err
			}
			return (ContentBlock{raw: raw}).ContentFields()
		}
	}
	kind, _ := b.value["type"].(string)
	return b.value["content"], kind, nil
}

// Size preserves either the raw wire size or the size after JSON encoding.
func (b ContentBlock) Size(normalize bool) (int, error) {
	if b.raw == nil {
		return jsonview.Size(b.value)
	}
	if normalize {
		return jsonview.EncodedSize(b.raw)
	}
	return len(b.raw), nil
}

// VisitContentBlocks handles Go block arrays and raw JSON uniformly. The visitor
// chooses which children to inspect: message APIs descend only into tool-result
// content, while Responses has its own envelope rules. Strings are never parsed
// as JSON. Raw arrays are scanned without copying large image payloads.
func VisitContentBlocks(value any, visit func(ContentBlock) ([]any, error)) error {
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		if depth >= 10000 {
			return fmt.Errorf("content nesting exceeds limit")
		}
		block := ContentBlock{}
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				if err := walk(item, depth+1); err != nil {
					return err
				}
			}
			return nil
		case []map[string]any:
			for _, item := range v {
				if err := walk(item, depth+1); err != nil {
					return err
				}
			}
			return nil
		case map[string]any:
			block.value = v
		case json.RawMessage:
			raw := bytes.TrimSpace(v)
			if len(raw) == 0 {
				return nil
			}
			if raw[0] == '[' {
				d := jsontext.NewDecoder(bytes.NewBuffer(raw), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
				if _, err := d.ReadToken(); err != nil {
					return err
				}
				for d.PeekKind() != ']' {
					item, err := d.ReadValue()
					if err != nil {
						return err
					}
					if err := walk(json.RawMessage(item), depth+1); err != nil {
						return err
					}
				}
				_, err := d.ReadToken()
				return err
			}
			if raw[0] != '{' {
				return nil
			}
			block.raw = raw
		default:
			return nil
		}
		children, err := visit(block)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(value, 0)
}
