package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiffReadLockCancellation(t *testing.T) {
	var lock sync.RWMutex
	lock.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- diffReadLock(ctx, &lock) }()
	select {
	case acquired := <-done:
		if acquired {
			t.Fatal("acquired held lock")
		}
	case <-time.After(time.Second):
		t.Fatal("lock wait ignored cancellation")
	}
	lock.Unlock()
	if diffReadLock(ctx, &lock) {
		lock.RUnlock()
		t.Fatal("cancelled caller acquired lock")
	}
	if !diffReadLock(context.Background(), &lock) {
		t.Fatal("subsequent reader blocked")
	}
	lock.RUnlock()
}

func TestDiffReadLockNotStarvedByQueuedWriter(t *testing.T) {
	var lock sync.RWMutex
	lock.Lock()
	done := make(chan bool, 1)
	go func() { done <- diffReadLock(context.Background(), &lock) }()
	time.Sleep(20 * time.Millisecond)
	nextWriter := make(chan struct{})
	go func() {
		lock.Lock()
		close(nextWriter)
		lock.Unlock()
	}()
	time.Sleep(20 * time.Millisecond)
	lock.Unlock()
	select {
	case acquired := <-done:
		if !acquired {
			t.Fatal("reader gave up")
		}
	case <-nextWriter:
		t.Fatal("later writer acquired the lock before the queued reader")
	case <-time.After(time.Second):
		t.Fatal("queued reader starved by later writer")
	}
	lock.RUnlock()
	<-nextWriter
}

func TestDiffFileGitCancellation(t *testing.T) {
	for _, stage := range []string{"canonical", "diff"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
case "$*" in
 "rev-parse --git-common-dir")
  if [ "$DIFF_TEST_STAGE" = canonical ]; then exec sleep 30; fi
  echo .git ;;
 "rev-parse --is-inside-work-tree") echo true ;;
 "rev-parse --verify --quiet main^{commit}") exit 0 ;;
 "merge-base main HEAD") echo base ;;
 "diff --name-status base") printf 'M\tfile.txt\n' ;;
 "ls-files --others --exclude-standard") exit 0 ;;
 "diff base -- file.txt") exec sleep 30 ;;
 *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DIFF_TEST_STAGE", stage)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, ok := GetWorktreeDiffFileWithUncommitted(ctx, dir, "task", "main", dir, 0)
			if ok || ctx.Err() != context.DeadlineExceeded {
				t.Fatalf("expected timeout, ok=%v err=%v", ok, ctx.Err())
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("Git did not stop promptly")
			}
			// A timed-out Git operation must release its lifecycle read lock.
			key := filepath.Join(dir, ".git")
			if value, exists := worktreeLifecycleLocks.Load(key); exists {
				lock := value.(*sync.RWMutex)
				if !lock.TryLock() {
					t.Fatal("lifecycle read lock leaked")
				}
				lock.Unlock()
			}
		})
	}
}

// Changing only stat metadata makes ordinary status refresh the index. Read
// helpers must report changes without writing it, even if the parent enables
// optional locks. Avoid racing cancellation against Git's short lock window.
func TestGitStatusReadsDoNotRefreshIndex(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	for name, status := range map[string]func(context.Context, string) (string, error){
		"worktree": GitStatusPorcelain,
		"github": func(ctx context.Context, dir string) (string, error) {
			out, err := defaultRunGit(ctx, dir, nil, "status", "--porcelain")
			return string(out), err
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			git("init", "-q")
			path := filepath.Join(dir, "tracked.txt")
			if err := os.WriteFile(path, []byte("original\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "tracked.txt")
			git("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			index := filepath.Join(dir, ".git", "index")
			before, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			out, err := status(context.Background(), dir)
			if err != nil || out != "" {
				t.Fatalf("clean status: %q, %v", out, err)
			}
			after, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("read-only status rewrote the index")
			}
			// Prove the fixture exercises a refresh when optional writes are enabled.
			git("status", "--porcelain")
			refreshed, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, refreshed) {
				t.Fatal("control status did not refresh the index")
			}
			if err := os.WriteFile(path, []byte("modified content\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out, err = status(context.Background(), dir)
			if err != nil || !strings.Contains(out, " M tracked.txt") {
				t.Fatalf("dirty status: %q, %v", out, err)
			}
		})
	}
}
