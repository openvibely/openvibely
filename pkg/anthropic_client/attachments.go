package anthropicclient

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/pkg/attachment"
)

// FileAttachment represents a file to be attached to an API request.
// Files are sent as multimodal content blocks (images as base64 image blocks,
// PDFs as document blocks, text/code files as inline text blocks).
type FileAttachment struct {
	// FileName is the display name for the file.
	FileName string

	// MediaType is the MIME type (e.g., "image/png", "application/pdf", "text/plain").
	// If empty, it is auto-detected from the file extension.
	MediaType string

	// Data is the raw file content. If nil, FilePath must be set.
	Data []byte

	// FilePath is the path to the file on disk. Used if Data is nil.
	FilePath string
}

// supportedMediaTypes combines the common mappings with Anthropic's PDF support.
var supportedMediaTypes = attachment.MediaTypes(map[string]string{
	".pdf": "application/pdf",
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
	return strings.HasPrefix(mediaType, "image/")
}

// IsDocumentMediaType returns true if the media type is a document type (PDF).
func IsDocumentMediaType(mediaType string) bool {
	return mediaType == "application/pdf"
}

// IsTextMediaType returns true if the media type is a text/code type.
func IsTextMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json"
}

// NewFileAttachment creates a FileAttachment from a file path.
// It auto-detects the media type from the extension if not provided.
// Returns an error if the file doesn't exist or has an unsupported type.
func NewFileAttachment(filePath string) (*FileAttachment, error) {
	prepared, err := attachment.NewFromPath(filePath, supportedMediaTypes)
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
// mediaType must be provided when creating from bytes.
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

// loadData reads the file data if not already loaded.
func (f *FileAttachment) loadData() ([]byte, error) {
	return attachment.LoadData(f.FileName, f.FilePath, f.Data)
}

// toContentBlock converts the attachment to an Anthropic API content block.
// Images become base64 image_source blocks, PDFs become document blocks,
// and text/code files become text blocks with the file content.
func (f *FileAttachment) toContentBlock() (map[string]interface{}, error) {
	data, err := f.loadData()
	if err != nil {
		return nil, err
	}

	if IsImageMediaType(f.MediaType) {
		return map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": f.MediaType,
				"data":       base64.StdEncoding.EncodeToString(data),
			},
		}, nil
	}

	if IsDocumentMediaType(f.MediaType) {
		return map[string]interface{}{
			"type": "document",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": f.MediaType,
				"data":       base64.StdEncoding.EncodeToString(data),
			},
		}, nil
	}

	// Text/code files: include as text blocks
	// Enforce a size limit to avoid token overflow
	const maxTextSize = 100 * 1024 // 100KB
	if len(data) > maxTextSize {
		return nil, fmt.Errorf("text file %s exceeds maximum size (%d bytes, max %d)", f.FileName, len(data), maxTextSize)
	}

	return map[string]interface{}{
		"type": "text",
		"text": fmt.Sprintf("--- File: %s ---\n%s\n--- End of %s ---", f.FileName, string(data), f.FileName),
	}, nil
}

// UnsupportedFileTypeError is returned when a file has an unsupported extension.
type UnsupportedFileTypeError = attachment.UnsupportedFileTypeError

// SupportedExtensions returns a sorted list of supported file extensions.
func SupportedExtensions() []string {
	exts := make([]string, 0, len(supportedMediaTypes))
	for ext := range supportedMediaTypes {
		exts = append(exts, ext)
	}
	return exts
}

// buildContentBlocks converts a text prompt and file attachments into the
// content array format expected by the Anthropic Messages API.
// Returns a slice of content block maps suitable for JSON marshaling.
func buildContentBlocks(text string, attachments []*FileAttachment) ([]map[string]interface{}, error) {
	blocks := make([]map[string]interface{}, 0, 1+len(attachments))

	// Add text block
	if text != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": text,
		})
	}

	// Add attachment blocks
	for _, att := range attachments {
		block, err := att.toContentBlock()
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", att.FileName, err)
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}
