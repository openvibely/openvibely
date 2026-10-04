package openaiclient

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/pkg/attachment"
)

// FileAttachment represents a file attached to an OpenAI request.
// Supports image attachments for multimodal input and text files as inline content.
type FileAttachment struct {
	FileName  string
	MediaType string
	Data      []byte
	FilePath  string
}

// supportedMediaTypes combines the common mappings with OpenAI's BMP support.
var supportedMediaTypes = attachment.MediaTypes(map[string]string{
	".bmp": "image/bmp",
})

// IsSupportedFileType returns true if the file extension is a supported attachment type.
func IsSupportedFileType(filename string) bool {
	return attachment.IsSupportedFileType(filename, supportedMediaTypes)
}

// MediaTypeFromExtension returns the MIME type for a file extension.
// Returns empty string if the extension is not supported.
func MediaTypeFromExtension(filename string) string {
	return attachment.MediaTypeFromExtension(filename, supportedMediaTypes)
}

// IsImageMediaType returns true if the media type is an image type.
func IsImageMediaType(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "image/")
}

// IsTextMediaType returns true if the media type is a text/code type.
func IsTextMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json"
}

// NewFileAttachment creates an attachment from a file path.
// Auto-detects the media type from the extension.
// Returns an error if the file doesn't exist or has an unsupported type.
func NewFileAttachment(filePath string) (*FileAttachment, error) {
	prepared, err := attachment.NewFromPathWithFallback(filePath, supportedMediaTypes, mediaTypeFromExtensionLegacy)
	if err != nil {
		return nil, err
	}

	return &FileAttachment{
		FileName:  prepared.FileName,
		MediaType: prepared.MediaType,
		Data:      prepared.Data,
		FilePath:  prepared.FilePath,
	}, nil
}

// NewFileAttachmentFromBytes creates a FileAttachment from raw bytes.
func NewFileAttachmentFromBytes(fileName string, mediaType string, data []byte) (*FileAttachment, error) {
	prepared, err := attachment.NewFromBytes(fileName, mediaType, data, supportedMediaTypes)
	if err != nil {
		return nil, err
	}
	return &FileAttachment{
		FileName:  prepared.FileName,
		MediaType: prepared.MediaType,
		Data:      prepared.Data,
		FilePath:  prepared.FilePath,
	}, nil
}

// UnsupportedFileTypeError is returned when a file has an unsupported extension.
type UnsupportedFileTypeError = attachment.UnsupportedFileTypeError

// SupportedExtensions returns a list of supported file extensions.
func SupportedExtensions() []string {
	exts := make([]string, 0, len(supportedMediaTypes))
	for ext := range supportedMediaTypes {
		exts = append(exts, ext)
	}
	return exts
}

func (f *FileAttachment) loadData() ([]byte, error) {
	return attachment.LoadData(f.FileName, f.FilePath, f.Data)
}

func (f *FileAttachment) toInputContent() (map[string]any, error) {
	if IsImageMediaType(f.MediaType) {
		data, err := f.loadData()
		if err != nil {
			return nil, err
		}
		dataURL := fmt.Sprintf("data:%s;base64,%s", f.MediaType, base64.StdEncoding.EncodeToString(data))
		return map[string]any{
			"type":      "input_image",
			"image_url": dataURL,
			"detail":    "auto",
		}, nil
	}

	if IsTextMediaType(f.MediaType) {
		data, err := f.loadData()
		if err != nil {
			return nil, err
		}
		const maxTextSize = 100 * 1024 // 100KB
		if len(data) > maxTextSize {
			return nil, fmt.Errorf("text file %s exceeds maximum size (%d bytes, max %d)", f.FileName, len(data), maxTextSize)
		}
		// Include text files as input_text blocks
		return map[string]any{
			"type": "input_text",
			"text": fmt.Sprintf("--- File: %s ---\n%s\n--- End of %s ---", f.FileName, string(data), f.FileName),
		}, nil
	}

	return nil, fmt.Errorf("unsupported attachment media type %q", f.MediaType)
}

// mediaTypeFromExtensionLegacy is the old simple lookup for backwards compatibility.
func mediaTypeFromExtensionLegacy(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}
