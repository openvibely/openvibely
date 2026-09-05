package repository

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestTaskRepoListSwarmChildrenUsesBoundedProjection(t *testing.T) {
	assertSwarmChildProjectionColumns(t)

	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()
	_, child := createSwarmProjectionFixture(t, ctx, db, repo, 32*1024, 4*1024, models.SwarmRoleWorker)

	counter.Reset()
	counter.SetEnabled(true)
	children, err := repo.ListSwarmChildren(ctx, *child.ParentTaskID)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListSwarmChildren: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 swarm child, got %d", len(children))
	}
	assertProjectionQueryOmitsSwarmChildPayloads(t, counter.Statements())
	assertSwarmChildMatchesFullTaskExceptPromptAndChainConfig(t, ctx, repo, children[0], child.ID)
}

func TestTaskRepoFindSwarmChildByRoleUsesBoundedProjection(t *testing.T) {
	assertSwarmChildProjectionColumns(t)

	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()
	_, child := createSwarmProjectionFixture(t, ctx, db, repo, 32*1024, 4*1024, models.SwarmRoleReviewer)

	counter.Reset()
	counter.SetEnabled(true)
	got, err := repo.FindSwarmChildByRole(ctx, *child.ParentTaskID, models.SwarmRoleReviewer)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("FindSwarmChildByRole: %v", err)
	}
	if got == nil {
		t.Fatal("expected reviewer child, got nil")
	}
	assertProjectionQueryOmitsSwarmChildPayloads(t, counter.Statements())
	assertSwarmChildMatchesFullTaskExceptPromptAndChainConfig(t, ctx, repo, *got, child.ID)
}

func BenchmarkTaskRepo_ListSwarmChildrenProjection(b *testing.B) {
	fixtures := []struct {
		name            string
		childCount      int
		promptSize      int
		chainConfigSize int
	}{
		{name: "Small10x512BPrompt256BChain", childCount: 10, promptSize: 512, chainConfigSize: 256},
		{name: "Large50x32KiBPrompt4KiBChain", childCount: 50, promptSize: 32 * 1024, chainConfigSize: 4 * 1024},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db, parentID := newSwarmChildrenBenchmarkDB(b, fixture.childCount, fixture.promptSize, fixture.chainConfigSize)
			defer db.Close()
			repo := NewTaskRepo(db, nil)

			b.Run("Repository", func(b *testing.B) {
				benchmarkListSwarmChildrenBoundedProjection(b, repo, parentID)
			})
		})
	}
}

func assertSwarmChildProjectionColumns(t *testing.T) {
	t.Helper()
	for _, forbidden := range []string{"prompt", "chain_config"} {
		if projectionContainsColumn(swarmChildTaskSelectColumns, forbidden) {
			t.Fatalf("swarm child projection must not select unused payload column %q: %s", forbidden, swarmChildTaskSelectColumns)
		}
	}
	for _, required := range []string{"id", "project_id", "title", "category", "priority", "status", "agent_id", "agent_definition_id", "tag", "display_order", "parent_task_id", "swarm_role", "swarm_status", "swarm_config", "swarm_sequence", "worktree_path", "worktree_branch", "auto_merge", "merge_target_branch", "merge_status", "base_branch", "base_commit_sha", "lineage_depth", "created_via", "telegram_chat_id", "created_at", "updated_at", "completed_at"} {
		if !projectionContainsColumn(swarmChildTaskSelectColumns, required) {
			t.Fatalf("swarm child projection missing required column %q: %s", required, swarmChildTaskSelectColumns)
		}
	}
}

func assertProjectionQueryOmitsSwarmChildPayloads(t *testing.T, statements []string) {
	t.Helper()
	if len(statements) != 1 {
		t.Fatalf("expected exactly one recorded query, got %d: %#v", len(statements), statements)
	}
	projection := strings.ToLower(strings.Split(statements[0], " from tasks")[0])
	for _, forbidden := range []string{"prompt", "chain_config"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("swarm child query selected %q: %s", forbidden, statements[0])
		}
	}
}

func assertSwarmChildMatchesFullTaskExceptPromptAndChainConfig(t *testing.T, ctx context.Context, repo *TaskRepo, got models.Task, childID string) {
	t.Helper()
	if got.Prompt != "" || got.ChainConfig != "" {
		t.Fatalf("bounded swarm child loaded prompt/chain_config: prompt=%d chain=%d", len(got.Prompt), len(got.ChainConfig))
	}
	full, err := repo.GetByID(ctx, childID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if full == nil {
		t.Fatalf("full child %s not found", childID)
	}
	if full.Prompt == "" || full.ChainConfig == "" {
		t.Fatalf("fixture must retain full child prompt/chain_config through GetByID: prompt=%d chain=%d", len(full.Prompt), len(full.ChainConfig))
	}
	want := *full
	want.Prompt = ""
	want.ChainConfig = ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded swarm child changed fields other than prompt/chain_config:\n got: %#v\nwant: %#v", got, want)
	}
}

func createSwarmProjectionFixture(t *testing.T, ctx context.Context, db *sql.DB, repo *TaskRepo, promptSize, chainConfigSize int, role models.SwarmRole) (*models.Task, *models.Task) {
	t.Helper()
	agentID := defaultAgentConfigID(t, ctx, db)
	agentDefinitionID := "swarm-projection-agent"
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (id, name, description, system_prompt) VALUES (?, 'Swarm Projection Agent', 'fixture', 'fixture')`, agentDefinitionID); err != nil {
		t.Fatalf("insert agent definition: %v", err)
	}

	parent := &models.Task{
		ProjectID:   "default",
		Title:       fmt.Sprintf("Swarm Projection Parent %s", role),
		Category:    models.CategoryActive,
		Priority:    4,
		Status:      models.StatusBlocked,
		Prompt:      "parent prompt",
		SwarmRole:   models.SwarmRoleParent,
		SwarmStatus: "running",
		SwarmConfig: `{"generation":1,"reviewer_enabled":true,"merger_enabled":true}`,
	}
	if err := repo.Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	child := &models.Task{
		ProjectID:         "default",
		Title:             fmt.Sprintf("Swarm Projection Child %s", role),
		Category:          models.CategoryActive,
		Priority:          3,
		Status:            models.StatusCompleted,
		Prompt:            strings.Repeat("p", promptSize),
		AgentID:           &agentID,
		AgentDefinitionID: &agentDefinitionID,
		Tag:               models.TagFeature,
		ParentTaskID:      &parent.ID,
		ChainConfig:       `{"enabled":true,"child_prompt_prefix":"` + strings.Repeat("c", chainConfigSize) + `"}`,
		SwarmRole:         role,
		SwarmStatus:       "completed",
		SwarmConfig:       `{"isolation":"worktree","rerun_generation":2,"completed_generation":2,"required":true}`,
		SwarmSequence:     42,
		WorktreePath:      "/tmp/openvibely-swarm-projection",
		WorktreeBranch:    "task/swarm-projection",
		AutoMerge:         true,
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusPending,
		BaseBranch:        "main",
		BaseCommitSHA:     strings.Repeat("a", 40),
		LineageDepth:      1,
		CreatedVia:        models.TaskOriginWeb,
		TelegramChatID:    12345,
	}
	if err := repo.Create(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}
	return parent, child
}

func defaultAgentConfigID(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx, `SELECT id FROM agent_configs WHERE is_default = 1 LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("load default agent config: %v", err)
	}
	return id
}

func benchmarkListSwarmChildrenBoundedProjection(b *testing.B, repo *TaskRepo, parentID string) {
	b.Helper()
	ctx := context.Background()
	var unboundedPayloadBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		children, err := repo.ListSwarmChildren(ctx, parentID)
		if err != nil {
			b.Fatalf("list swarm children: %v", err)
		}
		unboundedPayloadBytes = swarmChildrenUnboundedPayloadBytes(children)
	}
	b.StopTimer()
	b.ReportMetric(float64(unboundedPayloadBytes), "unbounded_payload_bytes/op")
}

func swarmChildrenUnboundedPayloadBytes(children []models.Task) int64 {
	var total int64
	for _, child := range children {
		total += int64(len(child.Prompt) + len(child.ChainConfig))
	}
	return total
}

func newSwarmChildrenBenchmarkDB(b *testing.B, childCount, promptSize, chainConfigSize int) (*sql.DB, string) {
	b.Helper()
	db := testutil.NewTestDB(b)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()
	parent := &models.Task{
		ProjectID:   "default",
		Title:       fmt.Sprintf("Benchmark swarm parent %d", childCount),
		Category:    models.CategoryActive,
		Priority:    4,
		Status:      models.StatusBlocked,
		Prompt:      strings.Repeat("parent", 128),
		SwarmRole:   models.SwarmRoleParent,
		SwarmStatus: "running",
		SwarmConfig: `{"generation":1,"reviewer_enabled":true,"merger_enabled":true}`,
	}
	if err := repo.Create(ctx, parent); err != nil {
		db.Close()
		b.Fatalf("create benchmark parent: %v", err)
	}

	prompt := strings.Repeat("p", promptSize)
	chainConfig := `{"enabled":true,"child_prompt_prefix":"` + strings.Repeat("c", chainConfigSize) + `"}`
	roles := []models.SwarmRole{models.SwarmRolePlanner, models.SwarmRoleWorker, models.SwarmRoleReviewer, models.SwarmRoleMerger}
	for i := 0; i < childCount; i++ {
		role := roles[i%len(roles)]
		if i == 0 {
			role = models.SwarmRolePlanner
		} else if i == childCount-1 {
			role = models.SwarmRoleMerger
		}
		child := &models.Task{
			ProjectID:         "default",
			Title:             fmt.Sprintf("Benchmark swarm child %03d", i),
			Category:          models.CategoryActive,
			Priority:          (i % 4) + 1,
			Status:            models.StatusPending,
			Prompt:            prompt,
			Tag:               models.TagFeature,
			ParentTaskID:      &parent.ID,
			ChainConfig:       chainConfig,
			SwarmRole:         role,
			SwarmStatus:       "pending",
			SwarmConfig:       fmt.Sprintf(`{"isolation":"worktree","rerun_generation":1,"required":%t}`, i%5 != 0),
			SwarmSequence:     10 + i,
			WorktreePath:      fmt.Sprintf("/tmp/openvibely-benchmark-swarm-%03d", i),
			WorktreeBranch:    fmt.Sprintf("task/benchmark-swarm-%03d", i),
			MergeTargetBranch: "main",
			MergeStatus:       models.MergeStatusPending,
			BaseBranch:        "main",
			BaseCommitSHA:     strings.Repeat("b", 40),
			LineageDepth:      1,
			CreatedVia:        models.TaskOriginSystemAgent,
		}
		if err := repo.Create(ctx, child); err != nil {
			db.Close()
			b.Fatalf("create benchmark child %d: %v", i, err)
		}
	}
	return db, parent.ID
}
