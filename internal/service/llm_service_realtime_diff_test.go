package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

type recordingDiffRepo struct {
	updates []string
	err     error
}

func (r *recordingDiffRepo) UpdateDiffOutput(ctx context.Context, id string, diffOutput string) error {
	r.updates = append(r.updates, diffOutput)
	return r.err
}

func TestPublishDiffSnapshotIfChangedSkipsIdenticalPeriodicDiffs(t *testing.T) {
	ctx := context.Background()
	repo := &recordingDiffRepo{}
	broadcaster := events.NewFileChangeBroadcaster()
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)
	state := &DiffSnapshotState{}

	if !PublishDiffSnapshotIfChanged(ctx, repo, broadcaster, state, "task-1", "exec-1", "diff one", false) {
		t.Fatal("expected first periodic diff to publish")
	}
	assertDiffSnapshotEvent(t, sub, "task-1", "exec-1")
	if got := len(repo.updates); got != 1 {
		t.Fatalf("expected first diff to write once, got %d writes", got)
	}

	if PublishDiffSnapshotIfChanged(ctx, repo, broadcaster, state, "task-1", "exec-1", "diff one", false) {
		t.Fatal("expected identical periodic diff to be skipped")
	}
	assertNoDiffSnapshotEvent(t, sub)
	if got := len(repo.updates); got != 1 {
		t.Fatalf("expected identical diff to skip DB write, got %d writes", got)
	}
}

func TestPublishDiffSnapshotIfChangedWritesAndPublishesChangedDiffs(t *testing.T) {
	ctx := context.Background()
	repo := &recordingDiffRepo{}
	broadcaster := events.NewFileChangeBroadcaster()
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)
	state := &DiffSnapshotState{}

	if !PublishDiffSnapshotIfChanged(ctx, repo, broadcaster, state, "task-1", "exec-1", "diff one", false) {
		t.Fatal("expected first periodic diff to publish")
	}
	assertDiffSnapshotEvent(t, sub, "task-1", "exec-1")

	if !PublishDiffSnapshotIfChanged(ctx, repo, broadcaster, state, "task-1", "exec-1", "diff two", false) {
		t.Fatal("expected changed periodic diff to publish")
	}
	assertDiffSnapshotEvent(t, sub, "task-1", "exec-1")
	if got := len(repo.updates); got != 2 {
		t.Fatalf("expected changed diff to write again, got %d writes", got)
	}
	if got := repo.updates[1]; got != "diff two" {
		t.Fatalf("expected changed diff to be persisted, got %q", got)
	}
}

func TestPublishDiffSnapshotIfChangedFinalSnapshotStillWritesAndPublishes(t *testing.T) {
	ctx := context.Background()
	repo := &recordingDiffRepo{}
	broadcaster := events.NewFileChangeBroadcaster()
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)
	state := &DiffSnapshotState{}

	if !PublishDiffSnapshotIfChanged(ctx, repo, broadcaster, state, "task-1", "exec-1", "diff one", false) {
		t.Fatal("expected first periodic diff to publish")
	}
	assertDiffSnapshotEvent(t, sub, "task-1", "exec-1")

	if !PublishDiffSnapshotIfChanged(ctx, repo, broadcaster, state, "task-1", "exec-1", "diff one", true) {
		t.Fatal("expected final diff snapshot to publish even when unchanged")
	}
	assertDiffSnapshotEvent(t, sub, "task-1", "exec-1")
	if got := len(repo.updates); got != 2 {
		t.Fatalf("expected final diff to write even when unchanged, got %d writes", got)
	}
}

func TestBroadcastDiffSnapshotsNonWorktreeUsesHeadBase(t *testing.T) {
	ctx := context.Background()
	repoDir := createTestGitRepo(t)
	runGitTest(t, repoDir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repoDir, "committed-feature.txt"), []byte("unrelated committed change\n"), 0o644); err != nil {
		t.Fatalf("write committed feature file: %v", err)
	}
	runGitTest(t, repoDir, "add", "committed-feature.txt")
	runGitTest(t, repoDir, "commit", "-m", "feature commit")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Pending direct-checkout edit\n"), 0o644); err != nil {
		t.Fatalf("write pending file: %v", err)
	}

	storeEnv := teststores.New(t)
	llmConfigRepo := storeEnv.Stores.LLMConfigs
	execRepo := storeEnv.Stores.Executions
	taskRepo := storeEnv.Stores.Tasks
	projectRepo := storeEnv.Stores.Projects
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Non-worktree snapshot project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Direct checkout snapshot",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "update project-owned files",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	execution := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, execution); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, storeEnv.Stores.Schedules, storeEnv.Stores.Attachments)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.broadcastDiffSnapshots(ctx, task.ID, execution.ID, repoDir, repoDir, "task/stale-lineage", "main", false, stop)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			close(stop)
			<-done
		}
	})

	deadline := time.Now().Add(4 * time.Second)
	var diffOutput string
	for time.Now().Before(deadline) {
		stored, err := execRepo.GetByID(ctx, execution.ID)
		if err != nil {
			t.Fatalf("get execution: %v", err)
		}
		diffOutput = stored.DiffOutput
		if diffOutput != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	close(stop)
	<-done

	if diffOutput == "" {
		t.Fatal("timed out waiting for a non-worktree diff snapshot")
	}
	if !strings.Contains(diffOutput, "Pending direct-checkout edit") {
		t.Fatalf("expected HEAD-relative pending edit in snapshot, got:\n%s", diffOutput)
	}
	if strings.Contains(diffOutput, "committed-feature.txt") || strings.Contains(diffOutput, "unrelated committed change") {
		t.Fatalf("non-worktree snapshot must use git diff HEAD, not main-to-feature history; got:\n%s", diffOutput)
	}
}

func assertDiffSnapshotEvent(t *testing.T, sub events.FileChangeSubscriber, taskID, execID string) {
	t.Helper()
	select {
	case evt := <-sub:
		if evt.Type != events.DiffSnapshot {
			t.Fatalf("expected DiffSnapshot event, got %s", evt.Type)
		}
		if evt.TaskID != taskID || evt.ExecID != execID {
			t.Fatalf("expected task=%s exec=%s, got task=%s exec=%s", taskID, execID, evt.TaskID, evt.ExecID)
		}
	default:
		t.Fatal("expected diff_snapshot event")
	}
}

func assertNoDiffSnapshotEvent(t *testing.T, sub events.FileChangeSubscriber) {
	t.Helper()
	select {
	case evt := <-sub:
		t.Fatalf("expected no diff_snapshot event, got %#v", evt)
	default:
	}
}
