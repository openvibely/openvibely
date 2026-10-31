// Package streamdecode contains allocation-conscious transport framing helpers.
// Callers must finish decoding borrowed data before advancing the scanner.
package streamdecode

import (
	"bufio"
	"bytes"
	"io"
)

func Scanner(r io.Reader, limit int) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, min(64*1024, limit)), limit)
	return s
}

// Data preserves the provider's framing policy: strict requires a leading
// "data: "; permissive accepts whitespace and an optional space after the colon.
func Data(line []byte, strict bool) ([]byte, bool) {
	if strict {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			return nil, false
		}
		return line[len("data: "):], true
	}
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	return bytes.TrimSpace(line[len("data:"):]), true
}
