package attachment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaTypesAndExtensionLookup(t *testing.T) {
	openAI := MediaTypes(map[string]string{".bmp": "image/bmp"})
	anthropic := MediaTypes(map[string]string{".pdf": "application/pdf"})

	for _, filename := range []string{"photo.png", "PHOTO.PNG", "source.go"} {
		openAIMediaType := MediaTypeFromExtension(filename, openAI)
		if openAIMediaType == "" || openAIMediaType != MediaTypeFromExtension(filename, anthropic) {
			t.Errorf("common mapping differs for %q: OpenAI %q, Anthropic %q", filename, openAIMediaType, MediaTypeFromExtension(filename, anthropic))
		}
	}
	if MediaTypeFromExtension("image.bmp", openAI) != "image/bmp" {
		t.Error("OpenAI-specific BMP mapping missing")
	}
	if MediaTypeFromExtension("document.pdf", anthropic) != "application/pdf" {
		t.Error("Anthropic-specific PDF mapping missing")
	}
	if IsSupportedFileType("document.pdf", openAI) || IsSupportedFileType("image.bmp", anthropic) {
		t.Error("provider-specific extensions leaked into the other mapping")
	}
}

func TestNewFromPath(t *testing.T) {
	mediaTypes := MediaTypes(nil)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "Readme.MD")
	if err := os.WriteFile(filePath, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}

	prepared, err := NewFromPath(filePath, mediaTypes)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.FileName != "Readme.MD" || prepared.MediaType != "text/markdown" || prepared.FilePath != filePath || prepared.Data != nil {
		t.Errorf("unexpected prepared path attachment: %+v", prepared)
	}

	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: filepath.Join(dir, "missing.txt"), want: "file not found:"},
		{name: "directory", path: dir, want: "cannot attach directory:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewFromPath(test.path, mediaTypes)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewFromPath error = %v, want context %q", err, test.want)
			}
		})
	}

	unsupportedPath := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(unsupportedPath, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = NewFromPath(unsupportedPath, mediaTypes)
	var unsupported *UnsupportedFileTypeError
	if !errors.As(err, &unsupported) || unsupported.FileName != "archive.zip" || unsupported.Extension != ".zip" {
		t.Fatalf("NewFromPath unsupported error = %#v, want UnsupportedFileTypeError for .zip", err)
	}
}

func TestNewFromBytes(t *testing.T) {
	mediaTypes := MediaTypes(nil)
	prepared, err := NewFromBytes("source.go", "", []byte("package source"), mediaTypes)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.FileName != "source.go" || prepared.MediaType != "text/x-go" || string(prepared.Data) != "package source" || prepared.FilePath != "" {
		t.Errorf("unexpected prepared byte attachment: %+v", prepared)
	}

	for _, test := range []struct {
		name     string
		fileName string
		media    string
		data     []byte
		want     string
	}{
		{name: "empty name", media: "text/plain", data: []byte("x"), want: "fileName is required"},
		{name: "unsupported extension", fileName: "archive.zip", data: []byte("x"), want: "unsupported file type: archive.zip (extension .zip)"},
		{name: "empty data", fileName: "note.txt", media: "text/plain", want: "empty file data for note.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewFromBytes(test.fileName, test.media, test.data, mediaTypes)
			if err == nil || err.Error() != test.want {
				t.Fatalf("NewFromBytes error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadData(t *testing.T) {
	inMemory := []byte("memory")
	got, err := LoadData("note.txt", "", inMemory)
	if err != nil || string(got) != string(inMemory) {
		t.Fatalf("LoadData in-memory = %q, %v", got, err)
	}

	filePath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(filePath, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadData("note.txt", filePath, nil)
	if err != nil || string(got) != "disk" {
		t.Fatalf("LoadData file = %q, %v", got, err)
	}

	_, err = LoadData("note.txt", "", nil)
	if err == nil || err.Error() != "no data or file path for attachment note.txt" {
		t.Fatalf("LoadData without source error = %v", err)
	}

	missingPath := filepath.Join(t.TempDir(), "missing.txt")
	_, err = LoadData("missing.txt", missingPath, nil)
	if err == nil || !strings.Contains(err.Error(), "read attachment "+missingPath+":") {
		t.Fatalf("LoadData read error = %v", err)
	}
}

func TestUnsupportedFileTypeError(t *testing.T) {
	if got := (&UnsupportedFileTypeError{FileName: "unknown", Extension: ""}).Error(); got != "unsupported file type: unknown (no extension)" {
		t.Errorf("no-extension error = %q", got)
	}
}
