package pageplugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
)

// ProtocolVersion is negotiated in the initialize handshake.
const ProtocolVersion = "openvibely.page/1"

const (
	MaxRPCMessageBytes = 1 << 20
	DefaultCallTimeout = 30 * time.Second
	HandshakeTimeout   = 10 * time.Second
	ShutdownGrace      = 3 * time.Second
	MaxRestarts        = 3
	RestartWindow      = 5 * time.Minute
	stderrRingLines    = 200
)

var (
	ErrBackendStopped = errors.New("plugin backend is stopped")
	ErrBackendFailed  = errors.New("plugin backend exceeded its restart limit")
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("plugin error %d: %s", e.Code, e.Message) }

// ProcessSpec fully describes how to launch one instance's backend.
type ProcessSpec struct {
	Path       string
	Args       []string
	Dir        string
	Env        map[string]string
	InitParams any
	OnNotify   func(method string, params json.RawMessage)
}

// MinimalEnv builds the environment for plugin processes: no inherited secrets.
func MinimalEnv(extra map[string]string) []string {
	env := []string{}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "PATH", "TEMP", "TMP"} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
	} else {
		env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8")
		if v := os.TempDir(); v != "" {
			env = append(env, "TMPDIR="+v)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// Process supervises a single backend: handshake, request correlation,
// cancellation, timeouts, bounded restarts, and clean shutdown.
type Process struct {
	spec ProcessSpec

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	nextID    int64
	pending   map[int64]chan rpcMessage
	exited    chan struct{}
	stopped   bool
	failed    bool
	restarts  []time.Time
	stderr    []string
	startedAt time.Time
}

func NewProcess(spec ProcessSpec) *Process {
	return &Process{spec: spec, pending: map[int64]chan rpcMessage{}}
}

// Start launches the process and completes the handshake.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startLocked(ctx)
}

func (p *Process) startLocked(ctx context.Context) error {
	if p.stopped {
		return ErrBackendStopped
	}
	if p.failed {
		return ErrBackendFailed
	}
	if p.cmd != nil {
		return nil
	}
	cmd := exec.Command(p.spec.Path, p.spec.Args...)
	cmd.Dir = p.spec.Dir
	cmd.Env = MinimalEnv(p.spec.Env)
	configureProcAttr(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start plugin backend: %w", err)
	}
	exited := make(chan struct{})
	p.cmd, p.stdin, p.exited, p.startedAt = cmd, stdin, exited, time.Now()
	go p.readStdout(cmd, stdout)
	go p.readStderr(stderr)
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd, p.stdin = nil, nil
			for id, ch := range p.pending {
				close(ch)
				delete(p.pending, id)
			}
		}
		p.mu.Unlock()
		close(exited)
	}()

	hctx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	p.mu.Unlock()
	raw, err := p.call(hctx, "initialize", map[string]any{"protocol": ProtocolVersion, "params": p.spec.InitParams})
	p.mu.Lock()
	if err == nil {
		var res struct {
			Protocol string `json:"protocol"`
		}
		if jerr := json.Unmarshal(raw, &res); jerr != nil || res.Protocol != ProtocolVersion {
			err = fmt.Errorf("plugin backend handshake: unsupported protocol %q", res.Protocol)
		}
	}
	if err != nil {
		p.killLocked()
		return fmt.Errorf("plugin backend handshake failed: %w", err)
	}
	return nil
}

func (p *Process) readStdout(cmd *exec.Cmd, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxRPCMessageBytes)
	for sc.Scan() {
		var msg rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil || msg.JSONRPC != "2.0" {
			p.appendStderr("[host] dropped malformed message from plugin")
			continue
		}
		if msg.ID != nil && msg.Method == "" {
			p.mu.Lock()
			ch := p.pending[*msg.ID]
			delete(p.pending, *msg.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.Method != "" && msg.ID == nil && p.spec.OnNotify != nil {
			p.spec.OnNotify(msg.Method, msg.Params)
		}
	}
	if err := sc.Err(); err != nil {
		p.appendStderr("[host] closing plugin: " + err.Error())
		p.mu.Lock()
		if p.cmd == cmd {
			p.killLocked()
		}
		p.mu.Unlock()
	}
}

func (p *Process) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		p.appendStderr(sanitizeLogLine(sc.Text()))
	}
}

func (p *Process) appendStderr(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stderr = append(p.stderr, line)
	if len(p.stderr) > stderrRingLines {
		p.stderr = p.stderr[len(p.stderr)-stderrRingLines:]
	}
	applog.Debugf("[pageplugins] backend %s: %s", p.spec.Path, line)
}

// Logs returns the most recent stderr lines.
func (p *Process) Logs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.stderr...)
}

func (p *Process) write(msg rpcMessage) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(raw) > MaxRPCMessageBytes {
		return fmt.Errorf("request exceeds %d bytes", MaxRPCMessageBytes)
	}
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()
	if stdin == nil {
		return ErrBackendStopped
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = stdin.Write(append(raw, '\n'))
	return err
}

func (p *Process) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.cmd == nil {
		p.mu.Unlock()
		return nil, ErrBackendStopped
	}
	p.nextID++
	id := p.nextID
	ch := make(chan rpcMessage, 1)
	p.pending[id] = ch
	p.mu.Unlock()
	cleanup := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}
	if err := p.write(rpcMessage{JSONRPC: "2.0", ID: &id, Method: method, Params: rawParams}); err != nil {
		cleanup()
		return nil, err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, errors.New("plugin backend exited during request")
		}
		if msg.Error != nil {
			return nil, msg.Error
		}
		if len(msg.Result) > MaxRPCMessageBytes {
			return nil, errors.New("plugin response too large")
		}
		return msg.Result, nil
	case <-ctx.Done():
		cleanup()
		cancelParams, _ := json.Marshal(map[string]int64{"id": id})
		_ = p.write(rpcMessage{JSONRPC: "2.0", Method: "$/cancelRequest", Params: cancelParams})
		return nil, ctx.Err()
	}
}

// Call invokes a method, restarting a crashed backend within the restart budget.
func (p *Process) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultCallTimeout)
		defer cancel()
	}
	p.mu.Lock()
	if p.cmd == nil && !p.stopped && !p.failed {
		if err := p.allowRestartLocked(); err != nil {
			p.mu.Unlock()
			return nil, err
		}
		if err := p.startLocked(ctx); err != nil {
			p.mu.Unlock()
			return nil, err
		}
	}
	stopped, failed := p.stopped, p.failed
	p.mu.Unlock()
	if stopped {
		return nil, ErrBackendStopped
	}
	if failed {
		return nil, ErrBackendFailed
	}
	return p.call(ctx, method, params)
}

func (p *Process) allowRestartLocked() error {
	if p.startedAt.IsZero() {
		return nil
	}
	now := time.Now()
	var recent []time.Time
	for _, t := range p.restarts {
		if now.Sub(t) < RestartWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= MaxRestarts {
		p.failed = true
		p.restarts = recent
		return ErrBackendFailed
	}
	p.restarts = append(recent, now)
	backoff := time.Duration(len(recent)) * 200 * time.Millisecond
	p.mu.Unlock()
	time.Sleep(backoff)
	p.mu.Lock()
	return nil
}

func (p *Process) killLocked() {
	if p.cmd != nil && p.cmd.Process != nil {
		killProcessTree(p.cmd)
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
}

// Running reports whether a process is currently alive.
func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

func (p *Process) Status() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.failed:
		return "failed"
	case p.stopped:
		return "stopped"
	case p.cmd != nil:
		return "running"
	default:
		return "idle"
	}
}

// Stop requests a clean shutdown, then kills after a grace period. Idempotent.
func (p *Process) Stop() {
	p.mu.Lock()
	p.stopped = true
	exited := p.exited
	running := p.cmd != nil
	p.mu.Unlock()
	if !running {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
	defer cancel()
	_, _ = p.call(ctx, "shutdown", map[string]any{})
	_ = p.write(rpcMessage{JSONRPC: "2.0", Method: "exit"})
	p.mu.Lock()
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	p.mu.Unlock()
	select {
	case <-exited:
	case <-time.After(ShutdownGrace):
		p.mu.Lock()
		p.killLocked()
		p.mu.Unlock()
		<-exited
	}
}

// PID is exposed for tests and diagnostics.
func (p *Process) PID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func sanitizeLogLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, s)
}
