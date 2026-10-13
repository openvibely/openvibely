package persistence

import (
	"crypto/rand"
	"encoding/hex"
)

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
