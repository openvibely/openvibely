package anthropicclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMediaTypeFromExtension(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"PNG image", "test.png", "image/png"},
		{"JPEG image", "test.jpg", "image/jpeg"},
		{"JPEG image alt", "test.jpeg", "image/jpeg"},
		{"GIF image", "test.gif", "image/gif"},
		{"WebP image", "test.webp", "image/webp"},
		{"PDF document", "test.pdf", "application/pdf"},
		{"Text file", "test.txt", "text/plain"},
		{"Go file", "test.go", "text/x-go"},
		{"Python file", "test.py", "text/x-python"},
		{"JavaScript file", "test.js", "text/javascript"},
		{"Markdown file", "test.md", "text/markdown"},
		{"Unknown", "test.xyz", ""},
		{"No extension", "test", ""},
		{"Case insensitive", "TEST.PNG", "image/png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MediaTypeFromExtension(tt.filename)
			if got != tt.want {
				t.Errorf("MediaTypeFromExtension(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestIsImageMediaType(t *testing.T) {
	tests := []struct {
		mediaType string
		want      bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"application/pdf", false},
		{"text/plain", false},
		{"application/octet-stream", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mediaType, func(t *testing.T) {
			got := IsImageMediaType(tt.mediaType)
			if got != tt.want {
				t.Errorf("IsImageMediaType(%q) = %v, want %v", tt.mediaType, got, tt.want)
			}
		})
	}
}

func TestIsDocumentMediaType(t *testing.T) {
	tests := []struct {
		mediaType string
		want      bool
	}{
		{"application/pdf", true},
		{"image/png", false},
		{"text/plain", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mediaType, func(t *testing.T) {
			got := IsDocumentMediaType(tt.mediaType)
			if got != tt.want {
				t.Errorf("IsDocumentMediaType(%q) = %v, want %v", tt.mediaType, got, tt.want)
			}
		})
	}
}

func TestNewFileAttachment(t *testing.T) {
	// Create temp file with test content
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := []byte("Hello, World!")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	att, err := NewFileAttachment(testFile)
	if err != nil {
		t.Fatalf("NewFileAttachment() error = %v", err)
	}

	if att.FileName != "test.txt" {
		t.Errorf("FileName = %q, want %q", att.FileName, "test.txt")
	}
	if att.MediaType != "text/plain" {
		t.Errorf("MediaType = %q, want %q", att.MediaType, "text/plain")
	}
	if att.FilePath != testFile {
		t.Errorf("FilePath = %q, want %q", att.FilePath, testFile)
	}
	// NewFileAttachment does not eagerly load data
	if att.Data != nil {
		t.Error("Data should be nil (lazy loading), got non-nil")
	}
}

func TestNewFileAttachment_NonExistent(t *testing.T) {
	_, err := NewFileAttachment("/nonexistent/file.txt")
	if err == nil {
		t.Error("NewFileAttachment() expected error for non-existent file, got nil")
	}
}

func TestNewFileAttachment_UnsupportedType(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.xyz")
	if err := os.WriteFile(testFile, []byte("data"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	_, err := NewFileAttachment(testFile)
	if err == nil {
		t.Error("NewFileAttachment() expected error for unsupported file type, got nil")
	}
	if _, ok := err.(*UnsupportedFileTypeError); !ok {
		t.Errorf("expected UnsupportedFileTypeError, got %T: %v", err, err)
	}
}

func TestNewFileAttachmentFromBytes(t *testing.T) {
	data := []byte("Hello, World!")
	att, err := NewFileAttachmentFromBytes("test.txt", "text/plain", data)
	if err != nil {
		t.Fatalf("NewFileAttachmentFromBytes() error = %v", err)
	}

	if att.FileName != "test.txt" {
		t.Errorf("FileName = %q, want %q", att.FileName, "test.txt")
	}
	if att.MediaType != "text/plain" {
		t.Errorf("MediaType = %q, want %q", att.MediaType, "text/plain")
	}
	if string(att.Data) != "Hello, World!" {
		t.Errorf("Data = %q, want %q", string(att.Data), "Hello, World!")
	}
}

func TestNewFileAttachmentFromBytes_EmptyData(t *testing.T) {
	_, err := NewFileAttachmentFromBytes("test.txt", "text/plain", nil)
	if err == nil {
		t.Error("NewFileAttachmentFromBytes() expected error for empty data, got nil")
	}
}

func TestNewFileAttachmentFromBytes_EmptyName(t *testing.T) {
	_, err := NewFileAttachmentFromBytes("", "text/plain", []byte("data"))
	if err == nil {
		t.Error("NewFileAttachmentFromBytes() expected error for empty name, got nil")
	}
}

func TestToContentBlock_TextFile(t *testing.T) {
	att := &FileAttachment{
		FileName:  "test.txt",
		MediaType: "text/plain",
		Data:      []byte("Hello"),
	}

	block, err := att.toContentBlock()
	if err != nil {
		t.Fatalf("toContentBlock() error = %v", err)
	}

	if block["type"] != "text" {
		t.Errorf("type = %v, want 'text'", block["type"])
	}
	text, ok := block["text"].(string)
	if !ok {
		t.Fatal("text field not a string")
	}
	if text == "" {
		t.Error("text field is empty")
	}
}

func TestToContentBlock_TextFileTooLarge(t *testing.T) {
	largeData := make([]byte, 101*1024) // > 100KB
	att := &FileAttachment{
		FileName:  "large.txt",
		MediaType: "text/plain",
		Data:      largeData,
	}

	_, err := att.toContentBlock()
	if err == nil {
		t.Error("toContentBlock() expected error for text file exceeding 100KB, got nil")
	}
}

func TestToContentBlock_Image(t *testing.T) {
	att := &FileAttachment{
		FileName:  "test.png",
		MediaType: "image/png",
		Data:      []byte("fake png data"),
	}

	block, err := att.toContentBlock()
	if err != nil {
		t.Fatalf("toContentBlock() error = %v", err)
	}

	if block["type"] != "image" {
		t.Errorf("type = %v, want 'image'", block["type"])
	}
	source, ok := block["source"].(map[string]interface{})
	if !ok {
		t.Fatal("source field not a map")
	}
	if source["type"] != "base64" {
		t.Errorf("source.type = %v, want 'base64'", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("source.media_type = %v, want 'image/png'", source["media_type"])
	}
}

func TestToContentBlock_Document(t *testing.T) {
	att := &FileAttachment{
		FileName:  "test.pdf",
		MediaType: "application/pdf",
		Data:      []byte("fake pdf data"),
	}

	block, err := att.toContentBlock()
	if err != nil {
		t.Fatalf("toContentBlock() error = %v", err)
	}

	if block["type"] != "document" {
		t.Errorf("type = %v, want 'document'", block["type"])
	}
}

func TestBuildContentBlocks(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		attachments []*FileAttachment
		wantLen     int
		wantErr     bool
	}{
		{
			name:        "Text only",
			message:     "Hello",
			attachments: nil,
			wantLen:     1, // Just text block
		},
		{
			name:    "Text with image",
			message: "Look at this",
			attachments: []*FileAttachment{
				{FileName: "test.png", MediaType: "image/png", Data: []byte("fake")},
			},
			wantLen: 2, // Text + image
		},
		{
			name:    "Text with document",
			message: "Read this",
			attachments: []*FileAttachment{
				{FileName: "test.pdf", MediaType: "application/pdf", Data: []byte("fake")},
			},
			wantLen: 2, // Text + document
		},
		{
			name:    "Text with multiple attachments",
			message: "Check these",
			attachments: []*FileAttachment{
				{FileName: "1.png", MediaType: "image/png", Data: []byte("fake1")},
				{FileName: "2.jpg", MediaType: "image/jpeg", Data: []byte("fake2")},
			},
			wantLen: 3, // Text + 2 images
		},
		{
			name:        "Empty message no attachments",
			message:     "",
			attachments: nil,
			wantLen:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := buildContentBlocks(tt.message, tt.attachments)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildContentBlocks() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(blocks) != tt.wantLen {
				t.Errorf("buildContentBlocks() returned %d blocks, want %d", len(blocks), tt.wantLen)
				return
			}

			// First block should be text when message is non-empty
			if tt.message != "" && len(blocks) > 0 {
				if blocks[0]["type"] != "text" {
					t.Errorf("First block type = %v, want 'text'", blocks[0]["type"])
				}
			}
		})
	}
}

func TestIsSupportedFileType(t *testing.T) {
	tests := []struct {
		filename string
		want     bool
	}{
		{"test.png", true},
		{"test.go", true},
		{"test.pdf", true},
		{"test.xyz", false},
		{"test", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := IsSupportedFileType(tt.filename)
			if got != tt.want {
				t.Errorf("IsSupportedFileType(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestIsTextMediaType(t *testing.T) {
	tests := []struct {
		mediaType string
		want      bool
	}{
		{"text/plain", true},
		{"text/x-go", true},
		{"text/markdown", true},
		{"application/json", true},
		{"image/png", false},
		{"application/pdf", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mediaType, func(t *testing.T) {
			got := IsTextMediaType(tt.mediaType)
			if got != tt.want {
				t.Errorf("IsTextMediaType(%q) = %v, want %v", tt.mediaType, got, tt.want)
			}
		})
	}
}
