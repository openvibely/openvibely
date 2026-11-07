package pageplugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

const validManifest = `{
  "schema_version": 1,
  "id": "acme.widgets",
  "name": "Widgets",
  "version": "1.2.3",
  "pages": [{"id": "main", "title": "Widgets", "entry": "index.html"}],
  "backend": {"executables": {"darwin-arm64": "bin/w-mac", "any": "bin/w.sh"}, "args": ["--stdio"]},
  "permissions": ["backend.exec", "events"],
  "operations": [{"name": "refresh", "description": "Refresh", "kind": "read", "expose_as_tool": true, "emits": ["data.changed"]}]
}`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if !m.RunsProcess() || m.Pages[0].Entry != "index.html" {
		t.Fatalf("unexpected manifest %+v", m)
	}
	cases := map[string]string{
		"unknown field":        strings.Replace(validManifest, `"name"`, `"nmae": "x", "name"`, 1),
		"traversal entry":      strings.Replace(validManifest, `"index.html"`, `"../index.html"`, 1),
		"absolute entry":       strings.Replace(validManifest, `"index.html"`, `"/etc/index.html"`, 1),
		"non-html entry":       strings.Replace(validManifest, `"index.html"`, `"index.js"`, 1),
		"bad version":          strings.Replace(validManifest, `"1.2.3"`, `"latest"`, 1),
		"unknown permission":   strings.Replace(validManifest, `"backend.exec", "events"`, `"backend.exec", "events", "root"`, 1),
		"backend without perm": strings.Replace(validManifest, `"backend.exec", "events"`, `"events"`, 1),
		"emits without perm":   strings.Replace(validManifest, `"backend.exec", "events"`, `"backend.exec"`, 1),
		"exe traversal":        strings.Replace(validManifest, `"bin/w.sh"`, `"../../bin/sh"`, 1),
		"schema version":       strings.Replace(validManifest, `"schema_version": 1`, `"schema_version": 2`, 1),
	}
	for name, raw := range cases {
		if _, err := ParseManifest([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseManifestNormalizesEntry(t *testing.T) {
	m, err := ParseManifest([]byte(strings.Replace(validManifest, `"index.html"`, `"./index.html"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Pages[0].Entry != "index.html" {
		t.Fatalf("entry not normalized: %q", m.Pages[0].Entry)
	}
}

func TestSelectExecutable(t *testing.T) {
	m, _ := ParseManifest([]byte(validManifest))
	if exe, _ := m.Backend.SelectExecutable("darwin", "arm64"); exe != "bin/w-mac" {
		t.Fatalf("exact platform not preferred: %s", exe)
	}
	if exe, _ := m.Backend.SelectExecutable("linux", "amd64"); exe != "bin/w.sh" {
		t.Fatalf("any fallback not used: %s", exe)
	}
	delete(m.Backend.Executables, "any")
	if _, err := m.Backend.SelectExecutable("linux", "amd64"); err == nil {
		t.Fatal("missing platform accepted")
	}
}

func TestSafeJoinRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o644)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644)
	if _, err := SafeJoin(root, "ok.txt"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../secret.txt", "/etc/passwd", "a/../../x", "a\\b", ""} {
		if _, err := SafeJoin(root, rel); err == nil {
			t.Errorf("%q accepted", rel)
		}
	}
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt"))
		if _, err := SafeJoin(root, "link.txt"); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
}

func TestHashTreeDetectsChanges(t *testing.T) {
	a := fstest.MapFS{"plugin.json": {Data: []byte("x")}, "index.html": {Data: []byte("a")}}
	b := fstest.MapFS{"plugin.json": {Data: []byte("x")}, "index.html": {Data: []byte("b")}}
	ha, err := HashTree(a)
	if err != nil {
		t.Fatal(err)
	}
	ha2, _ := HashTree(a)
	hb, _ := HashTree(b)
	if ha != ha2 || ha == hb {
		t.Fatalf("hash not content-addressed: %s %s %s", ha, ha2, hb)
	}
}

func TestHashTreeFileBoundariesAreUnambiguous(t *testing.T) {
	a := fstest.MapFS{"a": {Data: []byte("x\x00b\x00y")}, "b": {Data: []byte("z")}}
	b := fstest.MapFS{"a": {Data: []byte("x")}, "b": {Data: []byte("y\x00b\x00z")}}
	ha, err := HashTree(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashTree(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Fatalf("different package trees produced the same hash %s", ha)
	}
	if !strings.HasPrefix(ha, HashPrefix) {
		t.Fatalf("hash %q missing %q prefix", ha, HashPrefix)
	}
}

func TestParseBridgeMessage(t *testing.T) {
	ok := `{"v":1,"type":"op.invoke","id":"m1","handle":"h","payload":{"operation":"refresh_data"}}`
	m, err := ParseBridgeMessage([]byte(ok), "h")
	if err != nil || m.Type != BridgeOpInvoke {
		t.Fatalf("valid message rejected: %v", err)
	}
	bad := map[string]string{
		"wrong handle":  strings.Replace(ok, `"handle":"h"`, `"handle":"other"`, 1),
		"wrong version": strings.Replace(ok, `"v":1`, `"v":2`, 1),
		"unknown type":  strings.Replace(ok, `"op.invoke"`, `"eval"`, 1),
		"unknown field": strings.Replace(ok, `"id":"m1"`, `"id":"m1","project_id":"p"`, 1),
		"array payload": strings.Replace(ok, `{"operation":"refresh_data"}`, `[1]`, 1),
		"missing id":    strings.Replace(ok, `"id":"m1"`, `"id":""`, 1),
		"oversized":     `{"v":1,"type":"context.set","id":"m","handle":"h","payload":{"summary":"` + strings.Repeat("x", MaxBridgeMessageBytes) + `"}}`,
	}
	for name, raw := range bad {
		if _, err := ParseBridgeMessage([]byte(raw), "h"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var p OpInvokePayload
	if err := DecodeStrict(json.RawMessage(`{"operation":"x","sneaky":1}`), &p); err == nil {
		t.Fatal("unknown payload field accepted")
	}
}

func TestProcessLifecycle(t *testing.T) {
	var notes []string
	spec := helperSpec(t)
	spec.OnNotify = func(method string, _ json.RawMessage) { notes = append(notes, method) }
	p := NewProcess(spec)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	out, err := p.Call(ctx, "operation/invoke", map[string]any{"operation": "echo", "input": map[string]int{"a": 1}})
	if err != nil || !strings.Contains(string(out), `"a":1`) {
		t.Fatalf("echo: %s %v", out, err)
	}
	if _, err := p.Call(ctx, "operation/invoke", map[string]any{"operation": "notify"}); err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0] != "state/set" || notes[1] != "events/publish" {
		t.Fatalf("notifications not delivered in order: %v", notes)
	}

	cctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := p.Call(cctx, "operation/invoke", map[string]any{"operation": "slow"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout not enforced: %v", err)
	}

	firstPID := p.PID()
	if _, err := p.Call(ctx, "operation/invoke", map[string]any{"operation": "crash"}); err == nil {
		t.Fatal("crash not reported")
	}
	deadline := time.Now().Add(3 * time.Second)
	for p.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out, err = p.Call(ctx, "operation/invoke", map[string]any{"operation": "echo", "input": map[string]int{}})
	if err != nil || p.PID() == firstPID {
		t.Fatalf("backend not restarted after crash: %v", err)
	}
	if logs := p.Logs(); len(logs) == 0 || strings.ContainsRune(strings.Join(logs, ""), 0x1b) {
		t.Fatalf("stderr not captured/sanitized: %q", logs)
	}

	p.Stop()
	p.Stop()
	if p.Running() || p.Status() != "stopped" {
		t.Fatalf("not stopped: %s", p.Status())
	}
	if _, err := p.Call(ctx, "operation/invoke", map[string]any{"operation": "echo"}); !errors.Is(err, ErrBackendStopped) {
		t.Fatalf("stopped backend served call: %v", err)
	}
}

func TestProcessRestartBudget(t *testing.T) {
	p := NewProcess(helperSpec(t))
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	var err error
	for i := 0; i <= MaxRestarts+1; i++ {
		_, err = p.Call(ctx, "operation/invoke", map[string]any{"operation": "crash"})
		for j := 0; j < 300 && p.Running(); j++ {
			time.Sleep(10 * time.Millisecond)
		}
		if errors.Is(err, ErrBackendFailed) {
			break
		}
	}
	if !errors.Is(err, ErrBackendFailed) || p.Status() != "failed" {
		t.Fatalf("restart budget not enforced: %v %s", err, p.Status())
	}
}

func TestMinimalEnvDropsSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	for _, kv := range MinimalEnv(map[string]string{"X": "1"}) {
		if strings.Contains(kv, "sk-secret") {
			t.Fatal("secret leaked into plugin env")
		}
	}
}

func TestProcessPreparationFailureDoesNotLaunchOrReuseSnapshot(t *testing.T) {
	spec := helperSpec(t)
	blocked := false
	calls := 0
	unavailable := errors.New("saved state unavailable")
	spec.PrepareStart = func(context.Context) (any, error) {
		calls++
		if blocked {
			return nil, unavailable
		}
		return map[string]any{"state": map[string]int{"revision": calls}}, nil
	}
	p := NewProcess(spec)
	defer p.Stop()
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if _, err := p.Call(t.Context(), "operation/invoke", map[string]any{"operation": "crash"}); err == nil {
		t.Fatal("expected crash")
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("backend did not exit")
	}
	blocked = true
	if _, err := p.Call(t.Context(), "operation/invoke", map[string]any{"operation": "echo"}); !errors.Is(err, unavailable) {
		t.Fatalf("state read error: %v", err)
	}
	if p.PID() != 0 || p.Running() || calls != 2 {
		t.Fatal("process launched despite failed state read")
	}
	blocked = false
	raw, err := p.Call(t.Context(), "operation/invoke", map[string]any{"operation": "echo"})
	if err != nil || !strings.Contains(string(raw), `"revision":3`) {
		t.Fatalf("recovery reused snapshot: %s %v", raw, err)
	}
}

func TestStoppedProcessCannotDeliverLateNotifications(t *testing.T) {
	var mu sync.Mutex
	var applied []string
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	spec := helperSpec(t)
	spec.OnNotify = func(method string, _ json.RawMessage) {
		entered <- struct{}{}
		<-release
		mu.Lock()
		applied = append(applied, method)
		mu.Unlock()
	}
	p := NewProcess(spec)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = p.Call(ctx, "operation/invoke", map[string]any{"operation": "notify"}) }()
	<-entered

	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a notification handler was still writing")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	<-stopped

	mu.Lock()
	defer mu.Unlock()
	if len(applied) != 1 || applied[0] != "state/set" {
		t.Fatalf("notifications after stop were delivered: %v", applied)
	}
}
