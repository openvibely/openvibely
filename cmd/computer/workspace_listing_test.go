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
		{"directories", "", false, "nested/\ntop.go\ntop.txt\n"},
		{"filtered", "*.go", false, "nested/\ntop.go\n"},
		{"recursive filter", "*.go", true, "nested/\nnested/child.go\ntop.go\n"},
		{"no matching files", "*.rs", true, "nested/\n"},
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

func TestNativeSearchFindsNestedFilesWithoutRecursiveOption(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "Files", "nested", "deeper")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"match.go", "match.txt"} {
		if err := os.WriteFile(filepath.Join(nested, name), []byte("first line\nneedle\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, tc := range []struct{ include, want string }{
		{"", "nested/deeper/match.go:2: needle\nnested/deeper/match.txt:2: needle\n"},
		{"*.go", "nested/deeper/match.go:2: needle\n"},
	} {
		t.Run(tc.include, func(t *testing.T) {
			// The advertised grep_search schema has no recursive option.
			p := nativeFileRequest{Operation: "grep_search"}
			p.Input.Pattern = "needle"
			p.Input.Include = tc.include
			got, err := nativeFileOperation(context.Background(), root, p)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
}
