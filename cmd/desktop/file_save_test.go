package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type brokenDownload struct{}

func (brokenDownload) Read([]byte) (int, error) { return 0, errors.New("transfer failed") }

func TestSaveOriginalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.docx")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		source io.Reader
		fails  bool
	}{
		{"canceled", canceled, strings.NewReader("new"), true},
		{"failed", context.Background(), io.MultiReader(strings.NewReader("partial"), brokenDownload{}), true},
		{"overwrite", context.Background(), strings.NewReader("PK\x00original bytes"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := saveOriginalFile(tc.ctx, path, tc.source)
			if (err != nil) != tc.fails {
				t.Fatalf("save error: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := "previous"
			if !tc.fails {
				want = "PK\x00original bytes"
			}
			if string(data) != want {
				t.Fatalf("saved %q, want %q", data, want)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("temporary download leaked")
			}
		})
	}
}
