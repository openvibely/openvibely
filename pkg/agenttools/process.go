package agenttools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/pkg/executionenv"
)

const (
	processBufferLimit  = 1024 * 1024
	processOutputLimit  = 50000
	processSessionLimit = 64
	processRetention    = 5 * time.Minute
)

// Sessions survive individual model/tool requests, but have a bounded lifetime.
// IDs are random capabilities; access also requires the original working directory.
var shellProcesses = &processManager{sessions: make(map[string]*processSession)}

type processLifetimeKey struct{}

// WithProcessLifetime binds shell sessions to the owning execution context.
// Child tool-request cancellation only ends the wait; execution cancellation
// terminates the processes, including sessions that have already yielded.
func WithProcessLifetime(ctx context.Context) context.Context {
	return context.WithValue(ctx, processLifetimeKey{}, ctx)
}

func processLifetime(ctx context.Context) context.Context {
	if owner, ok := ctx.Value(processLifetimeKey{}).(context.Context); ok {
		return owner
	}
	return context.Background()
}

type processManager struct {
	closed   bool
	mu       sync.Mutex
	sessions map[string]*processSession
}

type processSession struct {
	expiry      *time.Timer // guarded by manager.mu
	id, dir     string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	cancel      context.CancelFunc
	done        chan struct{}
	changed     chan struct{}
	interaction sync.Mutex
	mu          sync.Mutex
	output      []byte
	dropped     int
	finished    bool
	exitCode    int
	timedOut    bool
	timeout     int
}

func (s *processSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	if len(p) >= processBufferLimit {
		s.dropped += len(s.output) + len(p) - processBufferLimit
		s.output = append(s.output[:0], p[len(p)-processBufferLimit:]...)
	} else {
		overflow := len(s.output) + len(p) - processBufferLimit
		if overflow > 0 {
			s.dropped += overflow
			copy(s.output, s.output[overflow:])
			s.output = s.output[:len(s.output)-overflow]
		}
		s.output = append(s.output, p...)
	}
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return n, nil
}

func processWorkDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	return filepath.Abs(dir)
}

func processYield(value *int, fallback int) time.Duration {
	return boundedProcessYield(value, fallback, 250, 30000)
}

func boundedProcessYield(value *int, fallback, minimum, maximum int) time.Duration {
	n := fallback
	if value != nil {
		n = *value
	}
	return time.Duration(min(maximum, max(minimum, n))) * time.Millisecond
}

func processInteractionYield(value *int, polling bool) time.Duration {
	if polling {
		return boundedProcessYield(value, 5000, 5000, 300000)
	}
	return processYield(value, 250)
}

func (m *processManager) remove(s *processSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.id] == s {
		if s.expiry != nil {
			s.expiry.Stop()
		}
		delete(m.sessions, s.id)
	}
}

func (m *processManager) start(ctx context.Context, dir, command string, timeout int, yield time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir, err := processWorkDir(dir)
	if err != nil {
		return "", err
	}
	shell, err := executionenv.Resolve()
	if err != nil {
		return "", err
	}
	// Individual requests bound waits; the owning execution bounds process lifetime.
	life, cancel := context.WithTimeout(processLifetime(ctx), time.Duration(timeout)*time.Second)
	cmd := exec.CommandContext(life, shell.Path, shell.Args(command)...)
	cmd.Dir = dir
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	afterStart, cleanup, err := configureProcess(cmd)
	if err != nil {
		cancel()
		return "", fmt.Errorf("prepare shell process: %w", err)
	}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		cleanup()
		return "", err
	}
	s := &processSession{dir: dir, cmd: cmd, stdin: stdin, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}, 1), timeout: timeout}
	cmd.Stdout, cmd.Stderr = s, s
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		cancel()
		cleanup()
		stdin.Close()
		return "", err
	}
	if err := life.Err(); err != nil {
		m.mu.Unlock()
		cancel()
		cleanup()
		stdin.Close()
		return "", err
	}
	if m.closed || len(m.sessions) >= processSessionLimit {
		m.mu.Unlock()
		cancel()
		cleanup()
		stdin.Close()
		return "", fmt.Errorf("shell session limit reached; finish or stop existing sessions before starting more")
	}
	for {
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			m.mu.Unlock()
			cancel()
			cleanup()
			stdin.Close()
			return "", err
		}
		s.id = "sh_" + hex.EncodeToString(id[:])
		if m.sessions[s.id] == nil {
			break
		}
	}
	if err = cmd.Start(); err != nil {
		m.mu.Unlock()
		cancel()
		cleanup()
		stdin.Close()
		return "", fmt.Errorf("start shell: %w", err)
	}
	if err = afterStart(); err != nil {
		m.mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cleanup()
		cancel()
		stdin.Close()
		return "", fmt.Errorf("initialize shell process: %w", err)
	}
	if life.Err() != nil {
		_ = cmd.Cancel()
	}
	m.sessions[s.id] = s
	m.mu.Unlock()
	go func() {
		waitErr := cmd.Wait()
		cleanup()
		stdin.Close()
		s.mu.Lock()
		s.exitCode = cmd.ProcessState.ExitCode()
		if waitErr != nil && s.exitCode == 0 {
			s.exitCode = -1
		}
		s.timedOut = life.Err() == context.DeadlineExceeded
		s.finished = true
		s.mu.Unlock()
		cancel()
		close(s.done)
		m.mu.Lock()
		if m.sessions[s.id] == s {
			s.expiry = time.AfterFunc(processRetention, func() { m.remove(s) })
		}
		m.mu.Unlock()
	}()
	s.interaction.Lock()
	defer s.interaction.Unlock()
	// Starting a command waits for completion or the initial yield, not each byte.
	timer := time.NewTimer(yield)
	defer timer.Stop()
	select {
	case <-s.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	output, finished, timedOut := s.readResult("")
	if finished {
		m.remove(s)
	}
	if timedOut {
		return output, fmt.Errorf("command timed out")
	}
	return output, nil
}

func (s *processSession) readResult(note string) (string, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Return a bounded tail. Explicitly report discarded output, never accumulate
	// unread output without a cap while the model is doing something else.
	if len(s.output) > processOutputLimit {
		s.dropped += len(s.output) - processOutputLimit
		s.output = s.output[len(s.output)-processOutputLimit:]
	}
	output := string(s.output)
	s.output = nil
	if s.dropped > 0 {
		output = fmt.Sprintf("[output truncated: %d earlier bytes omitted]\n", s.dropped) + output
		s.dropped = 0
	}
	if note != "" {
		output += "\n" + note
	}
	if s.finished {
		output += fmt.Sprintf("\n[process]\nstatus: completed\nexit_code: %d\n", s.exitCode)
		if s.timedOut {
			output += fmt.Sprintf("command timed out after %ds\n", s.timeout)
		}
		output += "[/process]"
	} else {
		output += "\n[process]\nsession_id: " + s.id + "\nstatus: running\nnext: call process with this session_id to read output or send input\n[/process]"
	}
	return output, s.finished, s.timedOut
}

func (m *processManager) interact(ctx context.Context, dir, id, input string, closeInput, terminate bool, yield time.Duration) (string, error) {
	dir, err := processWorkDir(dir)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil || s.dir != dir {
		return "", fmt.Errorf("unknown or expired process session for this working directory")
	}
	if !s.interaction.TryLock() {
		return "", fmt.Errorf("another process call is using this session; retry after it returns")
	}
	defer s.interaction.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	note := ""
	if terminate {
		s.cancel()
	} else if input != "" {
		select {
		case <-s.done:
			note = "Input was not sent: process already exited."
		default:
			// A child need not read stdin. Bound the write and close the pipe if it
			// stalls so an abandoned writer cannot outlive the process session.
			wrote := make(chan error, 1)
			go func() { _, e := io.WriteString(s.stdin, input); wrote <- e }()
			timer := time.NewTimer(5 * time.Second)
			select {
			case err = <-wrote:
				if err != nil {
					note = "Input write failed: " + err.Error()
				}
			case <-timer.C:
				s.stdin.Close()
				note = "Input write timed out; stdin closed. Input may have been partially sent."
			case <-ctx.Done():
				s.stdin.Close()
				note = "Input write interrupted; stdin closed. Input may have been partially sent."
			}
			timer.Stop()
		}
	}
	if closeInput {
		s.stdin.Close()
	}
	// Return already-buffered output immediately; otherwise wait for new output,
	// completion, cancellation, or the model's requested wait duration.
	timer := time.NewTimer(yield)
	defer timer.Stop()
	for {
		s.mu.Lock()
		ready := s.finished || len(s.output) > 0
		s.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-s.changed:
			continue
		case <-s.done:
		case <-timer.C:
		case <-ctx.Done():
		}
		break
	}
	output, finished, _ := s.readResult(note)
	if finished {
		m.remove(s)
	}
	return output, nil
}

func execBash(ctx context.Context, workDir string, input json.RawMessage) (string, error) {
	var p struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
		Yield   *int   `json:"yield_time_ms"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if p.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	return shellProcesses.start(ctx, workDir, p.Command, NormalizeBashTimeout(p.Timeout), processYield(p.Yield, 10000))
}

func execProcess(ctx context.Context, workDir string, input json.RawMessage) (string, error) {
	var p struct {
		SessionID  string `json:"session_id"`
		Input      string `json:"input"`
		Yield      *int   `json:"yield_time_ms"`
		CloseStdin bool   `json:"close_stdin"`
		Terminate  bool   `json:"terminate"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if p.SessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}
	if len(p.Input) > 65536 {
		return "", fmt.Errorf("input exceeds 65536 bytes; send smaller chunks")
	}
	if p.Terminate && (p.Input != "" || p.CloseStdin) {
		return "", fmt.Errorf("terminate cannot be combined with input or close_stdin")
	}
	polling := p.Input == "" && !p.CloseStdin && !p.Terminate
	return shellProcesses.interact(ctx, workDir, p.SessionID, p.Input, p.CloseStdin, p.Terminate, processInteractionYield(p.Yield, polling))
}

// ShutdownProcesses stops background shell processes during application shutdown.
// New shell starts are rejected while shutdown is draining processes.
func ShutdownProcesses() { shellProcesses.close() }

func (m *processManager) close() {
	m.mu.Lock()
	m.closed = true
	sessions := make([]*processSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
		if s.expiry != nil {
			s.expiry.Stop()
		}
		s.cancel()
	}
	clear(m.sessions)
	m.mu.Unlock()
	for _, s := range sessions {
		<-s.done
	}
	m.mu.Lock()
	m.closed = false
	m.mu.Unlock()
}
