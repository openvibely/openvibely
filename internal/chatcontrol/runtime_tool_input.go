package chatcontrol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeRuntimeToolInput normalizes empty tool arguments to an empty JSON object
// and decodes the result into dst.
func DecodeRuntimeToolInput(input json.RawMessage, dst any) error {
	payload := input
	if len(strings.TrimSpace(string(payload))) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("invalid tool input JSON: %w", err)
	}
	return nil
}
