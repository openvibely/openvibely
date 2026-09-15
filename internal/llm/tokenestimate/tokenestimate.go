package tokenestimate

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
