package models

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxTaskTagLength = 64

var ErrInvalidTaskTag = errors.New("tag must be valid text of at most 64 characters without control or formatting characters")

// NormalizeTaskTag defines the shared write and filter contract. Empty means untagged.
func NormalizeTaskTag(value string) (TaskTag, error) {
	if !utf8.ValidString(value) {
		return "", ErrInvalidTaskTag
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", ErrInvalidTaskTag
		}
	}
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > MaxTaskTagLength {
		return "", ErrInvalidTaskTag
	}
	// Built-in choices have stable IDs. Custom tags keep their display spelling.
	for _, tag := range []TaskTag{TagFeature, TagBug} {
		if strings.EqualFold(value, string(tag)) {
			return tag, nil
		}
	}
	return TaskTag(value), nil
}
