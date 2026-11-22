package geminiclient

import (
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	_ "golang.org/x/image/webp"
)

const fallbackImageTokens = 1122

// estimateRequestTokens counts image inputs separately from serialized text.
// Only protocol media fields are discounted; tool arguments and results remain text.
func estimateRequestTokens(body []byte) int {
	var envelope struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return tokenestimate.FromByteCount(len(body))
	}
	payload := body
	if len(envelope.Request) != 0 {
		payload = envelope.Request
	}
	var req struct {
		Contents          []Content `json:"contents"`
		SystemInstruction *Content  `json:"systemInstruction"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return tokenestimate.FromByteCount(len(body))
	}
	textBytes, mediaTokens := len(body), 0
	addMedia := func(inline *InlineData, file *FileData) {
		if inline != nil && strings.HasPrefix(strings.ToLower(inline.MimeType), "image/") {
			encoded, _ := json.Marshal(inline.Data)
			textBytes -= len(encoded) - 2
			mediaTokens += estimateInlineImageTokens(inline.Data)
		}
		if file != nil && strings.HasPrefix(strings.ToLower(file.MimeType), "image/") {
			mediaTokens += fallbackImageTokens
		}
	}
	contents := req.Contents
	if req.SystemInstruction != nil {
		contents = append(contents, *req.SystemInstruction)
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
