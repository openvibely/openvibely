package agenttools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/pkg/executionenv"
)

func testProcessManager(t *testing.T) *processManager {
	t.Helper()
	m := &processManager{sessions: make(map[string]*processSession)}
	t.Cleanup(func() {
		m.mu.Lock()
		sessions := make([]*processSession, 0, len(m.sessions))
		for _, s := range m.sessions {
			sessions = append(sessions, s)
			s.cancel()
		}
		m.mu.Unlock()
		for _, s := range sessions {
			<-s.done
		}
	})
	return m
}

func testCommands(t *testing.T) (echoInput, sleep string) {
	t.Helper()
	shell, err := executionenv.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	switch shell.Name {
	case "powershell":
		return `$line = Read-Host; Write-Output "got:$line"`, `Start-Sleep -Seconds 30`
	case "cmd":
		return `set /p line= & call echo got:%%line%%`, `ping -n 31 127.0.0.1 >nul`
	default:
		return `read -r line; printf 'got:%s\n' "$line"`, `sleep 30`
	}
}

func processID(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "session_id: ") {
			return strings.TrimPrefix(line, "session_id: ")
		}
	}
	t.Fatalf("no session ID: %s", output)
	return ""
}

func TestProcessInputAndCompletion(t *testing.T) {
	m := testProcessManager(t)
	command, _ := testCommands(t)
	dir := t.TempDir()
	out, err := m.start(context.Background(), dir, command, 10, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	id := processID(t, out)
	if _, err := m.interact(context.Background(), t.TempDir(), id, "", false, false, time.Millisecond); err == nil {
		t.Fatal("cross-directory access allowed")
	}
	out, err = m.interact(context.Background(), dir, id, "hello\n", false, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	all := out
	for !strings.Contains(out, "status: completed") {
		out, err = m.interact(context.Background(), dir, id, "", false, false, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		all += out
	}
	if strings.Count(all, "got:hello") != 1 || !strings.Contains(all, "exit_code: 0") {
		t.Fatal(all)
	}
	if _, err := m.interact(context.Background(), dir, id, "", false, false, time.Millisecond); err == nil {
		t.Fatal("completed session was not removed")
	}
}

func TestProcessSurvivesWaitCancellationAndCanStop(t *testing.T) {
	m := testProcessManager(t)
	_, command := testCommands(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	out, err := m.start(ctx, dir, command, 10, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	id := processID(t, out)
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	select {
	case <-s.done:
		t.Fatal("tool wait cancellation killed the process")
	default:
	}
	canceled, cancelPoll := context.WithCancel(context.Background())
	cancelPoll()
	if _, err = m.interact(canceled, dir, id, "", false, false, time.Second); err == nil {
		t.Fatal("canceled poll was accepted")
	}
	out, err = m.interact(context.Background(), dir, id, "", false, true, 5*time.Second)
	if err != nil || !strings.Contains(out, "status: completed") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestProcessTimeoutAfterYield(t *testing.T) {
	m := testProcessManager(t)
	_, command := testCommands(t)
	dir := t.TempDir()
	out, err := m.start(context.Background(), dir, command, 1, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	out, err = m.interact(context.Background(), dir, processID(t, out), "", false, false, 5*time.Second)
	if err != nil || !strings.Contains(out, "command timed out after 1s") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestProcessBufferIsBoundedAndDrained(t *testing.T) {
	s := &processSession{changed: make(chan struct{}, 1)}
	s.Write([]byte(strings.Repeat("a", processBufferLimit*2)))
	s.Write([]byte("tail"))
	if len(s.output) > processBufferLimit {
		t.Fatal("unbounded buffer")
	}
	out, _, _ := s.readResult("")
	if !strings.Contains(out, "output truncated") || !strings.Contains(out, "tail") {
		t.Fatal("truncation or final output lost")
	}
	out, _, _ = s.readResult("")
	if strings.Contains(out, "tail") || strings.Contains(out, "output truncated") {
		t.Fatal("output returned twice")
	}
}

func TestProcessFailedStartDoesNotLeak(t *testing.T) {
	m := testProcessManager(t)
	if _, err := m.start(context.Background(), t.TempDir()+"/missing", "echo hi", 1, time.Millisecond); err == nil {
		t.Fatal("start succeeded")
	}
	if len(m.sessions) != 0 {
		t.Fatal("failed start retained a session")
	}
}

func TestProcessYieldBounds(t *testing.T) {
	integer := func(n int) *int { return &n }
	for _, tc := range []struct {
		name               string
		value              *int
		shell, poll, input time.Duration
	}{
		{"default", nil, 10 * time.Second, 5 * time.Second, 250 * time.Millisecond},
		{"negative", integer(-1), 250 * time.Millisecond, 5 * time.Second, 250 * time.Millisecond},
		{"zero", integer(0), 250 * time.Millisecond, 5 * time.Second, 250 * time.Millisecond},
		{"one second", integer(1000), time.Second, 5 * time.Second, time.Second},
		{"thirty seconds", integer(30000), 30 * time.Second, 30 * time.Second, 30 * time.Second},
		{"five minutes", integer(300000), 30 * time.Second, 5 * time.Minute, 30 * time.Second},
		{"overflow", integer(int(^uint(0) >> 1)), 30 * time.Second, 5 * time.Minute, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := processYield(tc.value, 10000); got != tc.shell {
				t.Fatalf("shell wait = %v, want %v", got, tc.shell)
			}
			if got := processInteractionYield(tc.value, true); got != tc.poll {
				t.Fatalf("poll wait = %v, want %v", got, tc.poll)
			}
			if got := processInteractionYield(tc.value, false); got != tc.input {
				t.Fatalf("input/control wait = %v, want %v", got, tc.input)
			}
		})
	}
}

func TestProcessCloseStdin(t *testing.T) {
	m := testProcessManager(t)
	command, _ := testCommands(t)
	dir := t.TempDir()
	out, err := m.start(context.Background(), dir, command, 10, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	id := processID(t, out)
	out, err = m.interact(context.Background(), dir, id, "", true, false, time.Second)
	for err == nil && !strings.Contains(out, "status: completed") {
		out, err = m.interact(context.Background(), dir, id, "", false, false, time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessShutdown(t *testing.T) {
	m := testProcessManager(t)
	_, command := testCommands(t)
	out, err := m.start(context.Background(), t.TempDir(), command, 10, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	id := processID(t, out)
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	m.close()
	select {
	case <-s.done:
	default:
		t.Fatal("shutdown returned with a live shell")
	}
	if len(m.sessions) != 0 {
		t.Fatal("shutdown retained sessions")
	}
}

func TestProcessExecutionCancellation(t *testing.T) {
	// Cleanups run in reverse order. Stop the independent shell before removing
	// its working directory, which Windows keeps locked while the shell is alive.
	dir := t.TempDir()
	m := testProcessManager(t)
	_, command := testCommands(t)
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	request, cancelRequest := context.WithCancel(WithProcessLifetime(owner))
	defer cancelRequest()
	out, err := m.start(request, dir, command, 30, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	id := processID(t, out)
	m.mu.Lock()
	session := m.sessions[id]
	m.mu.Unlock()
	otherOut, err := m.start(context.Background(), dir, command, 30, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	other := m.sessions[processID(t, otherOut)]
	m.mu.Unlock()
	cancelRequest()
	select {
	case <-session.done:
		t.Fatal("tool request cancellation killed execution process")
	case <-time.After(30 * time.Millisecond):
	}
	cancelOwner()
	select {
	case <-session.done:
	case <-time.After(3 * time.Second):
		t.Fatal("execution cancellation left process running")
	}
	select {
	case <-other.done:
		t.Fatal("cancelled another execution in the same directory")
	default:
	}
	if _, err := m.start(context.WithoutCancel(request), dir, command, 30, time.Millisecond); err == nil {
		t.Fatal("started process for cancelled execution")
	}
}
