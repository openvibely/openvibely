//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeFileListingDirectoriesAndPatterns(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Files", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"top.go", "top.txt", "nested/child.go", "nested/child.txt"} {
		if err := os.WriteFile(filepath.Join(dir, "Files", name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, tc := range []struct {
		name, glob string
		recursive  bool
		want       string
	}{
		{"directories", "", false, "Files/nested/\nFiles/top.go\nFiles/top.txt\n"},
		{"filtered", "*.go", false, "Files/nested/\nFiles/top.go\n"},
		{"recursive filter", "*.go", true, "Files/nested/\nFiles/nested/child.go\nFiles/top.go\n"},
		{"no matching files", "*.rs", true, "Files/nested/\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := nativeFileRequest{Operation: "list_files"}
			p.Input.Pattern, p.Input.Recursive = tc.glob, tc.recursive
			got, err := nativeFileOperation(context.Background(), root, p)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
	p := nativeFileRequest{Operation: "list_files"}
	p.Input.Pattern = "["
	if _, err := nativeFileOperation(context.Background(), root, p); err == nil {
		t.Fatal("invalid glob accepted")
	}
}
