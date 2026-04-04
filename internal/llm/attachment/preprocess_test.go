package attachment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestPreprocess_NormalizesAndDetectsMime(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(fp, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	in := []models.Attachment{{FilePath: fp, FileName: " a.txt ", MediaType: ""}}
	out, err := Preprocess(in)
	if err != nil {
		t.Fatalf("Preprocess error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(out))
	}
	if strings.TrimSpace(out[0].FileName) != "a.txt" {
		t.Fatalf("expected trimmed filename, got %q", out[0].FileName)
	}
	if out[0].MediaType == "" {
		t.Fatalf("expected media type detection")
	}
	if !filepath.IsAbs(out[0].FilePath) {
		t.Fatalf("expected absolute file path, got %q", out[0].FilePath)
	}
}

func TestPreprocess_RejectsLargeImage(t *testing.T) {
	in := []models.Attachment{{FileName: "x.png", MediaType: "image/png", FileSize: DefaultMaxImageBytes + 1}}
	_, err := Preprocess(in)
	if err == nil {
		t.Fatal("expected size error")
	}
	if !strings.Contains(err.Error(), "image attachment too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}
