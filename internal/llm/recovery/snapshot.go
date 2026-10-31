// Package recovery stores immutable per-message failure snapshots. Consumers
// can inspect rows without constructing and decoding a whole conversation JSON.
package recovery

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

type Snapshot struct{ rows []json.RawMessage }

// Capture takes ownership of freshly encoded or immutable borrowed message rows.
func Capture[T any](values []T, encode func(T) ([]byte, error)) (*Snapshot, error) {
	s := &Snapshot{rows: make([]json.RawMessage, 0, len(values))}
	for _, v := range values {
		raw, err := encode(v)
		if err != nil {
			return nil, err
		}
		s.rows = append(s.rows, raw)
	}
	return s, nil
}

// Rows returns an independent index over immutable payloads. legacy preserves
// errors constructed by older integrations; only that path needs array parsing.
func (s *Snapshot) Rows(legacy string) ([]json.RawMessage, error) {
	if legacy != "" {
		return jsonview.Array([]byte(legacy))
	}
	if s == nil {
		return nil, nil
	}
	return append([]json.RawMessage(nil), s.rows...), nil
}
func (s *Snapshot) JSON(legacy string) string {
	if legacy != "" {
		return legacy
	}
	if s == nil {
		return ""
	}
	out := []byte{'['}
	for i, row := range s.rows {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, row...)
	}
	return string(append(out, ']'))
}
