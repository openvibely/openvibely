package skilldeps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/httpretry"
)

type Result struct {
	Name                string `json:"name"`
	EnvironmentVariable string `json:"environment_variable,omitempty"`
	Version             string `json:"version"`
	Status              string `json:"status"`
	Executable          string `json:"executable,omitempty"`
	OverrideVariable    string `json:"override_variable"`
	Error               string `json:"error,omitempty"`
}

type Progress struct {
	Name  string
	Stage string
}
type Manager struct {
	CacheDir  string
	Platform  string
	Client    *http.Client
	LookupEnv func(string) string
	Progress  func(Progress)
}

var installs sync.Map

func DefaultManager() (*Manager, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &Manager{CacheDir: filepath.Join(root, "openvibely", "skill-tools"), Platform: runtime.GOOS + "/" + runtime.GOARCH, LookupEnv: os.Getenv}, nil
}

// Resolve reports each failure independently: a missing Office renderer must not
// hide a working PDF tool. Paths describe this process's execution environment.
func (m *Manager) Resolve(ctx context.Context, deps []Dependency) ([]Result, error) {
	if err := Validate(deps); err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(deps))
	for _, d := range deps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := Result{Name: d.Name, EnvironmentVariable: d.EnvironmentVariable, Version: d.Version, OverrideVariable: OverrideVariable(d.Name)}
		executable, source, err := m.resolve(ctx, d)
		r.Status = source
		if err != nil {
			r.Error = err.Error()
			m.notify(d.Name, "failed")
		} else {
			r.Executable = executable
			m.notify(d.Name, "ready")
		}
		results = append(results, r)
	}
	return results, nil
}

func (m *Manager) notify(name, stage string) {
	if m.Progress != nil {
		m.Progress(Progress{Name: name, Stage: stage})
	}
}

func (m *Manager) resolve(ctx context.Context, d Dependency) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if m.LookupEnv != nil {
		if override := m.LookupEnv(OverrideVariable(d.Name)); override != "" {
			if !filepath.IsAbs(override) {
				return "", "failed", fmt.Errorf("%s must be an absolute executable path", OverrideVariable(d.Name))
			}
			m.notify(d.Name, "checking local executable")
			if err := health(ctx, override, d.HealthCheck); err != nil {
				return "", "failed", err
			}
			return override, "local", nil
		}
	}
	platform := m.Platform
	if platform == "" {
		platform = runtime.GOOS + "/" + runtime.GOARCH
	}
	p, ok := d.Packages[platform]
	if !ok {
		return "", "unsupported", fmt.Errorf("no verified %s package for %s %s; set %s to a local executable", platform, d.Name, d.Version, OverrideVariable(d.Name))
	}
	if m.CacheDir == "" {
		return "", "failed", errors.New("dependency cache directory is not configured")
	}
	manifest, _ := json.Marshal(struct {
		Package Package
		Check   []string
	}{p, d.HealthCheck})
	digest := sha256.Sum256(manifest)
	root, err := filepath.Abs(filepath.Join(m.CacheDir, d.Name, d.Version, platform, hex.EncodeToString(digest[:])))
	if err != nil {
		return "", "failed", err
	}
	entry, _ := installs.LoadOrStore(root, make(chan struct{}, 1))
	gate := entry.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return "", "failed", ctx.Err()
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return "", "failed", err
	}
	lock, err := os.OpenFile(root+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", "failed", err
	}
	defer lock.Close()
	for {
		ok, err := tryLock(lock)
		if err != nil {
			return "", "failed", err
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return "", "failed", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	executable := filepath.Join(root, filepath.FromSlash(p.Executable))
	if err := cachedHealth(ctx, root, executable, d.HealthCheck); err == nil {
		return executable, "cached", nil
	}
	if err := ctx.Err(); err != nil {
		return "", "failed", err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return "", "failed", err
	}
	// A failed health check never marks a broken installation as usable.
	if err := os.RemoveAll(root); err != nil {
		return "", "failed", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), ".prepare-")
	if err != nil {
		return "", "failed", err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "package")
	m.notify(d.Name, "downloading")
	if err := m.download(ctx, p, archive, d.Name); err != nil {
		return "", "failed", err
	}
	m.notify(d.Name, "preparing")
	content := filepath.Join(stage, "content")
	if err := os.Mkdir(content, 0700); err != nil {
		return "", "failed", err
	}
	if err := prepare(ctx, p, archive, content); err != nil {
		return "", "failed", err
	}
	prepared := filepath.Join(content, filepath.FromSlash(p.Executable))
	if err := inside(content, prepared); err != nil {
		return "", "failed", err
	}
	if err := os.Chmod(prepared, 0755); err != nil {
		return "", "failed", err
	}
	m.notify(d.Name, "checking executable")
	if err := health(ctx, prepared, d.HealthCheck); err != nil {
		return "", "failed", err
	}
	hash, err := fileHash(prepared)
	if err != nil {
		return "", "failed", err
	}
	if err := os.WriteFile(filepath.Join(content, ".ready"), []byte(hash), 0600); err != nil {
		return "", "failed", err
	}
	if err := os.Rename(content, root); err != nil {
		// Another process may have installed the identical package while we prepared it.
		if check := cachedHealth(ctx, root, executable, d.HealthCheck); check != nil {
			return "", "failed", err
		}
	}
	if err := cachedHealth(ctx, root, executable, d.HealthCheck); err != nil {
		return "", "failed", err
	}
	return executable, "installed", nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func cachedHealth(ctx context.Context, root, executable string, args []string) error {
	if err := inside(root, executable); err != nil {
		return err
	}
	want, err := os.ReadFile(filepath.Join(root, ".ready"))
	if err != nil {
		return err
	}
	actual, err := fileHash(executable)
	if err != nil {
		return err
	}
	if string(want) != actual {
		return errors.New("cached executable checksum mismatch")
	}
	return health(ctx, executable, args)
}
func inside(root, name string) error {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(canonical, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("executable escapes package")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("executable is not a regular file")
	}
	return nil
}
func health(ctx context.Context, path string, args []string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("dependency executable unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("dependency executable is not a regular file")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	// Diagnostics stay bounded and never include environment variables or download URLs.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dependency health check failed: %w", err)
	}
	return nil
}
func (m *Manager) download(ctx context.Context, p Package, destination, name string) error {
	client := *httpclient.New(15 * time.Minute)
	if m.Client != nil {
		client = *m.Client
	}
	originalRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("dependency redirect must use HTTPS")
		}
		if len(via) >= 10 {
			return errors.New("too many dependency redirects")
		}
		if originalRedirect != nil {
			return originalRedirect(req, via)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}
	resp, err := httpretry.DoRequest(&client, req)
	if err != nil {
		return errors.New("dependency download failed; check network access")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dependency download returned HTTP %d", resp.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(&downloadReader{Reader: resp.Body, last: time.Now(), notify: func() { m.notify(name, "downloading") }}, (2<<30)+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > 2<<30 {
		return errors.New("dependency archive exceeds 2 GiB")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), p.SHA256) {
		return errors.New("dependency archive checksum mismatch")
	}
	return nil
}

// Report continuing activity without emitting an event for every network read.
type downloadReader struct {
	io.Reader
	last   time.Time
	notify func()
}

func (r *downloadReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 && time.Since(r.last) >= 5*time.Second {
		r.last = time.Now()
		r.notify()
	}
	return n, err
}
