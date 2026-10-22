// Package tooloutput protects display transcript delimiters in tool results.
package tooloutput

import "strings"

var escape = strings.NewReplacer("&", "&amp;", "[", "&#91;")

// Encode escapes display output only; provider replay payloads stay raw.
func Encode(output string) string {
	return escape.Replace(output)
}
