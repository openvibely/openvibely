//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const transferLimit = 25 << 20

// computerHomeRoot is fixed in production; tests substitute a temporary root.
var computerHomeRoot = "/home/ubuntu"

func computerHome() string {
	if name := os.Getenv("OPENVIBELY_DESKTOP_ID"); name != "" && desktopName.MatchString(name) {
		return filepath.Join(computerHomeRoot, ".desktops", name, "home")
	}
	return computerHomeRoot
}

func homePath(path string) (string, error) {
	if computerHome() != "/home/ubuntu" && (path == "/home/ubuntu" || strings.HasPrefix(path, "/home/ubuntu/")) && !strings.HasPrefix(path, computerHome()+"/") {
		path = strings.TrimPrefix(strings.TrimPrefix(path, "/home/ubuntu"), "/")
		if path == "" {
			path = "."
		}
	}
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(computerHome(), path)
		if err != nil {
			return "", err
		}
		path = rel
	}
	path = filepath.Clean(path)
	if path != "." && !filepath.IsLocal(path) {
		return "", fmt.Errorf("choose a path inside /home/ubuntu")
	}
	if path == ".browser" || strings.HasPrefix(path, ".browser/") || path == ".desktops" || strings.HasPrefix(path, ".desktops/") || strings.HasPrefix(path, ".shared-browser-auth") {
		return "", fmt.Errorf("browser profiles are private; use the browser")
	}
	return path, nil
}
func workspaceRoutes(mux *http.ServeMux, recordingBusy ...func() bool) {
	mux.HandleFunc("POST /native-files", func(w http.ResponseWriter, r *http.Request) {
		var p nativeFileRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&p); err != nil {
			http.Error(w, "Invalid file operation", 400)
			return
		}
		root, err := os.OpenRoot(computerHome())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer root.Close()
		result, err := nativeFileOperation(r.Context(), root, p)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, result)
	})
	mux.HandleFunc("GET /files", func(w http.ResponseWriter, r *http.Request) {
		path, err := homePath(r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		root, err := os.OpenRoot(computerHome())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer root.Close()
		f, err := openWorkspaceFile(root, path, os.O_RDONLY, true)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if info.IsDir() {
			entries, err := f.ReadDir(1000)
			if err != nil && err != io.EOF {
				http.Error(w, err.Error(), 400)
				return
			}
			items := []map[string]any{}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				info, err := entry.Info()
				if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
					continue
				}
				items = append(items, map[string]any{"name": entry.Name(), "path": filepath.Join(path, entry.Name()), "directory": entry.IsDir(), "size": info.Size()})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"directory": true, "path": path, "entries": items})
			return
		}
		if !info.Mode().IsRegular() || info.Size() > transferLimit {
			http.Error(w, "Choose a regular file up to 25 MB", 400)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		io.Copy(w, io.LimitReader(f, transferLimit))
	})
	mux.HandleFunc("PUT /files", func(w http.ResponseWriter, r *http.Request) {
		path, err := homePath(r.URL.Query().Get("path"))
		if err != nil || path == "." {
			http.Error(w, "Choose a file inside /home/ubuntu", 400)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, transferLimit))
		if err != nil {
			http.Error(w, "File exceeds 25 MB", 413)
			return
		}
		root, err := os.OpenRoot(computerHome())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer root.Close()
		if err = taskMkdirAll(root, filepath.Dir(path)); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// Never silently overwrite existing deliverables.
		f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		defer f.Close()
		if err = taskOwnFile(f); err != nil {
			root.Remove(path)
			http.Error(w, "File ownership failed", 500)
			return
		}
		if _, err = f.Write(data); err != nil {
			root.Remove(path)
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"path": filepath.Join(computerHome(), path), "bytes": len(data)})
	})
	shellRoutes(mux, recordingBusy...)
}

type nativeFileRequest struct {
	Operation string `json:"operation"`
	Input     struct {
		FilePath  string `json:"file_path"`
		Path      string `json:"path"`
		Content   string `json:"content"`
		Old       string `json:"old_string"`
		New       string `json:"new_string"`
		All       bool   `json:"replace_all"`
		Offset    int    `json:"offset"`
		Limit     int    `json:"limit"`
		Recursive bool   `json:"recursive"`
		Pattern   string `json:"pattern"`
		Include   string `json:"include"`
	} `json:"input"`
}

// The tool behavior is shared with local execution; only filesystem access is
// adapted here. Every discovered path still passes through homePath and os.Root.
func nativeFileOperation(ctx context.Context, root *os.Root, p nativeFileRequest) (string, error) {
	input, err := json.Marshal(p.Input)
	if err != nil {
		return "", err
	}
	return agenttools.ExecuteFiles(ctx, filepath.Join(computerHome(), "Files"), p.Operation, input, workspaceFS{root})
}

type workspaceFS struct{ root *os.Root }

func (w workspaceFS) Open(path string) (*os.File, error) {
	p, err := homePath(path)
	if err != nil {
		return nil, err
	}
	return openWorkspaceFile(w.root, p, os.O_RDONLY, false)
}
func (w workspaceFS) ReadFile(path string) ([]byte, error) {
	f, err := w.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, transferLimit+1))
	if len(data) > transferLimit {
		return nil, fmt.Errorf("choose a regular file up to 25 MB")
	}
	return data, err
}
func (w workspaceFS) MkdirAll(path string, _ fs.FileMode) error {
	p, err := homePath(path)
	if err != nil {
		return err
	}
	return taskMkdirAll(w.root, p)
}
func (w workspaceFS) WriteFile(path string, data []byte, _ fs.FileMode) error {
	p, err := homePath(path)
	if err != nil {
		return err
	}
	return taskWriteFile(w.root, p, data)
}
func (w workspaceFS) ReadDir(path string) ([]os.DirEntry, error) {
	p, err := homePath(path)
	if err != nil {
		return nil, err
	}
	f, err := openWorkspaceFile(w.root, p, os.O_RDONLY, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	kept := entries[:0]
	for _, e := range entries {
		if _, err := homePath(filepath.Join(p, e.Name())); err == nil && (e.IsDir() || e.Type().IsRegular()) {
			kept = append(kept, e)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name() < kept[j].Name() })
	return kept, nil
}
func (w workspaceFS) WalkDir(path string, visit fs.WalkDirFunc) error {
	p, err := homePath(path)
	if err != nil {
		return err
	}
	return fs.WalkDir(w.root.FS(), filepath.ToSlash(p), func(name string, d fs.DirEntry, walkErr error) error {
		full := filepath.Join(computerHome(), name)
		if walkErr != nil {
			return visit(full, d, walkErr)
		}
		if _, err := homePath(name); err != nil || (!d.IsDir() && !d.Type().IsRegular()) {
			if name == p {
				return fmt.Errorf("choose a regular file or directory")
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return visit(full, d, nil)
	})
}

// Check before opening to avoid touching special files, then check the opened
// descriptor too: the path can change between stat and open. NONBLOCK prevents
// a substituted FIFO from waiting for a peer. Never truncate before validation.
func openWorkspaceFile(root *os.Root, path string, flags int, allowDirectory bool) (*os.File, error) {
	acceptable := func(info os.FileInfo) bool { return info.Mode().IsRegular() || (allowDirectory && info.IsDir()) }
	info, err := root.Stat(path)
	if err == nil && !acceptable(info) {
		return nil, fmt.Errorf("choose a regular file")
	}
	if err != nil && !(errors.Is(err, os.ErrNotExist) && flags&os.O_CREATE != 0) {
		return nil, err
	}
	f, err := root.OpenFile(path, flags|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || !acceptable(info) {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("choose a regular file")
	}
	return f, nil
}
