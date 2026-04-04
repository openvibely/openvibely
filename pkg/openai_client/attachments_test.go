package openaiclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaTypeFromExtension(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"image.png", "image/png"},
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"anim.gif", "image/gif"},
		{"image.webp", "image/webp"},
		{"bitmap.bmp", "image/bmp"},
		{"code.go", "text/x-go"},
		{"code.py", "text/x-python"},
		{"code.js", "text/javascript"},
		{"code.ts", "text/typescript"},
		{"code.rs", "text/x-rust"},
		{"code.rb", "text/x-ruby"},
		{"code.java", "text/x-java"},
		{"code.c", "text/x-c"},
		{"code.cpp", "text/x-c++"},
		{"readme.md", "text/markdown"},
		{"data.json", "application/json"},
		{"config.yaml", "text/yaml"},
		{"config.yml", "text/yaml"},
		{"data.csv", "text/csv"},
		{"notes.txt", "text/plain"},
		{"script.sh", "text/x-sh"},
		{"query.sql", "text/x-sql"},
		{"unknown.xyz", ""},
		{"binary.exe", ""},
	}

	for _, tt := range tests {
		got := MediaTypeFromExtension(tt.filename)
		if got != tt.want {
			t.Errorf("MediaTypeFromExtension(%q) = %q, want %q", tt.filename, got, tt.want)
		}
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
		{"text/plain", false},
		{"application/json", false},
		{"", false},
	}

	for _, tt := range tests {
		got := IsImageMediaType(tt.mediaType)
		if got != tt.want {
			t.Errorf("IsImageMediaType(%q) = %v, want %v", tt.mediaType, got, tt.want)
		}
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
		got := IsTextMediaType(tt.mediaType)
		if got != tt.want {
			t.Errorf("IsTextMediaType(%q) = %v, want %v", tt.mediaType, got, tt.want)
		}
	}
}

func TestIsSupportedFileType(t *testing.T) {
	tests := []struct {
		filename string
		want     bool
	}{
		{"image.png", true},
		{"code.go", true},
		{"readme.md", true},
		{"data.json", true},
		{"binary.exe", false},
		{"doc.pdf", false},
		{"archive.zip", false},
	}

	for _, tt := range tests {
		got := IsSupportedFileType(tt.filename)
		if got != tt.want {
			t.Errorf("IsSupportedFileType(%q) = %v, want %v", tt.filename, got, tt.want)
		}
	}
}

func TestNewFileAttachment(t *testing.T) {
	dir := t.TempDir()

	t.Run("image file", func(t *testing.T) {
		path := filepath.Join(dir, "test.png")
		os.WriteFile(path, []byte("PNG data"), 0644)
		att, err := NewFileAttachment(path)
		if err != nil {
			t.Fatal(err)
		}
		if att.MediaType != "image/png" {
			t.Errorf("MediaType = %q, want image/png", att.MediaType)
		}
		if att.FileName != "test.png" {
			t.Errorf("FileName = %q", att.FileName)
		}
	})

	t.Run("text file", func(t *testing.T) {
		path := filepath.Join(dir, "main.go")
		os.WriteFile(path, []byte("package main\n"), 0644)
		att, err := NewFileAttachment(path)
		if err != nil {
			t.Fatal(err)
		}
		if att.MediaType != "text/x-go" {
			t.Errorf("MediaType = %q, want text/x-go", att.MediaType)
		}
	})

	t.Run("unsupported type", func(t *testing.T) {
		path := filepath.Join(dir, "binary.exe")
		os.WriteFile(path, []byte("MZ"), 0644)
		_, err := NewFileAttachment(path)
		if err == nil {
			t.Fatal("expected error for unsupported type")
		}
		var ute *UnsupportedFileTypeError
		if !isUnsupportedFileTypeError(err) {
			t.Errorf("expected UnsupportedFileTypeError, got %T: %v", err, err)
		}
		_ = ute
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := NewFileAttachment(filepath.Join(dir, "nonexistent.txt"))
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("directory", func(t *testing.T) {
		_, err := NewFileAttachment(dir)
		if err == nil {
			t.Fatal("expected error for directory")
		}
	})
}

func isUnsupportedFileTypeError(err error) bool {
	_, ok := err.(*UnsupportedFileTypeError)
	return ok
}

func TestNewFileAttachmentFromBytes(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		att, err := NewFileAttachmentFromBytes("test.png", "image/png", []byte("PNG data"))
		if err != nil {
			t.Fatal(err)
		}
		if att.FileName != "test.png" || att.MediaType != "image/png" {
			t.Errorf("att = %+v", att)
		}
	})

	t.Run("auto-detect media type", func(t *testing.T) {
		att, err := NewFileAttachmentFromBytes("main.go", "", []byte("package main"))
		if err != nil {
			t.Fatal(err)
		}
		if att.MediaType != "text/x-go" {
			t.Errorf("MediaType = %q", att.MediaType)
		}
	})

	t.Run("empty data", func(t *testing.T) {
		_, err := NewFileAttachmentFromBytes("test.txt", "text/plain", nil)
		if err == nil {
			t.Error("expected error for empty data")
		}
	})

	t.Run("empty filename", func(t *testing.T) {
		_, err := NewFileAttachmentFromBytes("", "text/plain", []byte("data"))
		if err == nil {
			t.Error("expected error for empty filename")
		}
	})
}

func TestToInputContent_Image(t *testing.T) {
	att := &FileAttachment{
		FileName:  "test.png",
		MediaType: "image/png",
		Data:      []byte("fake-png-data"),
	}

	content, err := att.toInputContent()
	if err != nil {
		t.Fatal(err)
	}
	if content["type"] != "input_image" {
		t.Errorf("type = %v, want input_image", content["type"])
	}
	imageURL, ok := content["image_url"].(string)
	if !ok || !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Errorf("image_url = %v", content["image_url"])
	}
}

func TestToInputContent_TextFile(t *testing.T) {
	att := &FileAttachment{
		FileName:  "main.go",
		MediaType: "text/x-go",
		Data:      []byte("package main\nfunc main() {}\n"),
	}

	content, err := att.toInputContent()
	if err != nil {
		t.Fatal(err)
	}
	if content["type"] != "input_text" {
		t.Errorf("type = %v, want input_text", content["type"])
	}
	text, ok := content["text"].(string)
	if !ok {
		t.Fatal("text not a string")
	}
	if !strings.Contains(text, "main.go") || !strings.Contains(text, "package main") {
		t.Errorf("text = %q", text)
	}
}

func TestToInputContent_TextFileFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.go")
	os.WriteFile(path, []byte("package hello\n"), 0644)

	att := &FileAttachment{
		FileName:  "hello.go",
		MediaType: "text/x-go",
		FilePath:  path,
	}

	content, err := att.toInputContent()
	if err != nil {
		t.Fatal(err)
	}
	if content["type"] != "input_text" {
		t.Errorf("type = %v", content["type"])
	}
	text := content["text"].(string)
	if !strings.Contains(text, "package hello") {
		t.Errorf("text = %q", text)
	}
}

func TestToInputContent_UnsupportedMediaType(t *testing.T) {
	att := &FileAttachment{
		FileName:  "doc.pdf",
		MediaType: "application/pdf",
		Data:      []byte("PDF data"),
	}
	_, err := att.toInputContent()
	if err == nil {
		t.Error("expected error for unsupported media type")
	}
}

func TestSupportedExtensions(t *testing.T) {
	exts := SupportedExtensions()
	if len(exts) == 0 {
		t.Fatal("expected at least one supported extension")
	}

	// Check a few key ones
	found := make(map[string]bool)
	for _, ext := range exts {
		found[ext] = true
	}
	for _, required := range []string{".png", ".go", ".py", ".json", ".md"} {
		if !found[required] {
			t.Errorf("missing required extension: %s", required)
		}
	}
}

func TestUnsupportedFileTypeError_Message(t *testing.T) {
	err := &UnsupportedFileTypeError{FileName: "test.exe", Extension: ".exe"}
	msg := err.Error()
	if !strings.Contains(msg, "test.exe") || !strings.Contains(msg, ".exe") {
		t.Errorf("error message = %q", msg)
	}

	err2 := &UnsupportedFileTypeError{FileName: "noext", Extension: ""}
	msg2 := err2.Error()
	if !strings.Contains(msg2, "no extension") {
		t.Errorf("error message = %q", msg2)
	}
}
