package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestPublishForkHistoryWithoutRemoteBaseBranch(t *testing.T) {
	ctx := context.Background()
	env := teststores.New(t)
	if err := env.Stores.Settings.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatal(err)
	}
	svc := NewGitHubService(env.Stores.Settings, "", "", "", "")
	local := createTestGitRepo(t)
	fork := filepath.Join(t.TempDir(), "fork.git")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := defaultRunGit(ctx, dir, nil, args...)
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(local, "init", "--bare", fork)
	git(local, "push", fork, "HEAD:refs/heads/main")
	git(local, "checkout", "-b", "develop")
	if err := os.WriteFile(filepath.Join(local, "base-only.txt"), []byte("local base change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(local, "add", "-A")
	git(local, "commit", "-m", "local base commit")
	localBase := git(local, "rev-parse", "HEAD")
	git(local, "checkout", "-b", "task/contribution")
	if err := os.WriteFile(filepath.Join(local, "task.txt"), []byte("uncommitted task change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pushes := 0
	svc.runGit = func(ctx context.Context, dir string, extra []string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "push" {
			pushes++
			if len(args) != 4 || args[1] != "--no-follow-tags" || args[2] != "https://github.com/contributor/fork.git" || !strings.HasSuffix(args[3], ":refs/heads/task/contribution") {
				t.Fatalf("unexpected push: %v", args)
			}
			if strings.Contains(strings.Join(args, " "), "ghp_test") {
				t.Fatal("credential exposed in command arguments")
			}
			if !strings.Contains(strings.Join(extra, "\n"), "AUTHORIZATION: Basic") {
				t.Fatal("missing configured GitHub credential")
			}
			return defaultRunGit(ctx, dir, nil, "push", "--no-follow-tags", fork, args[3])
		}
		return defaultRunGit(ctx, dir, extra, args...)
	}
	repo, _ := ParseGitHubRepoURL("https://github.com/contributor/fork")
	req := GitHubPublishBranchRequest{PreserveLocalHistory: true, WorktreePath: local, Branch: "task/contribution", LocalBaseBranch: "develop", BaseBranch: "release-only-on-upstream", CommitMessage: "prepare contribution"}
	result, err := svc.PublishBranch(ctx, &repo, req)
	if err != nil {
		t.Fatal(err)
	}
	head := git(local, "rev-parse", "HEAD")
	if result.HeadSHA != head || !result.CreatedCommit || result.ParentSHA != localBase {
		t.Fatalf("wrong publication result: %+v", result)
	}
	if git(fork, "rev-parse", "refs/heads/task/contribution") != head {
		t.Fatal("published history differs from local history")
	}
	git(fork, "merge-base", "--is-ancestor", localBase, head)
	if git(fork, "show", head+":base-only.txt") != "local base change" {
		t.Fatal("lost local base contents")
	}
	if git(local, "status", "--porcelain") != "" {
		t.Fatal("task changes were not committed")
	}
	if _, err := defaultRunGit(ctx, fork, nil, "show-ref", "--verify", "refs/heads/develop"); err == nil {
		t.Fatal("publication created an unrelated base branch")
	}
	again, err := svc.PublishBranch(ctx, &repo, req)
	if err != nil || again.HeadSHA != head || again.CreatedCommit {
		t.Fatalf("repeat publication: %+v %v", again, err)
	}
	// Another writer advances the fork branch. The local task must not replace it.
	clone := filepath.Join(t.TempDir(), "other")
	git(local, "clone", "--branch", "task/contribution", fork, clone)
	git(clone, "-c", "user.name=Other", "-c", "user.email=other@example.com", "commit", "--allow-empty", "-m", "other writer")
	remoteHead := git(clone, "rev-parse", "HEAD")
	git(clone, "push", "origin", "HEAD:refs/heads/task/contribution")
	if _, err := svc.PublishBranch(ctx, &repo, req); err == nil {
		t.Fatal("accepted non-fast-forward publication")
	}
	if git(fork, "rev-parse", "refs/heads/task/contribution") != remoteHead {
		t.Fatal("overwrote concurrent remote commit")
	}
	if pushes != 3 {
		t.Fatalf("push count = %d", pushes)
	}
	req.Branch = "task/wrong"
	if _, err := svc.PublishBranch(ctx, &repo, req); err == nil || pushes != 3 {
		t.Fatal("published from the wrong checked-out branch")
	}
}
