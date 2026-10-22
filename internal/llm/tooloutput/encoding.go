// Package tooloutput protects display transcript delimiters in tool results.
package tooloutput

import (
	"encoding/base64"
	"strings"
)

const Prefix = "tool-output-base64:"

// Encode leaves ordinary output readable and wraps output containing reserved
// markers. Encode the prefix itself too, so literal encoded-looking output
// survives the renderer's single decode. Provider replay payloads stay raw.
func Encode(output string) string {
	if !strings.Contains(output, "[Tool ") && !strings.Contains(output, "[/Tool]") &&
		!strings.Contains(output, "[Using tool:") && !strings.HasPrefix(strings.TrimSpace(output), Prefix) {
		return output
	}
	return Prefix + base64.StdEncoding.EncodeToString([]byte(output))
}
