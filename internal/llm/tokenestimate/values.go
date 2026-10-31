package tokenestimate

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"strings"
)

// ContentAdjustment applies the existing image budget to immutable raw blocks
// or decoded content, without encoding a complete message array.
func ContentAdjustment(content any, imageType string, imageTokens int) (int, error) {
	if raw, ok := content.(json.RawMessage); ok {
		return contentImageAdjustmentSized(raw, imageType, imageTokens, true), nil
	}
	var blocks []any
	switch v := content.(type) {
	case []any:
		blocks = v
	case []map[string]interface{}:
		for _, block := range v {
			blocks = append(blocks, block)
		}
	default:
		return 0, nil
	}
	adjustment := 0
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		unusual := false
		for key := range block {
			if (strings.EqualFold(key, "type") && key != "type") || (strings.EqualFold(key, "content") && key != "content") {
				unusual = true
			}
		}
		if unusual {
			encoded, err := json.Marshal([]any{block})
			if err != nil {
				return 0, err
			}
			adjustment += contentImageAdjustmentSized(encoded, imageType, imageTokens, true)
			continue
		}
		if block["type"] == imageType {
			size, err := jsonview.Size(block)
			if err != nil {
				return 0, err
			}
			adjustment += ByteBudget(imageTokens) - size
		} else if block["type"] == "tool_result" {
			n, err := ContentAdjustment(block["content"], imageType, imageTokens)
			if err != nil {
				return 0, err
			}
			adjustment += n
		}
	}
	return adjustment, nil
}
