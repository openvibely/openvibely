package tokenestimate

import "unicode/utf8"

const approxBytesPerToken = 4

// FromText returns a tokenizer-free estimate based on UTF-8 byte length.
func FromText(text string) int {
	return FromByteCount(len(text))
}

// FromByteCount rounds a byte count up to the approximate token count.
func FromByteCount(byteCount int) int {
	if byteCount <= 0 {
		return 0
	}
	return 1 + (byteCount-1)/approxBytesPerToken
}

// ByteBudget converts an approximate token budget to its byte budget.
func ByteBudget(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * approxBytesPerToken
}

// TruncateMiddle bounds text by UTF-8 bytes while preserving both ends.
func TruncateMiddle(text string, byteBudget int, marker string) string {
	return TruncateMiddleWithMarker(text, byteBudget, func(int, int) string { return marker })
}

// TruncateMiddleWithMarker builds an omission marker from the removed byte and
// rune counts, then bounds the complete result to byteBudget.
func TruncateMiddleWithMarker(text string, byteBudget int, markerForRemoval func(removedBytes, removedRunes int) string) string {
	if byteBudget <= 0 || text == "" {
		return ""
	}
	if len(text) <= byteBudget {
		return text
	}
	marker := markerForRemoval(len(text)-byteBudget, utf8.RuneCountInString(text))
	for range 4 {
		if len(marker) >= byteBudget {
			prefix, _, _, _ := splitMiddle(text, byteBudget, 0)
			return prefix
		}
		contentBudget := byteBudget - len(marker)
		prefix, suffix, removedBytes, removedRunes := splitMiddle(text, contentBudget/2, contentBudget-contentBudget/2)
		nextMarker := markerForRemoval(removedBytes, removedRunes)
		if len(nextMarker) == len(marker) {
			return prefix + nextMarker + suffix
		}
		marker = nextMarker
	}
	contentBudget := byteBudget - len(marker)
	prefix, suffix, _, _ := splitMiddle(text, contentBudget/2, contentBudget-contentBudget/2)
	return prefix + marker + suffix
}

func splitMiddle(text string, prefixBudget, suffixBudget int) (prefix, suffix string, removedBytes, removedRunes int) {
	prefixEnd := 0
	for i, r := range text {
		next := i + len(string(r))
		if next > prefixBudget {
			break
		}
		prefixEnd = next
	}
	suffixStart := len(text)
	used := 0
	for i := len(text); i > prefixEnd; {
		_, size := utf8.DecodeLastRuneInString(text[:i])
		if used+size > suffixBudget {
			break
		}
		i -= size
		suffixStart = i
		used += size
	}
	removed := text[prefixEnd:suffixStart]
	return text[:prefixEnd], text[suffixStart:], len(removed), utf8.RuneCountInString(removed)
}
