package attachment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prepared contains provider-independent attachment metadata and content source.
type Prepared struct {
	FileName  string
	MediaType string
	Data      []byte
	FilePath  string
}

var commonMediaTypes = map[string]string{
	// Images
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",

	// Code and text files
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

// MediaTypes returns the shared mappings plus the provider-specific mappings.
func MediaTypes(additional map[string]string) map[string]string {
	mediaTypes := make(map[string]string, len(commonMediaTypes)+len(additional))
	for extension, mediaType := range commonMediaTypes {
		mediaTypes[extension] = mediaType
	}
	for extension, mediaType := range additional {
		mediaTypes[extension] = mediaType
	}
	return mediaTypes
}

// MediaTypeFromExtension returns the MIME type mapped to filename's extension.
func MediaTypeFromExtension(filename string, mediaTypes map[string]string) string {
	return mediaTypes[strings.ToLower(filepath.Ext(filename))]
}

// IsSupportedFileType reports whether filename's extension is in mediaTypes.
func IsSupportedFileType(filename string, mediaTypes map[string]string) bool {
	return MediaTypeFromExtension(filename, mediaTypes) != ""
}

// NewFromPath validates a path and prepares its metadata without reading content.
func NewFromPath(filePath string, mediaTypes map[string]string) (*Prepared, error) {
	return NewFromPathWithFallback(filePath, mediaTypes, nil)
}

// NewFromPathWithFallback also tries fallback when the extension is not in mediaTypes.
func NewFromPathWithFallback(filePath string, mediaTypes map[string]string, fallback func(string) string) (*Prepared, error) {
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
	mediaType := MediaTypeFromExtension(fileName, mediaTypes)
	if mediaType == "" && fallback != nil {
		mediaType = fallback(fileName)
	}
	if mediaType == "" {
		return nil, &UnsupportedFileTypeError{FileName: fileName, Extension: Extension(fileName)}
	}

	return &Prepared{FileName: fileName, MediaType: mediaType, FilePath: filePath}, nil
}

// NewFromBytes validates and prepares an attachment from in-memory data.
func NewFromBytes(fileName, mediaType string, data []byte, mediaTypes map[string]string) (*Prepared, error) {
	if fileName == "" {
		return nil, fmt.Errorf("fileName is required")
	}
	if mediaType == "" {
		mediaType = MediaTypeFromExtension(fileName, mediaTypes)
		if mediaType == "" {
			return nil, &UnsupportedFileTypeError{FileName: fileName, Extension: Extension(fileName)}
		}
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty file data for %s", fileName)
	}

	return &Prepared{FileName: fileName, MediaType: mediaType, Data: data}, nil
}

// Extension returns filename's lower-case extension.
func Extension(filename string) string {
	return strings.ToLower(filepath.Ext(filename))
}

// LoadData returns in-memory data or lazily reads the attachment path.
func LoadData(fileName, filePath string, data []byte) ([]byte, error) {
	if data != nil {
		return data, nil
	}
	if filePath == "" {
		return nil, fmt.Errorf("no data or file path for attachment %s", fileName)
	}
	loaded, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read attachment %s: %w", filePath, err)
	}
	return loaded, nil
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
