// Package replayjson encodes provider replay data without changing its format.
package replayjson

import "encoding/json"

// Encode returns an immutable representation suitable for storage and recovery.
func Encode(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
