// Package pageplugins installs, authorizes, and runs project-scoped plugin
// pages. It adapts plugins to the surface-neutral pageintegration contract.
package pageplugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"strings"

	"github.com/openvibely/openvibely/internal/pageintegration"
)

// SchemaVersion is the only plugin.json schema_version this host accepts.
const SchemaVersion = 1

const (
	ManifestFile     = "plugin.json"
	MaxManifestBytes = 256 << 10
	MaxPages         = 16
	MaxOperations    = 32
)

// Known permissions. Unknown permissions are rejected so capability declarations stay meaningful.
const (
	PermissionStorage = "storage"      // persist instance state
	PermissionEvents  = "events"       // publish/receive refresh events
	PermissionAgent   = "agent"        // legacy declaration; side chat is always available
	PermissionBackend = "backend.exec" // run a local backend process when the page is enabled
)

var knownPermissions = map[string]struct{}{
	PermissionStorage: {},
	PermissionEvents:  {},
	PermissionAgent:   {},
	PermissionBackend: {},
}

type Manifest struct {
	SchemaVersion int                         `json:"schema_version"`
	ID            string                      `json:"id"`
	Name          string                      `json:"name"`
	Version       string                      `json:"version"`
	Description   string                      `json:"description,omitempty"`
	Pages         []Page                      `json:"pages"`
	Backend       *Backend                    `json:"backend,omitempty"`
	Permissions   []string                    `json:"permissions,omitempty"`
	Operations    []pageintegration.Operation `json:"operations,omitempty"`
}

type Page struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	Entry string `json:"entry"`
}

// Backend declares an optional local process. Executables are keyed by
// "<GOOS>-<GOARCH>" (e.g. "darwin-arm64"); "any" is a fallback for scripts or
// portable binaries. Paths are relative to the package root.
type Backend struct {
	Executables map[string]string `json:"executables"`
	Args        []string          `json:"args,omitempty"`
	// Builtin names a host-implemented backend (used by bundled examples); it
	// never executes a process.
	Builtin string `json:"builtin,omitempty"`
}

var (
	pluginIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)
	pageIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	semverPattern    = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)
	platformPattern  = regexp.MustCompile(`^(any|[a-z0-9]+-[a-z0-9]+)$`)
	eventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
)

// ParseManifest decodes and validates plugin.json strictly.
func ParseManifest(raw []byte) (*Manifest, error) {
	if len(raw) > MaxManifestBytes {
		return nil, fmt.Errorf("plugin.json exceeds %d bytes", MaxManifestBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid plugin.json: %w", err)
	}
	if dec.More() {
		return nil, errors.New("invalid plugin.json: trailing data")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (want %d)", m.SchemaVersion, SchemaVersion)
	}
	if !pluginIDPattern.MatchString(m.ID) {
		return fmt.Errorf("id %q must match %s", m.ID, pluginIDPattern)
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 80 {
		return errors.New("name is required (max 80 chars)")
	}
	if !semverPattern.MatchString(m.Version) {
		return fmt.Errorf("version %q must be semantic (MAJOR.MINOR.PATCH)", m.Version)
	}
	if len(m.Pages) == 0 || len(m.Pages) > MaxPages {
		return fmt.Errorf("pages must contain 1-%d entries", MaxPages)
	}
	seenPages := map[string]bool{}
	for i := range m.Pages {
		p := &m.Pages[i]
		if !pageIDPattern.MatchString(p.ID) {
			return fmt.Errorf("page id %q must match %s", p.ID, pageIDPattern)
		}
		if seenPages[p.ID] {
			return fmt.Errorf("duplicate page id %q", p.ID)
		}
		seenPages[p.ID] = true
		if strings.TrimSpace(p.Title) == "" || len(p.Title) > 60 {
			return fmt.Errorf("page %q title is required (max 60 chars)", p.ID)
		}
		if len(p.Icon) > 8 {
			return fmt.Errorf("page %q icon must be a short emoji or glyph", p.ID)
		}
		clean, err := CleanRelPath(p.Entry)
		if err != nil {
			return fmt.Errorf("page %q entry: %w", p.ID, err)
		}
		p.Entry = clean
		if ext := strings.ToLower(path.Ext(p.Entry)); ext != ".html" && ext != ".htm" {
			return fmt.Errorf("page %q entry must be an .html file", p.ID)
		}
	}
	seenPerm := map[string]bool{}
	for _, perm := range m.Permissions {
		if _, ok := knownPermissions[perm]; !ok {
			return fmt.Errorf("unknown permission %q", perm)
		}
		if seenPerm[perm] {
			return fmt.Errorf("duplicate permission %q", perm)
		}
		seenPerm[perm] = true
	}
	if m.Backend != nil {
		if m.Backend.Builtin == "" && len(m.Backend.Executables) == 0 {
			return errors.New("backend requires executables or builtin")
		}
		if m.Backend.Builtin == "" && !seenPerm[PermissionBackend] {
			return fmt.Errorf("backend executables require the %q permission", PermissionBackend)
		}
		for platform, exe := range m.Backend.Executables {
			if !platformPattern.MatchString(platform) {
				return fmt.Errorf("backend platform key %q must be GOOS-GOARCH or any", platform)
			}
			if _, err := CleanRelPath(exe); err != nil {
				return fmt.Errorf("backend executable for %s: %w", platform, err)
			}
		}
		if len(m.Backend.Args) > 32 {
			return errors.New("backend args are limited to 32 entries")
		}
	} else if seenPerm[PermissionBackend] {
		return fmt.Errorf("permission %q declared without a backend", PermissionBackend)
	}
	if len(m.Operations) > MaxOperations {
		return fmt.Errorf("operations are limited to %d", MaxOperations)
	}
	seenOps := map[string]bool{}
	for _, op := range m.Operations {
		if err := pageintegration.ValidateOperation(op); err != nil {
			return err
		}
		if seenOps[op.Name] {
			return fmt.Errorf("duplicate operation %q", op.Name)
		}
		seenOps[op.Name] = true
		for _, ev := range op.Emits {
			if !eventTypePattern.MatchString(ev) {
				return fmt.Errorf("operation %q emits invalid event type %q", op.Name, ev)
			}
		}
		if len(op.Emits) > 0 && !seenPerm[PermissionEvents] {
			return fmt.Errorf("operation %q emits events without the %q permission", op.Name, PermissionEvents)
		}
		for _, perm := range op.Permissions {
			if !seenPerm[perm] {
				return fmt.Errorf("operation %q uses undeclared permission %q", op.Name, perm)
			}
		}
	}
	if len(m.Operations) > 0 && m.Backend == nil {
		return errors.New("operations require a backend (process or builtin)")
	}
	return nil
}

func (m *Manifest) Page(id string) (Page, bool) {
	for _, p := range m.Pages {
		if p.ID == id {
			return p, true
		}
	}
	return Page{}, false
}

func (m *Manifest) HasPermission(p string) bool {
	for _, perm := range m.Permissions {
		if perm == p {
			return true
		}
	}
	return false
}

// RunsProcess reports whether enabling this plugin would execute local code.
func (m *Manifest) RunsProcess() bool { return m.Backend != nil && m.Backend.Builtin == "" }

// SelectExecutable chooses the executable for goos/goarch, preferring an exact
// "<goos>-<goarch>" match over "any".
func (b *Backend) SelectExecutable(goos, goarch string) (string, error) {
	if exe, ok := b.Executables[goos+"-"+goarch]; ok {
		return exe, nil
	}
	if exe, ok := b.Executables["any"]; ok {
		return exe, nil
	}
	return "", fmt.Errorf("no backend executable for %s-%s", goos, goarch)
}

func (b *Backend) SelectHostExecutable() (string, error) {
	return b.SelectExecutable(runtime.GOOS, runtime.GOARCH)
}

// CleanRelPath normalizes a manifest/request path and rejects anything that
// could leave the package directory before the filesystem is consulted.
func CleanRelPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsAny(p, "\\\x00") {
		return "", errors.New("path contains forbidden characters")
	}
	if strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':') {
		return "", errors.New("path must be relative")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errors.New("path must not contain ..")
		}
	}
	clean := path.Clean(p)
	if clean == "." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path escapes package root")
	}
	return clean, nil
}
