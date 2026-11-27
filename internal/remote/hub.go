package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/machinetype"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
)

// Frame is one message from a remote /events/ws socket.
type Frame struct {
	ID     string          `json:"id,omitempty"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data,omitempty"`
	Start  int             `json:"start,omitempty"`
	Offset int             `json:"offset,omitempty"`
}

const liveSubscriptionID = "hub-live"

// silenceTimeout drops a socket that has sent no frame or ping for this long.
// A remote pings every 15 seconds, so silence means the link is dead even when
// no error arrives (power loss, Wi-Fi drop, sleep).
var silenceTimeout = 60 * time.Second

type connKey struct{ userID, serverID string }

// ProjectChanges updates an existing authorized project snapshot.
type ProjectChanges struct {
	Projects   []models.ProjectAPIItem
	RemovedIDs []string
}

// Hub owns one live socket per signed-in user and saved server.
type Hub struct {
	Client *http.Client
	// OnTask and OnChat receive remote events with IDs already tagged.
	OnTask           func(events.TaskEvent)
	OnChat           func(events.ChatEvent)
	OnProjects       func(userID, serverID string, projects []models.ProjectAPIItem)
	OnProjectChanges func(userID, serverID string, changes ProjectChanges)
	// OnChange runs after a server's socket connects or drops.
	OnChange func(userID, serverID string)

	mu     sync.Mutex
	conns  map[connKey]*Conn
	closed bool
	wg     sync.WaitGroup
}

// Connect replaces any socket for the user and server. onExpired runs when the
// remote rejects the session.
func (h *Hub) Connect(userID, serverID string, base *url.URL, jar http.CookieJar, onExpired func()) {
	ws := *base
	if ws.Scheme == "https" {
		ws.Scheme = "wss"
	} else {
		ws.Scheme = "ws"
	}
	ws.Path = strings.TrimRight(base.Path, "/") + "/events/ws"
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		hub: h, userID: userID, serverID: serverID, wsURL: ws.String(), base: base, jar: jar,
		ctx: ctx, cancel: cancel, onExpired: onExpired,
		ready: make(chan struct{}), pending: map[string]chan Frame{}, subs: map[string]*subscription{},
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		return
	}
	if h.conns == nil {
		h.conns = map[connKey]*Conn{}
	}
	old := h.conns[connKey{userID, serverID}]
	h.conns[connKey{userID, serverID}] = c
	h.wg.Add(1)
	h.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	go func() {
		defer h.wg.Done()
		c.run()
	}()
}

// Shutdown closes every socket, waits for them to stop and refuses new ones.
func (h *Hub) Shutdown() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	conns := h.conns
	h.conns = nil
	h.mu.Unlock()
	for _, c := range conns {
		c.cancel()
	}
	h.wg.Wait()
}

func (h *Hub) Close(userID, serverID string) {
	h.mu.Lock()
	c := h.conns[connKey{userID, serverID}]
	delete(h.conns, connKey{userID, serverID})
	h.mu.Unlock()
	if c != nil {
		c.cancel()
	}
}

func (h *Hub) Get(userID, serverID string) *Conn {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[connKey{userID, serverID}]
}

// Servers lists the user's servers that are not down, so reads skip a server
// that is unreachable instead of waiting for it.
func (h *Hub) Servers(userID string) []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	var conns []*Conn
	for k, c := range h.conns {
		if k.userID == userID {
			conns = append(conns, c)
		}
	}
	h.mu.Unlock()
	var out []string
	for _, c := range conns {
		if !c.Down() {
			out = append(out, c.serverID)
		}
	}
	return out
}

// AllServers lists every server the user has a socket for, including down ones.
func (h *Hub) AllServers(userID string) []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for k := range h.conns {
		if k.userID == userID {
			out = append(out, k.serverID)
		}
	}
	return out
}

type subscription struct {
	ch       chan Frame
	overflow atomic.Bool
}

// Conn is one live socket to a remote server. It reconnects until closed.
type Conn struct {
	hub       *Hub
	userID    string
	serverID  string
	wsURL     string
	base      *url.URL
	jar       http.CookieJar
	ctx       context.Context
	cancel    context.CancelFunc
	onExpired func()
	seq       atomic.Uint64

	mu          sync.Mutex
	ws          *websocket.Conn
	ready       chan struct{}
	pending     map[string]chan Frame
	subs        map[string]*subscription
	version     string
	machineType string
	down        bool
}

func (c *Conn) ServerID() string { return c.serverID }

// Down reports that the socket failed and has not reconnected yet. A socket
// still making its first attempt is not down.
func (c *Conn) Down() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.down
}

// Connected reports that the socket is open now.
func (c *Conn) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws != nil
}

func (c *Conn) markDown() {
	c.mu.Lock()
	was := c.down
	c.down = true
	c.mu.Unlock()
	if !was {
		c.changed()
	}
}

func (c *Conn) changed() {
	if c.hub.OnChange != nil {
		c.hub.OnChange(c.userID, c.serverID)
	}
}

// Version is the build version the server announced when the socket opened.
func (c *Conn) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

func (c *Conn) run() {
	backoff := time.Second
	for c.ctx.Err() == nil {
		expired, err := c.session()
		if expired {
			if c.onExpired != nil {
				c.onExpired()
			}
			c.cancel()
			return
		}
		if err != nil {
			applog.Debugf("[remote] server=%s socket: %v", c.serverID, err)
			if c.ctx.Err() == nil {
				c.markDown()
			}
		} else {
			backoff = time.Second
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Conn) header() http.Header {
	header := http.Header{}
	header.Set("Origin", c.base.Scheme+"://"+c.base.Host)
	header.Set("User-Agent", "OpenVibely-Hub")
	var cookies []string
	for _, cookie := range c.jar.Cookies(c.base) {
		cookies = append(cookies, cookie.Name+"="+cookie.Value)
	}
	if len(cookies) > 0 {
		header.Set("Cookie", strings.Join(cookies, "; "))
	}
	return header
}

func (c *Conn) httpClient() *http.Client {
	client := c.hub.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &http.Client{Transport: client.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *Conn) session() (expired bool, err error) {
	dialCtx, stop := context.WithTimeout(c.ctx, 15*time.Second)
	defer stop()
	var lastHeard atomic.Int64
	heard := func() { lastHeard.Store(time.Now().UnixNano()) }
	ws, resp, err := websocket.Dial(dialCtx, c.wsURL, &websocket.DialOptions{
		HTTPHeader:     c.header(),
		OnPingReceived: func(context.Context, []byte) bool { heard(); return true },
		HTTPClient:     c.httpClient(),
		Subprotocols:   []string{"openvibely-live"},
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || c.signInRedirect(resp)) {
			return true, err
		}
		return false, err
	}
	ws.SetReadLimit(-1)
	defer ws.CloseNow()
	var first Frame
	if err := wsjson.Read(dialCtx, ws, &first); err != nil {
		return false, err
	}
	if first.Event != "ready" {
		return false, errors.New("unexpected first frame " + first.Event)
	}
	var hello struct {
		Version     string `json:"version"`
		MachineType string `json:"machine_type"`
	}
	_ = json.Unmarshal(first.Data, &hello)
	c.mu.Lock()
	c.ws = ws
	c.version = hello.Version
	c.machineType = machinetype.Normalize(hello.MachineType)
	c.down = false
	close(c.ready)
	c.mu.Unlock()
	defer c.disconnected()
	c.changed()
	if err := wsjson.Write(c.ctx, ws, map[string]string{"op": "live", "id": liveSubscriptionID, "project_id": models.AllProjectsID}); err != nil {
		return false, err
	}
	heard()
	timeout := silenceTimeout
	watchCtx, stopWatch := context.WithCancel(c.ctx)
	defer stopWatch()
	go func() {
		tick := time.NewTicker(timeout / 4)
		defer tick.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-tick.C:
				if time.Since(time.Unix(0, lastHeard.Load())) > timeout {
					ws.CloseNow()
					return
				}
			}
		}
	}()
	for {
		var f Frame
		err := wsjson.Read(c.ctx, ws, &f)
		if err != nil {
			return false, err
		}
		heard()
		c.dispatch(f)
	}
}

func (c *Conn) disconnected() {
	c.mu.Lock()
	c.ws = nil
	c.ready = make(chan struct{})
	pending, subs := c.pending, c.subs
	c.pending, c.subs = map[string]chan Frame{}, map[string]*subscription{}
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	for _, s := range subs {
		close(s.ch)
	}
}

func (c *Conn) dispatch(f Frame) {
	if f.ID == "" && f.Event == "repository_project_changes" {
		var changes ProjectChanges
		if json.Unmarshal(f.Data, &changes) == nil && c.hub.OnProjectChanges != nil {
			Tag(c.serverID, &changes.Projects)
			for i, id := range changes.RemovedIDs {
				changes.RemovedIDs[i] = remoteid.ID(c.serverID, id)
			}
			c.hub.OnProjectChanges(c.userID, c.serverID, changes)
		}
		return
	}

	if f.ID == "" && f.Event == "repository_projects" {
		var projects []models.ProjectAPIItem
		if json.Unmarshal(f.Data, &projects) == nil && c.hub.OnProjects != nil {
			Tag(c.serverID, &projects)
			c.hub.OnProjects(c.userID, c.serverID, projects)
		}
		return
	}

	if f.ID == liveSubscriptionID {
		switch f.Event {
		case "subscribed":
		case "retry", "error", "done":
			go func() {
				select {
				case <-c.ctx.Done():
				case <-time.After(5 * time.Second):
					_ = c.write(map[string]string{"op": "live", "id": liveSubscriptionID, "project_id": models.AllProjectsID})
				}
			}()
		default:
			c.publish(f)
		}
		return
	}
	if f.ID == "" {
		return
	}
	c.mu.Lock()
	if ch := c.pending[f.ID]; ch != nil {
		delete(c.pending, f.ID)
		c.mu.Unlock()
		ch <- f
		return
	}
	s := c.subs[f.ID]
	if s == nil {
		c.mu.Unlock()
		return
	}
	select {
	case s.ch <- f:
		c.mu.Unlock()
	default:
		// The reader is slow; it resumes from its last offset after this stream ends.
		s.overflow.Store(true)
		delete(c.subs, f.ID)
		close(s.ch)
		c.mu.Unlock()
		_ = c.write(map[string]string{"op": "unsubscribe", "id": f.ID})
	}
}

func (c *Conn) publish(f Frame) {
	if strings.HasPrefix(f.Event, "chat_") {
		var ev events.ChatEvent
		if json.Unmarshal(f.Data, &ev) != nil || c.hub.OnChat == nil {
			return
		}
		if relayedEvent(ev.ProjectID, ev.TaskID, ev.ExecID) {
			return
		}
		Tag(c.serverID, &ev)
		ev.RemoteUserID = c.userID
		c.hub.OnChat(ev)
		return
	}
	var ev events.TaskEvent
	if json.Unmarshal(f.Data, &ev) != nil || ev.Type == "" || c.hub.OnTask == nil {
		return
	}
	if relayedEvent(ev.ProjectID, ev.TaskID, ev.ExecID) {
		return
	}
	Tag(c.serverID, &ev)
	ev.RemoteUserID = c.userID
	c.hub.OnTask(ev)
}

// A hub consumes only events owned by its peer. Remote IDs belong to that
// peer's saved-server namespace, not ours. Importing them again would circulate
// events indefinitely when two servers are connected to each other.
func relayedEvent(ids ...string) bool {
	for _, id := range ids {
		if _, remote := remoteid.ServerOf(id); remote {
			return true
		}
	}
	return false
}

func (c *Conn) write(v any) error {
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	if ws == nil {
		return ErrUnavailable
	}
	ctx, stop := context.WithTimeout(c.ctx, 30*time.Second)
	defer stop()
	return wsjson.Write(ctx, ws, v)
}

func (c *Conn) waitReady(ctx context.Context) error {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	wait, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	select {
	case <-ready:
		return nil
	case <-c.ctx.Done():
		return ErrUnavailable
	case <-wait.Done():
		return ErrUnavailable
	}
}

// Call runs one read on the remote server.
func (c *Conn) Call(ctx context.Context, read Read) (Result, error) {
	return c.request(ctx, map[string]any{"op": "read", "read": read}, 60*time.Second)
}

// Act runs one named action on the remote server.
func (c *Conn) Act(ctx context.Context, action Action) (Result, error) {
	return c.request(ctx, map[string]any{"op": "action", "action": action}, 15*time.Minute)
}

func (c *Conn) request(ctx context.Context, cmd map[string]any, timeout time.Duration) (Result, error) {
	if err := c.waitReady(ctx); err != nil {
		return Result{}, err
	}
	id := "q" + strconv.FormatUint(c.seq.Add(1), 36)
	ch := make(chan Frame, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	cmd["id"] = id
	if err := c.write(cmd); err != nil {
		return Result{}, ErrUnavailable
	}
	wait, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	select {
	case f, ok := <-ch:
		if !ok {
			return Result{}, ErrUnavailable
		}
		var r Result
		if err := json.Unmarshal(f.Data, &r); err != nil {
			return Result{}, err
		}
		return r, nil
	case <-wait.Done():
		return Result{}, ErrUnavailable
	}
}

// Stream relays one execution subscription. The returned channel closes when the
// stream ends; overflowed reports whether it ended because the reader fell behind.
func (c *Conn) Stream(ctx context.Context, projectID, taskID, execID string, offset int) (<-chan Frame, func() bool, func(), error) {
	return c.Subscribe(ctx, map[string]any{"op": "execution", "project_id": projectID, "task_id": taskID, "exec_id": execID, "offset": offset})
}

// Subscribe relays one live subscription command, such as "op":"computer".
// IDs in cmd must already be the server's own.
func (c *Conn) Subscribe(ctx context.Context, cmd map[string]any) (<-chan Frame, func() bool, func(), error) {
	if err := c.waitReady(ctx); err != nil {
		return nil, nil, nil, err
	}
	id := "x" + strconv.FormatUint(c.seq.Add(1), 36)
	s := &subscription{ch: make(chan Frame, 256)}
	c.mu.Lock()
	c.subs[id] = s
	c.mu.Unlock()
	stop := func() {
		c.mu.Lock()
		if c.subs[id] == s {
			delete(c.subs, id)
			close(s.ch)
		}
		c.mu.Unlock()
		_ = c.write(map[string]string{"op": "unsubscribe", "id": id})
	}
	cmd["id"] = id
	if err := c.write(cmd); err != nil {
		stop()
		return nil, nil, nil, ErrUnavailable
	}
	return s.ch, s.overflow.Load, stop, nil
}

// Dial opens a separate socket to path on the server with the saved sign-in,
// for traffic too heavy to share the live socket, such as a computer screen.
func (c *Conn) Dial(ctx context.Context, path string, query url.Values, subprotocols []string) (*websocket.Conn, error) {
	u := *c.base
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawQuery = query.Encode()
	dialCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	ws, resp, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{HTTPHeader: c.header(), HTTPClient: c.httpClient(), Subprotocols: subprotocols})
	if err != nil && resp != nil && (resp.StatusCode == http.StatusUnauthorized || c.signInRedirect(resp)) {
		if c.onExpired != nil {
			c.onExpired()
		}
		c.cancel()
		return nil, ErrSessionExpired
	}
	return ws, err
}

// ErrSessionExpired reports that the server rejected the saved sign-in.
var ErrSessionExpired = errors.New("remote server session expired")

// signInRedirect reports whether resp sends a signed-out request to the
// server's own sign-in page. Other redirects, such as a proxy's, are transient.
func (c *Conn) signInRedirect(resp *http.Response) bool {
	if resp.StatusCode/100 != 3 {
		return false
	}
	loc, err := resp.Location()
	if err != nil || !strings.EqualFold(loc.Host, c.base.Host) {
		return false
	}
	path := strings.TrimPrefix(loc.Path, strings.TrimRight(c.base.Path, "/"))
	return path == "/login" || path == "/auth/sso/start"
}

// MachineType is the hardware category announced by the peer.
func (c *Conn) MachineType() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return machinetype.Normalize(c.machineType)
}
