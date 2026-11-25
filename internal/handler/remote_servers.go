package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const uiPreferenceSavedServersKey = "ui.saved_servers"

var (
	errRemoteLogin       = errors.New("incorrect username or password")
	errRemoteRateLimited = errors.New("too many sign-in attempts; try again later")
	errRemoteHostedSSO   = errors.New("this server uses single sign-on")
)

// remoteSession is one live connection to a saved server. Its cookies are kept
// in remoteSessionsSettingKey so a restart reconnects without the password,
// which is never stored.
type remoteSession struct {
	jar      *remoteJar
	base     *url.URL
	username string
	user     string
	at       time.Time
	expired  bool
	key      remoteSessionKey
	stop     context.CancelFunc
}

// remoteJar records the remote's cookie lifetime and reports every cookie
// change, including renewals, so the stored copy stays current. It keeps each
// cookie's attributes, which http.CookieJar does not return.
type remoteJar struct {
	http.CookieJar
	lifetime atomic.Int64
	changed  func()
	mu       sync.Mutex
	saved    map[string]storedRemoteCookie
}

func newRemoteJar() *remoteJar {
	jar, _ := cookiejar.New(nil)
	return &remoteJar{CookieJar: jar, saved: map[string]storedRemoteCookie{}}
}

func (j *remoteJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if len(cookies) == 0 {
		return
	}
	j.CookieJar.SetCookies(u, cookies)
	now := time.Now()
	j.mu.Lock()
	for _, c := range cookies {
		if c.MaxAge > 0 {
			j.lifetime.Store(int64(c.MaxAge) * int64(time.Second))
		}
		stored := storedRemoteCookie{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure, HTTPOnly: c.HttpOnly}
		if stored.Path == "" || stored.Path[0] != '/' {
			stored.Path = "/"
		}
		switch {
		case c.MaxAge < 0:
			delete(j.saved, cookieKey(c.Name, stored.Path))
			continue
		case c.MaxAge > 0:
			stored.Expires = now.Add(time.Duration(c.MaxAge) * time.Second)
		case !c.Expires.IsZero():
			if !c.Expires.After(now) {
				delete(j.saved, cookieKey(c.Name, stored.Path))
				continue
			}
			stored.Expires = c.Expires
		}
		j.saved[cookieKey(c.Name, stored.Path)] = stored
	}
	j.mu.Unlock()
	if j.changed != nil {
		j.changed()
	}
}

func cookieKey(name, path string) string { return name + ";" + path }

// storedCookies returns the jar's unexpired cookies with their attributes.
func (j *remoteJar) storedCookies() []storedRemoteCookie {
	now := time.Now()
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]storedRemoteCookie, 0, len(j.saved))
	for key, c := range j.saved {
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			delete(j.saved, key)
			continue
		}
		out = append(out, c)
	}
	return out
}

const remoteSessionsSettingKey = "remote_sessions"

type storedRemoteCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
	Expires  time.Time `json:"expires,omitzero"`
}

type storedRemoteSession struct {
	URL      string               `json:"url"`
	Username string               `json:"username"`
	User     string               `json:"user"`
	Lifetime time.Duration        `json:"lifetime"`
	Cookies  []storedRemoteCookie `json:"cookies"`
}

type remoteProjectsCache struct {
	at       time.Time
	projects []pages.RemoteProject
}

type remoteSessionKey struct{ userID, serverID string }

type remoteSessions struct {
	sync.Mutex
	entries  map[remoteSessionKey]*remoteSession
	projects map[remoteSessionKey]remoteProjectsCache
	client   *http.Client
	hub      *remote.Hub
	settings persistence.SettingsStore
	// storeMu serializes saves of remoteSessionsSettingKey; take it before the main lock.
	storeMu sync.Mutex
	// watchers wake a user's open pages when one of their servers comes or goes.
	watchers map[string]map[chan struct{}]struct{}
	// restoring closes when startup reconnection finishes.
	restoring chan struct{}
}

// watch returns a channel that fires when the user's reachable servers change.
func (r *remoteSessions) watch(userID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	r.Lock()
	if r.watchers == nil {
		r.watchers = map[string]map[chan struct{}]struct{}{}
	}
	if r.watchers[userID] == nil {
		r.watchers[userID] = map[chan struct{}]struct{}{}
	}
	r.watchers[userID][ch] = struct{}{}
	r.Unlock()
	return ch, func() {
		r.Lock()
		delete(r.watchers[userID], ch)
		if len(r.watchers[userID]) == 0 {
			delete(r.watchers, userID)
		}
		r.Unlock()
	}
}

// serverChanged forgets the server's cached projects and wakes the user's pages.
func (r *remoteSessions) serverChanged(userID, serverID string) {
	r.Lock()
	delete(r.projects, remoteSessionKey{userID, serverID})
	r.notifyLocked(userID)
	r.Unlock()
}

func (r *remoteSessions) notifyLocked(userID string) {
	for ch := range r.watchers[userID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (r *remoteSessions) httpClient() *http.Client {
	r.Lock()
	defer r.Unlock()
	if r.client == nil {
		r.client = &http.Client{
			Transport: httpclient.NewTransport(),
			Timeout:   15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return r.client
}

func (r *remoteSessions) get(userID, serverID string) *remoteSession {
	r.Lock()
	defer r.Unlock()
	return r.entries[remoteSessionKey{userID, serverID}]
}

func (r *remoteSessions) put(userID, serverID string, s *remoteSession) {
	r.Lock()
	r.putLocked(userID, serverID, s)
	r.Unlock()
	r.store(remoteSessionKey{userID, serverID})
}

// connect stores a new session only if current still approves the saved server.
// It runs under the lock dropServer takes, so an admin change saved before
// dropServer either fails current or is followed by dropServer removing s.
func (r *remoteSessions) connect(userID, serverID string, s *remoteSession, current func() bool) bool {
	r.Lock()
	if !current() {
		r.Unlock()
		return false
	}
	r.putLocked(userID, serverID, s)
	r.Unlock()
	r.store(remoteSessionKey{userID, serverID})
	return true
}

func (r *remoteSessions) putLocked(userID, serverID string, s *remoteSession) {
	if r.entries == nil {
		r.entries = map[remoteSessionKey]*remoteSession{}
	}
	key := remoteSessionKey{userID, serverID}
	if old := r.entries[key]; old != nil && old != s && old.stop != nil {
		old.stop()
	}
	if s == nil {
		delete(r.entries, key)
	} else {
		r.entries[key] = s
		s.key = key
		if s.jar != nil && s.stop == nil {
			ctx, cancel := context.WithCancel(context.Background())
			s.stop = cancel
			s.jar.changed = func() { r.cookiesChanged(s) }
			go r.keepAlive(ctx, s)
		}
	}
	delete(r.projects, key)
	r.notifyLocked(userID)
	if r.hub == nil {
		return
	}
	if s == nil {
		r.hub.Close(userID, serverID)
		return
	}
	r.hub.Connect(userID, serverID, s.base, s.jar, func() { r.markExpired(userID, serverID, s) })
}

func (r *remoteSessions) cookiesChanged(s *remoteSession) {
	r.Lock()
	key, current := s.key, r.entries[s.key] == s
	r.Unlock()
	if current {
		r.store(key)
	}
}

// keepAlive asks the remote to renew the session well before it lapses. The
// remote renews once half the lifetime is used, so a quarter-lifetime period
// keeps an idle connection signed in.
func (r *remoteSessions) keepAlive(ctx context.Context, s *remoteSession) {
	for {
		period := time.Duration(s.jar.lifetime.Load()) / 4
		if period <= 0 || period > time.Hour {
			period = time.Hour
		} else if period < 30*time.Second {
			period = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(period):
		}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := r.verify(callCtx, s)
		cancel()
		if errors.Is(err, errRemoteLogin) {
			r.markExpired(s.key.userID, s.key.serverID, s)
			return
		}
	}
}

// store writes the current in-memory state of one session to settings. The
// snapshot is taken under storeMu, so a stale snapshot never overwrites a newer one.
func (r *remoteSessions) store(key remoteSessionKey) {
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	r.Lock()
	settings := r.settings
	s := r.entries[key]
	var record *storedRemoteSession
	if s != nil && !s.expired && s.jar != nil {
		record = &storedRemoteSession{URL: s.base.String(), Username: s.username, User: s.user, Lifetime: time.Duration(s.jar.lifetime.Load())}
		record.Cookies = s.jar.storedCookies()
	}
	r.Unlock()
	if settings == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := loadStoredRemoteSessions(ctx, settings)
	if err != nil {
		applog.Infof("[remote] load saved sessions: %v", err)
		return
	}
	if record == nil {
		if _, ok := all[key.userID][key.serverID]; !ok {
			return
		}
		delete(all[key.userID], key.serverID)
		if len(all[key.userID]) == 0 {
			delete(all, key.userID)
		}
	} else {
		if all[key.userID] == nil {
			all[key.userID] = map[string]storedRemoteSession{}
		}
		all[key.userID][key.serverID] = *record
	}
	encoded, err := json.Marshal(all)
	if err == nil {
		err = settings.Set(ctx, remoteSessionsSettingKey, string(encoded))
	}
	if err != nil {
		applog.Infof("[remote] save session: %v", err)
	}
}

func loadStoredRemoteSessions(ctx context.Context, settings persistence.SettingsStore) (map[string]map[string]storedRemoteSession, error) {
	raw, err := settings.Get(ctx, remoteSessionsSettingKey)
	if err != nil {
		return nil, err
	}
	all := map[string]map[string]storedRemoteSession{}
	if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &all) != nil {
		all = map[string]map[string]storedRemoteSession{}
	}
	return all, nil
}

// StartRemoteRestore reconnects saved servers in the background. Until it
// finishes, their pages show a connecting page instead of a reconnect prompt.
func (h *Handler) StartRemoteRestore(ctx context.Context) {
	done := make(chan struct{})
	h.remoteSessions.Lock()
	h.remoteSessions.restoring = done
	h.remoteSessions.Unlock()
	go func() {
		h.RestoreRemoteSessions(ctx)
		close(done)
		// Pages waiting on a server that did not come back show the reconnect prompt.
		h.remoteSessions.Lock()
		for userID := range h.remoteSessions.watchers {
			h.remoteSessions.notifyLocked(userID)
		}
		h.remoteSessions.Unlock()
	}()
}

// restoring reports that startup reconnection is still running.
func (h *Handler) restoring() bool {
	h.remoteSessions.Lock()
	done := h.remoteSessions.restoring
	h.remoteSessions.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

// RestoreRemoteSessions reconnects saved servers using the stored cookies. A
// session the remote rejects is forgotten; an unreachable remote keeps retrying.
func (h *Handler) RestoreRemoteSessions(ctx context.Context) {
	r := &h.remoteSessions
	r.Lock()
	settings := r.settings
	r.Unlock()
	if settings == nil {
		return
	}
	all, err := loadStoredRemoteSessions(ctx, settings)
	if err != nil {
		applog.Infof("[remote] load saved sessions: %v", err)
		return
	}
	var wg sync.WaitGroup
	for userID, servers := range all {
		for serverID, record := range servers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.restoreRemoteSession(ctx, userID, serverID, record)
			}()
		}
	}
	wg.Wait()
}

func (h *Handler) restoreRemoteSession(ctx context.Context, userID, serverID string, record storedRemoteSession) {
	r := &h.remoteSessions
	userCtx := models.WithPrincipal(ctx, models.Principal{UserID: userID})
	key := remoteSessionKey{userID, serverID}
	server, err := h.findSavedServer(userCtx, serverID)
	if err != nil {
		return
	}
	if server == nil || !server.Enabled || server.URL != record.URL || server.Username != record.Username {
		r.store(key)
		return
	}
	s := restoredRemoteSession(*server, record)
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	name, err := r.verify(callCtx, s)
	cancel()
	if errors.Is(err, errRemoteLogin) {
		r.store(key)
		return
	}
	if name != "" {
		s.user = name
	}
	current := func() bool {
		saved, err := h.findSavedServer(userCtx, serverID)
		return err == nil && saved != nil && saved.Enabled && saved.URL == server.URL && saved.Username == server.Username
	}
	if r.get(userID, serverID) == nil {
		r.connect(userID, serverID, s, current)
	}
}

func restoredRemoteSession(server pages.SavedServer, record storedRemoteSession) *remoteSession {
	base, _ := url.Parse(server.URL)
	jar := newRemoteJar()
	jar.lifetime.Store(int64(record.Lifetime))
	now := time.Now()
	jar.mu.Lock()
	cookies := make([]*http.Cookie, 0, len(record.Cookies))
	for _, c := range record.Cookies {
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly, Expires: c.Expires})
		jar.saved[cookieKey(c.Name, c.Path)] = c
	}
	jar.mu.Unlock()
	jar.CookieJar.SetCookies(base, cookies)
	return &remoteSession{jar: jar, base: base, username: server.Username, user: record.User, at: time.Now()}
}

// dropServer signs every user out of a saved server that was removed or changed.
func (r *remoteSessions) dropServer(serverID string) {
	r.Lock()
	var users []string
	var sessions []*remoteSession
	for key, s := range r.entries {
		if key.serverID == serverID {
			users = append(users, key.userID)
			sessions = append(sessions, s)
		}
	}
	r.Unlock()
	for i, userID := range users {
		r.put(userID, serverID, nil)
		go r.logout(sessions[i])
	}
}

// markExpired ignores reports from a replaced session's connection.
func (r *remoteSessions) markExpired(userID, serverID string, s *remoteSession) {
	r.Lock()
	key := remoteSessionKey{userID, serverID}
	if r.entries[key] != s {
		r.Unlock()
		return
	}
	s.expired = true
	if s.stop != nil {
		s.stop()
	}
	delete(r.projects, key)
	r.notifyLocked(userID)
	r.Unlock()
	r.store(key)
}

func principalUserID(ctx context.Context) string {
	if p, ok := models.PrincipalFromContext(ctx); ok && p.UserID != "" {
		return p.UserID
	}
	return models.BootstrapUserID
}

func newServerID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func validServerID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// normalizeServerURL accepts an http(s) origin with an optional path prefix and
// rejects embedded credentials, queries and fragments.
func normalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("enter a valid http:// or https:// server URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

type storedSavedServer struct {
	pages.SavedServer
	Password string `json:"password,omitempty"`
}

func (h *Handler) loadSavedServers(ctx context.Context) ([]pages.SavedServer, error) {
	if h.settingsRepo == nil {
		return nil, nil
	}
	raw, err := h.settingsRepo.Get(ctx, uiPreferenceSavedServersKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, err
	}
	var records []storedSavedServer
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return nil, nil
	}
	valid := make([]pages.SavedServer, 0, len(records))
	for _, record := range records {
		s := record.SavedServer
		s.Password = record.Password
		if validServerID(s.ID) && s.URL != "" {
			valid = append(valid, s)
		}
	}
	return valid, nil
}

func (h *Handler) storeSavedServers(ctx context.Context, servers []pages.SavedServer) error {
	if h.settingsRepo == nil {
		return errors.New("settings unavailable")
	}
	if servers == nil {
		servers = []pages.SavedServer{}
	}
	records := make([]storedSavedServer, 0, len(servers))
	for _, server := range servers {
		records = append(records, storedSavedServer{SavedServer: server, Password: server.Password})
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return h.settingsRepo.Set(ctx, uiPreferenceSavedServersKey, string(encoded))
}

func (h *Handler) findSavedServer(ctx context.Context, id string) (*pages.SavedServer, error) {
	servers, err := h.loadSavedServers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range servers {
		if servers[i].ID == id {
			return &servers[i], nil
		}
	}
	return nil, nil
}

// serverViews joins saved metadata with live session state for rendering.
func (h *Handler) serverViews(ctx context.Context) ([]pages.SavedServer, error) {
	servers, err := h.loadSavedServers(ctx)
	if err != nil {
		return nil, err
	}
	userID := principalUserID(ctx)
	for i := range servers {
		s := h.remoteSessions.get(userID, servers[i].ID)
		switch {
		case !servers[i].Enabled:
			servers[i].Status = pages.ServerStatusDisabled
		case s == nil:
			servers[i].Status = pages.ServerStatusSignedOut
		case s.expired:
			servers[i].Status = pages.ServerStatusExpired
		default:
			servers[i].Status = pages.ServerStatusConnected
			servers[i].SignedInAs = s.user
			if conn := h.remoteHub().Get(userID, servers[i].ID); conn != nil {
				if conn.Down() {
					servers[i].Status = pages.ServerStatusOffline
				}
				servers[i].Version = conn.Version()
			}
		}
	}
	return servers, nil
}

func remoteOrigin(u *url.URL) string { return u.Scheme + "://" + u.Host }

func (r *remoteSessions) newRequest(ctx context.Context, method string, base *url.URL, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, base.String()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", remoteOrigin(base))
	req.Header.Set("Referer", remoteOrigin(base)+"/")
	req.Header.Set("User-Agent", "OpenVibely-Gateway")
	return req, nil
}

// login signs in with the remote server's own local auth and returns a new
// session. The caller replaces any existing session only on success.
func (r *remoteSessions) login(ctx context.Context, server pages.SavedServer, password string) (*remoteSession, error) {
	base, err := url.Parse(server.URL)
	if err != nil {
		return nil, err
	}
	jar := newRemoteJar()
	client := r.httpClient()
	form := url.Values{"username": {server.Username}, "password": {password}, "next": {""}}
	req, err := r.newRequest(ctx, http.MethodPost, base, "/login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach server: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusMethodNotAllowed:
		return nil, errRemoteHostedSSO
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errRemoteRateLimited
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errRemoteLogin
	case resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("the server rejected the sign-in request")
	case resp.StatusCode/100 == 3:
		if loc := resp.Header.Get("Location"); strings.Contains(loc, "/login") {
			return nil, errRemoteLogin
		}
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("this URL is not an OpenVibely server")
	default:
		return nil, fmt.Errorf("unexpected sign-in response (%d)", resp.StatusCode)
	}
	jar.SetCookies(base, resp.Cookies())
	session := &remoteSession{jar: jar, base: base, username: server.Username, user: server.Username, at: time.Now()}
	name, err := r.verify(ctx, session)
	if err != nil {
		return nil, err
	}
	if name != "" {
		session.user = name
	}
	return session, nil
}

func (r *remoteSessions) do(ctx context.Context, s *remoteSession, path string) (*http.Response, error) {
	req, err := r.newRequest(ctx, http.MethodGet, s.base, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for _, c := range s.jar.Cookies(s.base) {
		req.AddCookie(c)
	}
	resp, err := r.httpClient().Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		s.jar.SetCookies(s.base, resp.Cookies())
	}
	return resp, err
}

// verify checks the session and returns the remote's display name for it.
// Only a rejected session is errRemoteLogin; other failures may be transient.
func (r *remoteSessions) verify(ctx context.Context, s *remoteSession) (string, error) {
	resp, err := r.do(ctx, s, "/auth/me")
	if err != nil {
		return "", fmt.Errorf("could not reach server: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode/100 == 3:
		return "", errRemoteLogin
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("unexpected response from server (%d)", resp.StatusCode)
	}
	var me struct {
		Username string `json:"username"`
		Display  string `json:"display"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&me) == nil {
		if me.Display != "" {
			return me.Display, nil
		}
		return me.Username, nil
	}
	return "", nil
}

// remoteProjects returns the connected server's projects for combined navigation.
func (h *Handler) remoteProjects(ctx context.Context, userID string, server pages.SavedServer) []pages.RemoteProject {
	r := &h.remoteSessions
	key := remoteSessionKey{userID, server.ID}
	r.Lock()
	cached, ok := r.projects[key]
	s := r.entries[key]
	r.Unlock()
	if s == nil || s.expired {
		return nil
	}
	if ok && time.Since(cached.at) < 30*time.Second {
		return remoteProjectNames(cached.projects, server.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var items []models.ProjectAPIItem
	if err := h.remoteHub().Read(ctx, server.ID, "Projects", "ListAPIProjects", nil, &items, new(error)); err != nil {
		return remoteProjectNames(cached.projects, server.Name)
	}
	projects := make([]pages.RemoteProject, 0, len(items))
	for _, p := range items {
		projects = append(projects, pages.RemoteProject{ServerID: server.ID, ServerName: server.Name, ID: p.ID, Name: p.Name, RepoKey: p.RepoKey, RepoPending: p.RepoPending})
	}
	r.Lock()
	if r.projects == nil {
		r.projects = map[remoteSessionKey]remoteProjectsCache{}
	}
	if r.entries[key] == s && r.projects[key].at == cached.at {
		r.projects[key] = remoteProjectsCache{at: time.Now(), projects: projects}
	} else if latest, ok := r.projects[key]; ok {
		projects = remoteProjectNames(latest.projects, server.Name)
	}
	r.Unlock()
	return projects
}

// remoteServerGroups loads projects from every connected server concurrently.
func (h *Handler) remoteServerGroups(ctx context.Context) []layout.RemoteServer {
	return memoProjectLookup(ctx, func(c *projectLookupCache) map[string][]layout.RemoteServer { return c.servers }, func() []layout.RemoteServer {
		servers, err := h.loadSavedServers(ctx)
		if err != nil || len(servers) == 0 {
			return nil
		}
		userID := principalUserID(ctx)
		groups := make([]*layout.RemoteServer, len(servers))
		var wg sync.WaitGroup
		for i, server := range servers {
			if s := h.remoteSessions.get(userID, server.ID); !server.Enabled || s == nil || s.expired {
				continue
			}
			if conn := h.remoteHub().Get(userID, server.ID); conn == nil || conn.Down() {
				continue
			}
			wg.Add(1)
			go func(i int, server pages.SavedServer) {
				defer wg.Done()
				projects := h.remoteProjects(ctx, userID, server)
				if s := h.remoteSessions.get(userID, server.ID); s != nil && !s.expired {
					groups[i] = &layout.RemoteServer{ID: server.ID, Name: server.Name, Projects: projects}
				}
			}(i, server)
		}
		wg.Wait()
		var out []layout.RemoteServer
		for _, group := range groups {
			if group != nil {
				out = append(out, *group)
			}
		}
		return out
	})
}

func (r *remoteSessions) logout(s *remoteSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := r.newRequest(ctx, http.MethodPost, s.base, "/logout", nil)
	if err != nil {
		return
	}
	for _, c := range s.jar.Cookies(s.base) {
		req.AddCookie(c)
	}
	if resp, err := r.httpClient().Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

func (h *Handler) remoteHub() *remote.Hub {
	h.remoteSessions.Lock()
	defer h.remoteSessions.Unlock()
	return h.remoteSessions.hub
}

// remoteLiveExecutions lists running executions on every connected server.
func (h *Handler) remoteLiveExecutions(ctx context.Context) []models.LiveExecution {
	servers := h.remoteHub().Servers(principalUserID(ctx))
	parts := make([][]models.LiveExecution, len(servers))
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(remote.WithServer(ctx, server), 3*time.Second)
			defer cancel()
			parts[i], _ = h.execRepo.ListLiveExecutions(callCtx)
		}()
	}
	wg.Wait()
	var out []models.LiveExecution
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// projectsChanged receives an authorized full project projection from its owner.
func (r *remoteSessions) projectsChanged(userID, serverID string, items []models.ProjectAPIItem) {
	r.Lock()
	defer r.Unlock()
	key := remoteSessionKey{userID, serverID}
	if session := r.entries[key]; session == nil || session.expired {
		return
	}
	projects := make([]pages.RemoteProject, 0, len(items))
	for _, p := range items {
		projects = append(projects, pages.RemoteProject{ServerID: serverID, ID: p.ID, Name: p.Name, RepoKey: p.RepoKey, RepoPending: p.RepoPending})
	}
	if r.projects == nil {
		r.projects = map[remoteSessionKey]remoteProjectsCache{}
	}
	r.projects[key] = remoteProjectsCache{at: time.Now(), projects: projects}
	r.notifyLocked(userID)
}

// Names are local connection metadata, not part of the remote project projection.
func remoteProjectNames(projects []pages.RemoteProject, name string) []pages.RemoteProject {
	out := append([]pages.RemoteProject(nil), projects...)
	for i := range out {
		out[i].ServerName = name
	}
	return out
}

func (r *remoteSessions) projectDelta(userID, serverID string, changes remote.ProjectChanges) {
	r.Lock()
	defer r.Unlock()
	key := remoteSessionKey{userID, serverID}
	if session := r.entries[key]; session == nil || session.expired {
		return
	}
	cached, ok := r.projects[key]
	if !ok {
		r.notifyLocked(userID)
		return
	}
	removed := map[string]bool{}
	for _, id := range changes.RemovedIDs {
		removed[id] = true
	}
	upserts := map[string]models.ProjectAPIItem{}
	for _, p := range changes.Projects {
		upserts[p.ID] = p
	}
	projects := make([]pages.RemoteProject, 0, len(cached.projects)+len(upserts))
	for _, p := range cached.projects {
		if removed[p.ID] {
			continue
		}
		if updated, ok := upserts[p.ID]; ok {
			p.Name, p.RepoKey, p.RepoPending, p.RepoPath = updated.Name, updated.RepoKey, updated.RepoPending, updated.RepoPath
			delete(upserts, p.ID)
		}
		projects = append(projects, p)
	}
	for _, p := range changes.Projects {
		if _, ok := upserts[p.ID]; ok {
			projects = append(projects, pages.RemoteProject{ServerID: serverID, ID: p.ID, Name: p.Name, RepoKey: p.RepoKey, RepoPending: p.RepoPending, RepoPath: p.RepoPath})
		}
	}
	r.projects[key] = remoteProjectsCache{at: time.Now(), projects: projects}
	r.notifyLocked(userID)
}
