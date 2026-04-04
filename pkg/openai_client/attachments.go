package openaiclient

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileAttachment represents a file attached to an OpenAI request.
// Supports image attachments for multimodal input and text files as inline content.
type FileAttachment struct {
	FileName  string
	MediaType string
	Data      []byte
	FilePath  string
}

// supportedMediaTypes maps file extensions to their MIME types.
var supportedMediaTypes = map[string]string{
	// Images
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",

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

	fileName := filepath.Base(filePath)
	mediaType := MediaTypeFromExtension(fileName)
	if mediaType == "" {
		mediaType = mediaTypeFromExtensionLegacy(fileName)
	}
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

// SupportedExtensions returns a list of supported file extensions.
func SupportedExtensions() []string {
	exts := make([]string, 0, len(supportedMediaTypes))
	for ext := range supportedMediaTypes {
		exts = append(exts, ext)
	}
	return exts
}

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
