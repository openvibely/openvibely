//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

const transferLimit = 25 << 20

func computerHome() string {
	if name := os.Getenv("OPENVIBELY_DESKTOP_ID"); name != "" && desktopName.MatchString(name) {
		return filepath.Join("/home/ubuntu/.desktops", name, "home")
	}
	return "/home/ubuntu"
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
func workspaceRoutes(mux *http.ServeMux) {
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
	shellRoutes(mux)
}

type limitedOutput struct {
	mu        sync.Mutex
	text      string
	truncated bool
}

func (b *limitedOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text, b.truncated
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	space := 65536 - len(b.text)
	if len(p) > space {
		b.truncated = true
		p = p[:space]
	}
	b.text += string(p)
	return n, nil
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

func nativeFileOperation(ctx context.Context, root *os.Root, p nativeFileRequest) (string, error) {
	raw := p.Input.Path
	if p.Operation == "read_file" || p.Operation == "write_file" || p.Operation == "edit_file" {
		raw = p.Input.FilePath
		if raw == "" {
			return "", fmt.Errorf("provide file_path")
		}
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(computerHome(), "Files", raw)
	}
	path, err := homePath(raw)
	if err != nil {
		return "", err
	}
	switch p.Operation {
	case "write_file":
		if err = taskMkdirAll(root, filepath.Dir(path)); err != nil {
			return "", err
		}
		err = taskWriteFile(root, path, []byte(p.Input.Content))
		return "Saved " + raw, err
	case "read_file", "edit_file":
		f, err := openWorkspaceFile(root, path, os.O_RDONLY, false)
		if err != nil {
			return "", err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
			return "", fmt.Errorf("choose a regular text file up to 2 MB")
		}
		data, err := io.ReadAll(io.LimitReader(f, 2<<20))
		if err != nil {
			return "", err
		}
		text := string(data)
		if p.Operation == "edit_file" {
			count := strings.Count(text, p.Input.Old)
			if p.Input.Old == "" || count == 0 || (!p.Input.All && count != 1) {
				return "", fmt.Errorf("old_string must match once unless replace_all is true")
			}
			n := 1
			if p.Input.All {
				n = -1
			}
			err = taskWriteFile(root, path, []byte(strings.Replace(text, p.Input.Old, p.Input.New, n)))
			return "Updated " + raw, err
		}
		lines := strings.Split(text, "\n")
		offset := max(0, p.Input.Offset)
		if offset >= len(lines) {
			return "End of file", nil
		}
		limit := p.Input.Limit
		if limit <= 0 || limit > 1000 {
			limit = 1000
		}
		return strings.Join(lines[offset:min(len(lines), offset+limit)], "\n"), nil
	case "list_files", "grep_search":
		var pattern *regexp.Regexp
		if p.Operation == "grep_search" {
			if p.Input.Pattern == "" {
				return "", fmt.Errorf("provide pattern")
			}
			pattern, err = regexp.Compile(p.Input.Pattern)
			if err != nil {
				return "", err
			}
		}
		var out strings.Builder
		count, visited := 0, 0
		err = fs.WalkDir(root.FS(), filepath.ToSlash(path), func(name string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			visited++
			if visited > 10000 || count >= 500 || out.Len() > 60000 {
				return fs.SkipAll
			}
			if walkErr != nil {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 || strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if name != path && !p.Input.Recursive {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			if p.Input.Include != "" {
				match, _ := filepath.Match(p.Input.Include, d.Name())
				if !match {
					return nil
				}
			}
			if pattern == nil {
				fmt.Fprintln(&out, name)
				count++
				return nil
			}
			f, err := openWorkspaceFile(root, name, os.O_RDONLY, false)
			if err != nil {
				return nil
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
				return nil
			}
			data, err := io.ReadAll(io.LimitReader(f, 2<<20))
			if err != nil {
				return nil
			}
			for i, line := range strings.Split(string(data), "\n") {
				if pattern.MatchString(line) {
					fmt.Fprintf(&out, "%s:%d:%s\n", name, i+1, line)
					count++
				}
				if count >= 500 || out.Len() > 60000 {
					break
				}
			}
			return nil
		})
		return out.String(), err
	default:
		return "", fmt.Errorf("unknown file operation")
	}
}

func computerShellTimeout(seconds int, background bool) int {
	if seconds <= 0 {
		return 60
	}
	limit := 180
	if background {
		limit = 3600
	}
	return min(seconds, limit)
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
