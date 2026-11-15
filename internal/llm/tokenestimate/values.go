package tokenestimate

import (
	"bytes"
	"encoding/json"
)

// ContentAdjustment applies the existing image budget to immutable raw blocks
// or decoded content, without encoding a complete message array.
func ContentAdjustment(content any, imageType string, imageTokens int) (int, error) {
	return adjustContentImages(content, imageType, imageTokens, true)
}

func adjustContentImages(content any, imageType string, imageTokens int, normalize bool) (int, error) {
	if !isContentArray(content) {
		return 0, nil
	}
	adjustment := 0
	err := VisitContentBlocks(content, func(block ContentBlock) ([]any, error) {
		nested, kind, err := block.ContentFields()
		if err != nil {
			// Non-content objects are not image blocks.
			return nil, nil
		}
		if kind == imageType {
			size, err := block.Size(normalize)
			if err != nil {
				return nil, err
			}
			adjustment += ByteBudget(imageTokens) - size
		} else if kind == "tool_result" && isContentArray(nested) {
			return []any{nested}, nil
		}
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	return adjustment, nil
}

// Message API content must be a block array; an object-shaped tool payload is
// not itself an image. Responses selects its own envelope traversal separately.
func isContentArray(value any) bool {
	switch v := value.(type) {
	case []any, []map[string]any:
		return true
	case json.RawMessage:
		raw := bytes.TrimSpace(v)
		return len(raw) > 0 && raw[0] == '['
	default:
		return false
	}
}
