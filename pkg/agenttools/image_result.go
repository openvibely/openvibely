package agenttools

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// ImageResultPrefix keeps image results distinguishable from ordinary JSON tool output
// through the existing durable string transcript. Providers decode before dispatch.
const ImageResultPrefix = "\x1ecomputer-images-v1:"

type ToolImage struct {
	Type string `json:"type"`
	MIME string `json:"mimeType"`
	Data string `json:"data"`
}
type ImageResult struct {
	Content []ToolImage `json:"content"`
}

func EncodeImageResult(raw []byte) (string, error) {
	result, ok := decodeImages(raw)
	if !ok {
		return "", errors.New("computer returned an invalid screenshot")
	}
	b, err := json.Marshal(result)
	return ImageResultPrefix + string(b), err
}
func DecodeImageResult(raw string) (ImageResult, bool) {
	if !strings.HasPrefix(raw, ImageResultPrefix) {
		return ImageResult{}, false
	}
	return decodeImages([]byte(strings.TrimPrefix(raw, ImageResultPrefix)))
}
func decodeImages(raw []byte) (ImageResult, bool) {
	var result ImageResult
	if len(raw) > 12_000_000 || json.Unmarshal(raw, &result) != nil || len(result.Content) == 0 || len(result.Content) > 4 {
		return result, false
	}
	for _, img := range result.Content {
		if img.Type != "image" || (img.MIME != "image/png" && img.MIME != "image/jpeg" && img.MIME != "image/webp") {
			return ImageResult{}, false
		}
		data, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil || http.DetectContentType(data) != img.MIME {
			return ImageResult{}, false
		}
	}
	return result, true
}
func ImageResultSummary(raw string) string {
	if strings.HasPrefix(raw, ImageResultPrefix) {
		return "Project computer screenshot captured."
	}
	return raw
}
func ResponsesImageOutput(raw string) any {
	result, ok := DecodeImageResult(raw)
	if !ok {
		return raw
	}
	blocks := []map[string]any{{"type": "input_text", "text": "Current project computer screenshot. Inspect before continuing."}}
	for _, img := range result.Content {
		blocks = append(blocks, map[string]any{"type": "input_image", "image_url": "data:" + img.MIME + ";base64," + img.Data})
	}
	return blocks
}
func AnthropicImageOutput(raw string) json.RawMessage {
	result, ok := DecodeImageResult(raw)
	if !ok {
		b, _ := json.Marshal(raw)
		return b
	}
	blocks := []map[string]any{{"type": "text", "text": "Current project computer screenshot. Inspect before continuing."}}
	for _, img := range result.Content {
		blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": img.MIME, "data": img.Data}})
	}
	b, _ := json.Marshal(blocks)
	return b
}
