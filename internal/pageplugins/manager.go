package pageplugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Kind is the pageintegration.PageRef kind for plugin pages.
const Kind = "plugin"

// HandleTTL bounds how long one page load's bridge handle stays valid.
const HandleTTL = 12 * time.Hour

var (
	ErrNotFound         = errors.New("plugin page not found")
	ErrDisabled         = errors.New("plugin page is disabled")
	ErrApprovalRequired = errors.New("administrator approval required before this plugin's backend can run")
	ErrApprovalStale    = errors.New("this page changed since you loaded Settings; review the current version and approve again")
	ErrPackageChanged   = errors.New("plugin page was updated; reload it to use the current version")
	ErrContentChanged   = errors.New("plugin package content no longer matches the approved hash")
	ErrVersionConflict  = errors.New("this plugin version is already installed with different content; bump the version")
	ErrPackageInUse     = errors.New("package still has project instances")
)

// BuiltinRequest is passed to host-implemented example backends.
type BuiltinRequest struct {
	Instance  models.PagePluginInstance
	Operation string
	Input     json.RawMessage
	Caller    pageintegration.Caller
}

// BuiltinResult carries the operation output and optional replacement instance state.
type BuiltinResult struct {
	Output json.RawMessage
	State  json.RawMessage
}

// Builtin is a backend implemented in-process (used for bundled examples only).
type Builtin interface {
	Invoke(ctx context.Context, req BuiltinRequest) (BuiltinResult, error)
}

// Handle binds one page load (iframe) to an instance, its owning project, and a principal.
type Handle struct {
	Token       string
	InstanceID  string
	ProjectID   string
	PrincipalID string
	ExpiresAt   time.Time
	done        chan struct{}
	timer       *time.Timer
}

// Done closes when this page load expires or is retired. It does not cancel chat.
func (h Handle) Done() <-chan struct{} { return h.done }

// Resolved is an authorized, enabled plugin page instance.
type Resolved struct {
	Instance models.PagePluginInstance
	Package  models.PagePluginPackage
	Manifest *Manifest
	Page     Page
}

// Ref returns the surface-neutral page reference.
func (r *Resolved) Ref() pageintegration.PageRef {
	return pageintegration.PageRef{Kind: Kind, PageID: r.Page.ID, InstanceID: r.Instance.ID, ProjectID: r.Instance.ProjectID, Title: r.Instance.Title}
}

type procEntry struct {
	proc      *Process
	projectID string
	hash      string
}

// Manager owns plugin packages, instances, processes, and bridge handles.
type Manager struct {
	store    persistence.PagePluginStore
	root     string
	Hub      *pageintegration.Hub
	Contexts *pageintegration.ContextStore
	builtins map[string]Builtin

	publishMu sync.Mutex
	// packageMu serializes package files and admission of new package references.
	// Never hold it while acquiring instance lifecycle locks or invoking callbacks.
	packageMu sync.Mutex
	mu        sync.Mutex
	procs     map[string]*procEntry
	handles   map[string]Handle
	startups  map[string]StartupStatus
	manifests map[string]*Manifest
	instMu    map[string]*sync.Mutex
	lifetimes map[string]*instanceLifetime
	// OnDisable cancels host-owned work after admission is closed.
	OnDisable func(context.Context, string) error
	now       func() time.Time
}

// NewManager creates a manager storing packages under root (outside any task worktree).
func NewManager(store persistence.PagePluginStore, root string) *Manager {
	m := &Manager{
		store:     store,
		root:      root,
		Hub:       pageintegration.NewHub(),
		Contexts:  pageintegration.NewContextStore(),
		builtins:  map[string]Builtin{},
		procs:     map[string]*procEntry{},
		handles:   map[string]Handle{},
		startups:  map[string]StartupStatus{},
		manifests: map[string]*Manifest{},
		instMu:    map[string]*sync.Mutex{},
		lifetimes: map[string]*instanceLifetime{},
		now:       time.Now,
	}
	m.builtins[SalesBuiltinName] = salesBuiltin{}
	return m
}

func (m *Manager) Root() string { return m.root }

// RegisterBuiltin adds a host-implemented backend (tests and bundled examples).
func (m *Manager) RegisterBuiltin(name string, b Builtin) { m.builtins[name] = b }

// InstallFromDir validates and copies a prebuilt plugin bundle into the managed store.
func (m *Manager) InstallFromDir(ctx context.Context, dir, installedBy string) (*models.PagePluginPackage, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("plugin directory %q not found", dir)
	}
	return m.install(ctx, os.DirFS(abs), installedBy)
}

// InstallExample installs the bundled Sales example package.
func (m *Manager) InstallExample(ctx context.Context, installedBy string) (*models.PagePluginPackage, error) {
	return m.install(ctx, SalesExampleFS(), installedBy)
}

func (m *Manager) install(ctx context.Context, src fs.FS, installedBy string) (*models.PagePluginPackage, error) {
	m.packageMu.Lock()
	defer m.packageMu.Unlock()
	man, hash, err := validatePackageFS(src)
	if err != nil {
		return nil, err
	}
	if man.Backend != nil && man.Backend.Builtin != "" {
		if _, ok := m.builtins[man.Backend.Builtin]; !ok {
			return nil, fmt.Errorf("unknown builtin backend %q", man.Backend.Builtin)
		}
	}
	if existing, err := m.store.GetPackageByVersion(ctx, man.ID, man.Version); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.ContentHash == hash {
			return existing, nil
		}
		return nil, ErrVersionConflict
	}
	pluginDir := filepath.Join(m.root, man.ID)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return nil, err
	}
	final := filepath.Join(pluginDir, man.Version)
	tmp, err := os.MkdirTemp(pluginDir, ".install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		return nil, err
	}
	copied, err := HashTree(os.DirFS(tmp))
	if err != nil {
		return nil, err
	}
	if copied != hash {
		return nil, errors.New("package changed while installing")
	}
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(man)
	pkg := &models.PagePluginPackage{PluginID: man.ID, Version: man.Version, Name: man.Name, ContentHash: hash, ManifestJSON: string(raw), InstallPath: final, InstalledBy: installedBy}
	if err := m.store.CreatePackage(ctx, pkg); err != nil {
		_ = os.RemoveAll(final)
		return nil, err
	}
	return pkg, nil
}

// RemovePackage deletes an unused package and its files.
func (m *Manager) RemovePackage(ctx context.Context, packageID string) error {
	m.packageMu.Lock()
	defer m.packageMu.Unlock()
	pkg, err := m.store.GetPackage(ctx, packageID)
	if err != nil {
		return err
	}
	if pkg == nil {
		return ErrNotFound
	}
	n, err := m.store.CountInstancesForPackage(ctx, packageID)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrPackageInUse
	}
	// Files go first so a failed deletion keeps the record available for retry.
	if rel, err := filepath.Rel(m.root, pkg.InstallPath); err == nil && rel != "." && !filepath.IsAbs(rel) && rel[:min(2, len(rel))] != ".." {
		if err := os.RemoveAll(pkg.InstallPath); err != nil {
			return fmt.Errorf("remove plugin package files: %w", err)
		}
	}
	if err := m.store.DeletePackage(ctx, packageID); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.manifests, packageID)
	m.mu.Unlock()
	return nil
}

// Manifest returns the parsed manifest recorded for a package.
func (m *Manager) Manifest(pkg *models.PagePluginPackage) (*Manifest, error) {
	m.mu.Lock()
	cached := m.manifests[pkg.ID]
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	man, err := ParseManifest([]byte(pkg.ManifestJSON))
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.manifests[pkg.ID] = man
	m.mu.Unlock()
	return man, nil
}

// CreateInstance adds a disabled page instance of a package to one project.
func (m *Manager) CreateInstance(ctx context.Context, projectID, packageID, pageID string) (*models.PagePluginInstance, error) {
	m.packageMu.Lock()
	defer m.packageMu.Unlock()
	pkg, err := m.store.GetPackage(ctx, packageID)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		return nil, ErrNotFound
	}
	man, err := m.Manifest(pkg)
	if err != nil {
		return nil, err
	}
	if pageID == "" && len(man.Pages) > 0 {
		pageID = man.Pages[0].ID
	}
	page, ok := man.Page(pageID)
	if !ok {
		return nil, fmt.Errorf("package has no page %q", pageID)
	}
	in := &models.PagePluginInstance{ProjectID: projectID, PackageID: pkg.ID, PageID: page.ID, Title: page.Title, ConfigJSON: "{}", StateJSON: "{}"}
	if err := m.store.CreateInstance(ctx, in); err != nil {
		return nil, err
	}
	return m.store.GetInstance(ctx, in.ID)
}

// NeedsApproval reports whether the instance must be approved for its current package content.
func (m *Manager) NeedsApproval(in *models.PagePluginInstance, man *Manifest) bool {
	return man.RunsProcess() && in.ApprovedHash != in.ContentHash
}

// Approve records administrator approval for the exact content hash the
// administrator reviewed; it fails if the page now uses different content.
func (m *Manager) Approve(ctx context.Context, instanceID, reviewedHash, approvedBy string) error {
	if reviewedHash == "" {
		return ErrApprovalStale
	}
	in, err := m.store.GetInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	if in == nil {
		return ErrNotFound
	}
	ok, err := m.store.ApproveInstanceIfContent(ctx, instanceID, reviewedHash, approvedBy)
	if err != nil {
		return err
	}
	if !ok {
		return ErrApprovalStale
	}
	return nil
}

// Enable turns an instance on, starting its backend process when it has one.
func (m *Manager) Enable(ctx context.Context, instanceID string) error {
	lifecycle := m.instanceLock("lifecycle:" + instanceID)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	lock := m.instanceLock(instanceID)
	lock.Lock()
	defer lock.Unlock()
	return m.enableReplacementLocked(ctx, instanceID)
}

// The caller owns both instance lifecycle and operation locks.
func (m *Manager) enableLocked(ctx context.Context, instanceID string) error {
	in, pkg, man, err := m.load(ctx, instanceID)
	if err != nil {
		return err
	}
	if m.NeedsApproval(in, man) {
		return ErrApprovalRequired
	}
	if err := m.store.SetInstanceEnabled(ctx, instanceID, true); err != nil {
		return err
	}
	in.Enabled = true
	m.mu.Lock()
	life := m.lifetimes[instanceID]
	if life == nil || life.ctx.Err() != nil {
		lifetimeCtx, cancel := context.WithCancel(context.Background())
		m.lifetimes[instanceID] = &instanceLifetime{ctx: lifetimeCtx, cancel: cancel}
	}
	life = m.lifetimes[instanceID]
	m.mu.Unlock()
	startupCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life.ctx, cancel)
	defer func() { stop(); cancel() }()
	if life.ctx.Err() != nil {
		cancel()
	}
	ctx = startupCtx
	m.mu.Lock()
	if entry := m.procs[instanceID]; entry != nil && entry.proc.Status() == "failed" {
		delete(m.procs, instanceID)
	}
	m.mu.Unlock()
	if man.RunsProcess() {
		if _, err := m.ensureProcess(ctx, in, pkg, man); err != nil {
			applog.Infof("[pageplugins] backend for instance %s failed to start: %v", instanceID, err)
			_ = m.store.SetInstanceEnabled(ctx, instanceID, false)
			return err
		}
	}
	return nil
}

// Disable turns an instance off and releases every runtime resource bound to it.
func (m *Manager) Disable(ctx context.Context, instanceID string) error {
	return m.deactivate(ctx, instanceID, false)
}

// RemoveInstance first shuts down the instance, then deletes its saved data.
func (m *Manager) RemoveInstance(ctx context.Context, instanceID string) error {
	return m.deactivate(ctx, instanceID, true)
}

// ReleaseProject stops runtime resources for instances of a deleted project.
func (m *Manager) ReleaseProject(projectID string) {
	m.mu.Lock()
	var ids []string
	for id, e := range m.procs {
		if e.projectID == projectID {
			ids = append(ids, id)
		}
	}
	for tok, h := range m.handles {
		if h.ProjectID == projectID {
			ids = append(ids, h.InstanceID)
			m.retireHandleLocked(tok)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.releaseInstance(id)
	}
}

func (m *Manager) releaseInstance(instanceID string) {
	m.mu.Lock()
	if life := m.lifetimes[instanceID]; life != nil {
		life.cancel()
	}
	entry := m.procs[instanceID]
	delete(m.procs, instanceID)
	for tok, h := range m.handles {
		if h.InstanceID == instanceID {
			m.retireHandleLocked(tok)
		}
	}
	m.mu.Unlock()
	if entry != nil {
		entry.proc.Abort()
		entry.proc.Stop()
	}
	m.Hub.CloseInstance(instanceID)
	m.Contexts.ForgetInstance(instanceID)
}

// StopAll stops every backend process (server shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	for _, life := range m.lifetimes {
		life.cancel()
	}
	entries := m.procs
	m.procs = map[string]*procEntry{}
	for token := range m.handles {
		m.retireHandleLocked(token)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(p *Process) { defer wg.Done(); p.Stop() }(e.proc)
	}
	wg.Wait()
}

// ProcessStatus reports the backend status for an instance ("none" when no process is tracked).
func (m *Manager) ProcessStatus(instanceID string) (string, []string) {
	m.mu.Lock()
	e := m.procs[instanceID]
	m.mu.Unlock()
	if e == nil {
		return "none", nil
	}
	return e.proc.Status(), e.proc.Logs()
}

func (m *Manager) load(ctx context.Context, instanceID string) (*models.PagePluginInstance, *models.PagePluginPackage, *Manifest, error) {
	in, err := m.store.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, nil, nil, err
	}
	if in == nil {
		return nil, nil, nil, ErrNotFound
	}
	pkg, err := m.store.GetPackage(ctx, in.PackageID)
	if err != nil {
		return nil, nil, nil, err
	}
	if pkg == nil {
		return nil, nil, nil, ErrNotFound
	}
	man, err := m.Manifest(pkg)
	if err != nil {
		return nil, nil, nil, err
	}
	return in, pkg, man, nil
}

// Resolve authorizes access to an enabled instance. requestProjectID is the
// project the caller is acting in; models.AllProjectsID permits any project
// (aggregate view) while every other value must equal the owning project.
func (m *Manager) Resolve(ctx context.Context, instanceID, requestProjectID string) (*Resolved, error) {
	in, pkg, man, err := m.load(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if requestProjectID != models.AllProjectsID && requestProjectID != in.ProjectID {
		return nil, ErrNotFound
	}
	if !in.Enabled {
		return nil, ErrDisabled
	}
	page, ok := man.Page(in.PageID)
	if !ok {
		return nil, ErrNotFound
	}
	return &Resolved{Instance: *in, Package: *pkg, Manifest: man, Page: page}, nil
}

// ListEnabled returns enabled instances for a project, or for all projects.
func (m *Manager) ListEnabled(ctx context.Context, projectID string) ([]models.PagePluginInstance, error) {
	if projectID == models.AllProjectsID {
		return m.store.ListAllInstances(ctx, true)
	}
	return m.store.ListInstances(ctx, projectID, true)
}

// Store exposes the underlying persistence for settings and conversation bindings.
func (m *Manager) Store() persistence.PagePluginStore { return m.store }

// IssueHandle creates a bridge handle for one page load.
func (m *Manager) IssueHandle(r *Resolved, principalID string) (Handle, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return Handle{}, err
	}
	h := Handle{Token: hex.EncodeToString(buf), InstanceID: r.Instance.ID, ProjectID: r.Instance.ProjectID, PrincipalID: principalID, ExpiresAt: m.now().Add(HandleTTL), done: make(chan struct{})}
	m.mu.Lock()
	now := m.now()
	for tok, old := range m.handles {
		if now.After(old.ExpiresAt) {
			m.retireHandleLocked(tok)
		}
	}
	h.timer = time.AfterFunc(HandleTTL, func() { m.RetireHandle(h.Token) })
	m.handles[h.Token] = h
	m.startups[h.InstanceID] = StartupStatus{State: "loading", ContentHash: r.Package.ContentHash, ObservedAt: m.now().UTC(), handle: h.Token}
	m.mu.Unlock()
	return h, nil
}

// LookupHandle returns a live handle.
func (m *Manager) LookupHandle(token string) (Handle, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.handles[token]
	if !ok || m.now().After(h.ExpiresAt) {
		m.retireHandleLocked(token)
		return Handle{}, false
	}
	return h, true
}

// RetireHandle invalidates a handle (page unload or project switch).
func (m *Manager) RetireHandle(token string) {
	m.mu.Lock()
	m.retireHandleLocked(token)
	m.mu.Unlock()
}

// retireHandleLocked also revokes already-open subscriptions, not just future requests.
func (m *Manager) retireHandleLocked(token string) {
	if h, ok := m.handles[token]; ok {
		delete(m.handles, token)
		if h.timer != nil {
			h.timer.Stop()
		}
		if h.done != nil {
			close(h.done)
		}
	}
}

// AssetPath resolves a file inside the instance's package directory.
func (m *Manager) AssetPath(r *Resolved, rel string) (string, error) {
	clean, err := CleanRelPath(rel)
	if err != nil {
		return "", err
	}
	return SafeJoin(r.Package.InstallPath, clean)
}

func (m *Manager) instanceLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.instMu[id]
	if l == nil {
		l = &sync.Mutex{}
		m.instMu[id] = l
	}
	return l
}

func (m *Manager) ensureProcess(ctx context.Context, in *models.PagePluginInstance, pkg *models.PagePluginPackage, man *Manifest) (*Process, error) {
	if m.NeedsApproval(in, man) || in.ApprovedHash != pkg.ContentHash {
		return nil, ErrApprovalRequired
	}
	m.mu.Lock()
	if e := m.procs[in.ID]; e != nil && e.hash == in.ApprovedHash && e.proc.Status() != "stopped" {
		m.mu.Unlock()
		return e.proc, nil
	}
	m.mu.Unlock()

	exe, err := man.Backend.SelectHostExecutable()
	if err != nil {
		return nil, err
	}
	exePath, err := SafeJoin(pkg.InstallPath, exe)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	prev := m.procs[in.ID]
	m.mu.Unlock()
	if prev != nil {
		prev.proc.CloseNotifications()
	}
	instanceID, projectID := in.ID, in.ProjectID
	packageID, approvedHash, installPath := pkg.ID, pkg.ContentHash, pkg.InstallPath
	proc := NewProcess(ProcessSpec{
		// Cached processes also pass through this hook on every actual launch.
		// Load state/config from the same fresh record used for authorization;
		// never retain the first startup snapshot for later crash recovery.
		PrepareStart: func(ctx context.Context) (any, error) {
			fresh, err := m.store.GetInstance(ctx, instanceID)
			if err != nil {
				return nil, err
			}
			if fresh == nil || !fresh.Enabled || fresh.ProjectID != projectID {
				return nil, ErrDisabled
			}
			if fresh.PackageID != packageID || fresh.ApprovedHash != approvedHash || fresh.ContentHash != approvedHash {
				return nil, ErrApprovalRequired
			}
			onDisk, err := HashTree(os.DirFS(installPath))
			if err != nil {
				return nil, err
			}
			if onDisk != approvedHash {
				return nil, ErrContentChanged
			}
			return map[string]any{
				"protocol": ProtocolVersion,
				"instance": map[string]string{"id": fresh.ID, "project_id": fresh.ProjectID, "page_id": fresh.PageID},
				"config":   json.RawMessage(nonEmptyJSON(fresh.ConfigJSON)),
				"state":    json.RawMessage(nonEmptyJSON(fresh.StateJSON)),
			}, nil
		},
		Path:     exePath,
		Args:     man.Backend.Args,
		Dir:      pkg.InstallPath,
		OnNotify: func(method string, params json.RawMessage) { m.handleNotify(instanceID, man, method, params) },
	})
	stopAbort := context.AfterFunc(ctx, proc.Abort)
	defer stopAbort()
	if err := proc.Start(ctx); err != nil {
		proc.Abort()
		return nil, err
	}
	m.mu.Lock()
	if old := m.procs[in.ID]; old != nil {
		go old.proc.Stop()
	}
	m.procs[in.ID] = &procEntry{proc: proc, projectID: in.ProjectID, hash: in.ApprovedHash}
	m.mu.Unlock()
	return proc, nil
}

func (m *Manager) handleNotify(instanceID string, man *Manifest, method string, params json.RawMessage) {
	switch method {
	case "state/set":
		if !man.HasPermission(PermissionStorage) || len(params) > MaxStateBytes {
			return
		}
		var p struct {
			State json.RawMessage `json:"state"`
		}
		if json.Unmarshal(params, &p) != nil || len(p.State) == 0 {
			return
		}
		if err := m.store.UpdateInstanceState(context.Background(), instanceID, string(p.State)); err != nil {
			applog.Infof("[pageplugins] state update for %s failed: %v", instanceID, err)
		}
	case "events/publish":
		if !man.HasPermission(PermissionEvents) {
			return
		}
		var p struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(params, &p) != nil || !eventTypePattern.MatchString(p.Type) {
			return
		}
		m.Hub.Publish(instanceID, pageintegration.Event{Type: p.Type, Data: p.Data, At: m.now()})
	}
}

// MaxStateBytes bounds persisted instance state.
const MaxStateBytes = 1 << 20

func nonEmptyJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// Integration adapts a resolved plugin instance to the shared page-integration contract.
func (m *Manager) Integration(r *Resolved) pageintegration.Integration {
	return &pluginIntegration{m: m, r: r}
}

// Resolver returns a pageintegration.Resolver that re-authorizes the instance on every tool call.
func (m *Manager) Resolver(instanceID, projectID string) pageintegration.Resolver {
	return func(ctx context.Context) (pageintegration.Integration, error) {
		r, err := m.Resolve(ctx, instanceID, projectID)
		if err != nil {
			return nil, err
		}
		return m.Integration(r), nil
	}
}

type pluginIntegration struct {
	m *Manager
	r *Resolved
}

func (p *pluginIntegration) Ref() pageintegration.PageRef { return p.r.Ref() }

func (p *pluginIntegration) Operations() []pageintegration.Operation { return p.r.Manifest.Operations }

func (p *pluginIntegration) Invoke(ctx context.Context, caller pageintegration.Caller, op string, input json.RawMessage) (json.RawMessage, error) {
	ctx, done, err := p.m.BeginInstance(ctx, p.r.Instance.ID)
	if err != nil {
		return nil, err
	}
	defer done()
	fresh, err := p.m.store.GetInstance(ctx, p.r.Instance.ID)
	if err != nil {
		return nil, err
	}
	if fresh == nil || !fresh.Enabled || fresh.ProjectID != p.r.Instance.ProjectID {
		return nil, ErrDisabled
	}
	if fresh.PackageID != p.r.Package.ID || fresh.ContentHash != p.r.Package.ContentHash {
		return nil, ErrPackageChanged
	}
	opDef, ok := pageintegration.FindOperation(p, op)
	if !ok {
		return nil, pageintegration.ErrOperationNotFound
	}
	man := p.r.Manifest
	if man.Backend == nil {
		return nil, errors.New("plugin has no backend")
	}
	var out json.RawMessage
	if man.Backend.Builtin != "" {
		b := p.m.builtins[man.Backend.Builtin]
		if b == nil {
			return nil, fmt.Errorf("builtin backend %q unavailable", man.Backend.Builtin)
		}
		res, err := b.Invoke(ctx, BuiltinRequest{Instance: *fresh, Operation: op, Input: input, Caller: caller})
		if err != nil {
			return nil, err
		}
		if len(res.State) > 0 && man.HasPermission(PermissionStorage) {
			if len(res.State) > MaxStateBytes {
				return nil, errors.New("plugin state too large")
			}
			if err := p.m.store.UpdateInstanceState(ctx, fresh.ID, string(res.State)); err != nil {
				return nil, err
			}
		}
		out = res.Output
	} else {
		proc, err := p.m.ensureProcess(ctx, fresh, &p.r.Package, man)
		if err != nil {
			return nil, err
		}
		stopAbort := context.AfterFunc(ctx, proc.Abort)
		defer stopAbort()
		out, err = proc.Call(ctx, "operation/invoke", map[string]any{
			"operation": op,
			"input":     input,
			"caller":    map[string]string{"via": caller.Via},
		})
		if err != nil {
			return nil, err
		}
	}
	if man.HasPermission(PermissionEvents) {
		for _, ev := range opDef.Emits {
			p.m.Hub.Publish(p.r.Instance.ID, pageintegration.Event{Type: ev, Operation: op, At: p.m.now()})
		}
	}
	return out, nil
}
