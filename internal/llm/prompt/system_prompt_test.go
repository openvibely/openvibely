package prompt

import (
	"strings"
	"testing"
)

func TestBuildAgentSystemPrompt_IncludesWorktreeContextWhenProvided(t *testing.T) {
	worktreePath := "/tmp/.worktrees/task_123"
	got := BuildAgentSystemPrompt("", worktreePath)

	if !strings.Contains(got, "You are operating in an isolated git worktree at "+worktreePath+".") {
		t.Fatalf("expected worktree context in system prompt, got: %q", got)
	}
}

func TestBuildAgentSystemPrompt_OmitsWorktreeContextWhenNotProvided(t *testing.T) {
	got := BuildAgentSystemPrompt("")
	if strings.Contains(got, "You are operating in an isolated git worktree at ") {
		t.Fatalf("did not expect worktree context without path, got: %q", got)
	}
}

func TestBuildWorktreeContextSentence(t *testing.T) {
	worktreePath := "/tmp/.worktrees/task_abc"
	got := BuildWorktreeContextSentence("  " + worktreePath + " ")
	want := "You are operating in an isolated git worktree at " + worktreePath + "."
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}

	if empty := BuildWorktreeContextSentence("   "); empty != "" {
		t.Fatalf("expected empty context for blank workDir, got %q", empty)
	}

	nonWorktreePath := "/tmp/my-project-repo"
	if non := BuildWorktreeContextSentence(nonWorktreePath); non != "" {
		t.Fatalf("expected empty context for non-worktree path, got %q", non)
	}
}

func TestAppendWorktreeContextPrompt(t *testing.T) {
	base := "base system prompt"
	worktreePath := "/tmp/.worktrees/task_abc"
	got := AppendWorktreeContextPrompt(base, worktreePath)
	want := BuildWorktreeContextSentence(worktreePath)
	if !strings.Contains(got, want) {
		t.Fatalf("expected appended worktree context, got: %q", got)
	}

	unchanged := AppendWorktreeContextPrompt(base, "")
	if unchanged != base {
		t.Fatalf("expected prompt unchanged when workDir is empty, got: %q", unchanged)
	}
}
