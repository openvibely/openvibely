package geminiclient

import (
	"encoding/base64"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	_ "golang.org/x/image/webp"
)

const fallbackImageTokens = 1122

// EstimateContentsTokens budgets replay contents using the request image estimates.
func EstimateContentsTokens(contents []Content) (int, error) {
	var values []any
	if contents != nil {
		values = make([]any, len(contents))
	}
	for i, c := range contents {
		values[i] = contentValue(c)
	}
	size, err := requestjson.Size(values)
	if err != nil {
		return 0, err
	}
	return estimateContentTokens(size, contents), nil
}

func estimateContentTokens(textBytes int, contents []Content) int {
	mediaTokens := 0
	addMedia := func(inline *InlineData, file *FileData) {
		if inline != nil && strings.HasPrefix(strings.ToLower(inline.MimeType), "image/") {
			textBytes -= requestjson.StringSize(inline.Data) - 2
			mediaTokens += estimateInlineImageTokens(inline.Data)
		}
		if file != nil && (file.MimeType == "" || strings.HasPrefix(strings.ToLower(file.MimeType), "image/")) {
			mediaTokens += fallbackImageTokens
		}
	}
	for _, content := range contents {
		for _, part := range content.Parts {
			addMedia(part.InlineData, part.FileData)
			if part.FunctionResponse != nil {
				for _, media := range part.FunctionResponse.Parts {
					addMedia(media.InlineData, media.FileData)
				}
			}
		}
	}
	return tokenestimate.FromByteCount(max(0, textBytes)) + mediaTokens
}

func estimateInlineImageTokens(data string) int {
	config, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return fallbackImageTokens
	}
	// Divide before converting to int so large dimensions cannot wrap the estimate.
	pixels := uint64(config.Width) * uint64(config.Height)
	tokens := pixels / 750
	maxInt := uint64(^uint(0) >> 1)
	if tokens > maxInt {
		return int(maxInt)
	}
	return max(fallbackImageTokens, int(tokens))
}
