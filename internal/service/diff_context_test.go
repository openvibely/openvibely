package service

import (
	"context"
	"os"
	"path/filepath"
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
			_, ok := GetWorktreeDiffFileWithUncommittedContext(ctx, dir, "task", "main", dir, 0)
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
