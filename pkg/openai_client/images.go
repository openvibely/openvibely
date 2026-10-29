package openaiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"

	_ "golang.org/x/image/webp"
)

const (
	generatedImageModel   = "gpt-image-2"
	maxImageBytes         = 20 << 20
	maxImageResponseBytes = 32 << 20
	maxImageReferences    = 5
	maxImagePixels        = 64 * 1024 * 1024
)

// GenerateImage mirrors Codex's standalone image backend: generation without
// references, JSON edits with references, and PNG output from gpt-image-2.
// References must be inline PNG, JPEG, or WebP data URLs; remote URLs are not fetched.
func (c *Client) GenerateImage(ctx context.Context, prompt string, references []string, transparentBackground bool) ([]byte, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("image prompt is required")
	}
	if len(references) > maxImageReferences {
		return nil, fmt.Errorf("at most %d reference images are supported", maxImageReferences)
	}
	payload := map[string]any{
		"model": generatedImageModel, "prompt": prompt,
		"background": "auto", "quality": "auto", "size": "auto",
	}
	if transparentBackground {
		payload["background"] = "transparent"
	}
	operation := "generations"
	if len(references) > 0 {
		operation = "edits"
		images := make([]map[string]string, 0, len(references))
		for _, reference := range references {
			if err := validateImageDataURL(reference); err != nil {
				return nil, err
			}
			images = append(images, map[string]string{"image_url": reference})
		}
		payload["images"] = images
	}
	if err := c.ensureValidToken(); err != nil {
		return nil, err
	}
	isOAuth := strings.TrimSpace(c.auth.APIKey) == ""
	base := OpenAIAPIBaseURL
	if isOAuth {
		base = OpenAIChatGPTAPIBaseURL
	}
	endpoint, err := url.Parse(strings.TrimSpace(base))
	if err != nil || endpoint == nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("invalid OpenAI images base URL")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/images/" + operation
	if isOAuth {
		query := endpoint.Query()
		query.Set("client_version", "0.0.0")
		endpoint.RawQuery = query.Encode()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode image request: %w", err)
	}
	resp, err := c.doWithOAuthRecovery(ctx, endpoint.String(), isOAuth, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(req, isOAuth)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("image generation: %w", parseAPIError(resp.StatusCode, data))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image response: %w", err)
	}
	if len(data) > maxImageResponseBytes {
		return nil, fmt.Errorf("image response exceeds size limit")
	}
	var result struct {
		Data []struct {
			Base64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode image response: %w", err)
	}
	if len(result.Data) == 0 || result.Data[0].Base64 == "" {
		return nil, fmt.Errorf("image response contains no image data")
	}
	encoded := result.Data[0].Base64
	if len(encoded) > base64.StdEncoding.EncodedLen(maxImageBytes) {
		return nil, fmt.Errorf("generated image exceeds size limit")
	}
	pngData, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid image base64: %w", err)
	}
	if err := validateImageBytes(pngData, "image/png"); err != nil {
		return nil, err
	}
	// Decode the full PNG before publishing, not just its header.
	if _, err := png.Decode(bytes.NewReader(pngData)); err != nil {
		return nil, fmt.Errorf("invalid generated PNG: %w", err)
	}
	return pngData, nil
}

func validateImageBytes(data []byte, mediaType string) error {
	if len(data) == 0 || len(data) > maxImageBytes {
		return fmt.Errorf("image is empty or exceeds size limit")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid image: %w", err)
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		return fmt.Errorf("reference images must be PNG, JPEG, or WebP")
	}
	if mediaType != "image/"+format {
		return fmt.Errorf("image content does not match its media type")
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxImagePixels {
		return fmt.Errorf("image dimensions exceed size limit")
	}
	return nil
}

func validateImageDataURL(value string) error {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
		return fmt.Errorf("image references must be inline base64 image data")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxImageBytes) {
		return fmt.Errorf("reference image exceeds size limit")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid reference image base64: %w", err)
	}
	return validateImageBytes(data, strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
}
