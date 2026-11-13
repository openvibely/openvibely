package gitidentity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var testRuntime *Runtime

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "gitidentity-tests-")
	if err != nil {
		panic(err)
	}
	testRuntime, err = Prepare(root)
	if err != nil {
		os.RemoveAll(root)
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

func testResolver(ctx context.Context, r Resolver) context.Context {
	return WithResolver(WithRuntime(ctx, testRuntime), r)
}

func TestRuntimeWithoutSystemUtilitiesOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell wrapper")
	}
	root := filepath.Join(t.TempDir(), "app data with spaces")
	rt, err := Prepare(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRuntime(identityContext("Alice"), rt)
	// Environment prepends only the wrapper directory to this empty PATH.
	// The real Git executable is supplied independently by absolute path.
	env, err := Environment(ctx, []string{"PATH="})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `git --version && git -c 'alias.check-home=!printf "%s" "$HOME"' check-home`)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Git without system utilities: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "git version ") || !strings.HasSuffix(string(out), filepath.Join(root, "home")) {
		t.Fatalf("unexpected version or isolated HOME: %s", out)
	}
}

func TestRuntimeConcurrentStartupAndReuse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell integration")
	}
	install := filepath.Join(t.TempDir(), "app data with spaces", "runtime", "git")
	t.Setenv("TMPDIR", "/does-not-exist")
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, name := range []string{"Alice", "Bob", "Carol", "Dave"} {
		repo := t.TempDir()
		wg.Go(func() {
			<-start
			rt, err := Prepare(install)
			if err != nil {
				t.Error(err)
				return
			}
			ctx := WithRuntime(identityContext(name), rt)
			cmd := Command(ctx, "init")
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("app Git: %v: %s", err, out)
				return
			}
			env, err := Environment(ctx, append(os.Environ(), "OV_GIT_REAL_EXECUTABLE=/untrusted/git"))
			if err != nil {
				t.Error(err)
				return
			}
			cmd = exec.Command("/bin/sh", "-c", `git commit --allow-empty -m agent && git log -1 --format='%an <%ae>'`)
			cmd.Dir, cmd.Env = repo, env
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), name+" <"+strings.ToLower(name)+"@example.com>") {
				t.Errorf("agent Git: %v: %s", err, out)
			}
		})
	}
	close(start)
	wg.Wait()
	rt, err := Prepare(install)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(rt.path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Prepare(install)
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(again.path)
	if err != nil {
		t.Fatal(err)
	}
	if rt.path != again.path || !os.SameFile(old, now) {
		t.Fatal("unchanged wrapper was replaced")
	}
	entries, err := os.ReadDir(filepath.Join(install, "bin"))
	if err != nil || len(entries) != 3 {
		t.Fatalf("wrapper buildup: %v %v", entries, err)
	}
	entries, err = os.ReadDir(filepath.Join(install, "home"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("home not empty: %v %v", entries, err)
	}
}

func TestRuntimeRelativePathAndUpdate(t *testing.T) {
	parent := t.TempDir()
	t.Chdir(parent)
	rt, err := Prepare(filepath.Join("relative data", "runtime", "git"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(rt.path) {
		t.Fatal("wrapper must be absolute")
	}
	ctx := WithRuntime(identityContext("Alice"), rt)
	cmd := Command(ctx, "--version")
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Git in another directory: %v: %s", err, out)
	}
	if err := os.WriteFile(rt.path, []byte("old version"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(filepath.Join("relative data", "runtime", "git")); err != nil {
		t.Fatal(err)
	}
	// The existing instance's path still works after another instance updates it.
	if out, err := Command(ctx, "--version").CombinedOutput(); err != nil {
		t.Fatalf("updated wrapper: %v: %s", err, out)
	}
	if _, err := Prepare(filepath.Join(rt.path, "blocked")); err == nil {
		t.Fatal("expected initialization failure")
	}
	bare := WithResolver(context.Background(), func(context.Context) (Identity, error) { return Identity{}, nil })
	if Command(bare, "status").Err == nil {
		t.Fatal("missing runtime must fail closed")
	}
}

func TestWriteChangedNeverExposesPartialScript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git")
	a, b := []byte(strings.Repeat("a", 32768)), []byte(strings.Repeat("b", 32768))
	if err := writeChanged(path, a); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(done)
		for n := 0; n < 30; n++ {
			data := a
			if n%2 == 0 {
				data = b
			}
			if err := writeChanged(path, data); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for {
		select {
		case <-done:
			wg.Wait()
			return
		default:
		}
		data, err := os.ReadFile(path)
		if err != nil || (string(data) != string(a) && string(data) != string(b)) {
			t.Errorf("partial script: length=%d error=%v", len(data), err)
			wg.Wait()
			return
		}
		time.Sleep(time.Millisecond)
	}
}
