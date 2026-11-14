package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileSystem supplies access, not tool behavior. Confined backends must enforce
// their policy on every operation, including paths discovered during traversal.
type FileSystem interface {
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, fs.FileMode) error
	MkdirAll(string, fs.FileMode) error
	ReadDir(string) ([]os.DirEntry, error)
	WalkDir(string, fs.WalkDirFunc) error
	Open(string) (*os.File, error)
}

type localFileSystem struct{}

func (localFileSystem) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
func (localFileSystem) WriteFile(p string, b []byte, m fs.FileMode) error {
	return os.WriteFile(p, b, m)
}
func (localFileSystem) MkdirAll(p string, m fs.FileMode) error   { return os.MkdirAll(p, m) }
func (localFileSystem) ReadDir(p string) ([]os.DirEntry, error)  { return os.ReadDir(p) }
func (localFileSystem) WalkDir(p string, f fs.WalkDirFunc) error { return filepath.WalkDir(p, f) }
func (localFileSystem) Open(p string) (*os.File, error)          { return os.Open(p) }

// ExecuteFiles is the common file-tool implementation for local and isolated execution.
func ExecuteFiles(ctx context.Context, workDir, name string, input json.RawMessage, filesystem FileSystem) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if filesystem == nil {
		return "", fmt.Errorf("filesystem is required")
	}
	switch name {
	case "read_file":
		return readFile(filesystem, workDir, input)
	case "write_file":
		return writeFile(filesystem, workDir, input)
	case "edit_file":
		return editFile(filesystem, workDir, input)
	case "list_files":
		return listFiles(filesystem, workDir, input)
	case "grep_search":
		return grepSearch(ctx, filesystem, workDir, input)
	default:
		return "", fmt.Errorf("unknown file tool: %s", name)
	}
}
