package handler

import "unicode/utf8"

// chatTitlePreview preserves the 50-byte preview limit without splitting a UTF-8 character.
func chatTitlePreview(message string) string {
	const maxBytes = 50
	if len(message) <= maxBytes {
		return message
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(message[end]) {
		end--
	}
	return message[:end]
}
