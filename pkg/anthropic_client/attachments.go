package anthropicclient

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// supportedMediaTypes maps file extensions to their MIME types.
// Only these file types are accepted for attachment.
var supportedMediaTypes = map[string]string{
	// Images
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",

	// Documents
	".pdf": "application/pdf",

	// Code / text files
	".txt":  "text/plain",
	".md":   "text/markdown",
	".go":   "text/x-go",
	".py":   "text/x-python",
	".js":   "text/javascript",
	".ts":   "text/typescript",
	".jsx":  "text/javascript",
	".tsx":  "text/typescript",
	".rs":   "text/x-rust",
	".rb":   "text/x-ruby",
	".java": "text/x-java",
	".c":    "text/x-c",
	".cpp":  "text/x-c++",
	".h":    "text/x-c",
	".hpp":  "text/x-c++",
	".cs":   "text/x-csharp",
	".html": "text/html",
	".css":  "text/css",
	".xml":  "text/xml",
	".json": "application/json",
	".yaml": "text/yaml",
	".yml":  "text/yaml",
	".toml": "text/toml",
	".sql":  "text/x-sql",
	".sh":   "text/x-sh",
	".bash": "text/x-sh",
	".zsh":  "text/x-sh",
	".csv":  "text/csv",
	".log":  "text/plain",
	".env":  "text/plain",
	".cfg":  "text/plain",
	".ini":  "text/plain",
	".conf": "text/plain",
}

// IsSupportedFileType returns true if the file extension is a supported attachment type.
func IsSupportedFileType(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	_, ok := supportedMediaTypes[ext]
	return ok
}

// MediaTypeFromExtension returns the MIME type for a file extension.
// Returns empty string if the extension is not supported.
func MediaTypeFromExtension(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	return supportedMediaTypes[ext]
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
	// Check file exists
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", filePath)
		}
		return nil, fmt.Errorf("stat file %s: %w", filePath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("cannot attach directory: %s", filePath)
	}

	// Detect media type
	fileName := filepath.Base(filePath)
	mediaType := MediaTypeFromExtension(fileName)
	if mediaType == "" {
		return nil, &UnsupportedFileTypeError{FileName: fileName, Extension: strings.ToLower(filepath.Ext(fileName))}
	}

	return &FileAttachment{
		FileName:  fileName,
		MediaType: mediaType,
		FilePath:  filePath,
	}, nil
}

// NewFileAttachmentFromBytes creates a FileAttachment from raw bytes.
// mediaType must be provided when creating from bytes.
func NewFileAttachmentFromBytes(fileName string, mediaType string, data []byte) (*FileAttachment, error) {
	if fileName == "" {
		return nil, fmt.Errorf("fileName is required")
	}
	if mediaType == "" {
		mediaType = MediaTypeFromExtension(fileName)
		if mediaType == "" {
			return nil, &UnsupportedFileTypeError{FileName: fileName, Extension: strings.ToLower(filepath.Ext(fileName))}
		}
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty file data for %s", fileName)
	}

	return &FileAttachment{
		FileName:  fileName,
		MediaType: mediaType,
		Data:      data,
	}, nil
}

// loadData reads the file data if not already loaded.
func (f *FileAttachment) loadData() ([]byte, error) {
	if f.Data != nil {
		return f.Data, nil
	}
	if f.FilePath == "" {
		return nil, fmt.Errorf("no data or file path for attachment %s", f.FileName)
	}
	data, err := os.ReadFile(f.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read attachment %s: %w", f.FilePath, err)
	}
	return data, nil
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
type UnsupportedFileTypeError struct {
	FileName  string
	Extension string
}

func (e *UnsupportedFileTypeError) Error() string {
	if e.Extension == "" {
		return fmt.Sprintf("unsupported file type: %s (no extension)", e.FileName)
	}
	return fmt.Sprintf("unsupported file type: %s (extension %s)", e.FileName, e.Extension)
}

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
