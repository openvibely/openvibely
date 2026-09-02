package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func decodeListTasksResult(t *testing.T, raw string) taskDiscoveryResult {
	t.Helper()
	var result taskDiscoveryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal list_tasks result: %v (raw=%s)", err, raw)
	}
	return result
}

func TestExecuteListTasksTool(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	other := &models.Project{Name: "List Tasks Other Project"}
	if err := projectRepo.Create(ctx, other); err != nil {
		t.Fatalf("create other project: %v", err)
	}

	mk := func(projectID, title string, category models.TaskCategory, status models.TaskStatus) *models.Task {
		task := &models.Task{ProjectID: projectID, Title: title, Category: category, Status: status, Prompt: "p"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		if err := taskRepo.UpdateStatus(ctx, task.ID, status); err != nil {
			t.Fatalf("status %q: %v", title, err)
		}
		return task
	}

	target := mk("default", "Implement issue 25", models.CategoryActive, models.StatusPending)
	mk("default", "Implement issue 25 followup", models.CategoryBacklog, models.StatusPending)
	mk("default", "Unrelated task", models.CategoryActive, models.StatusRunning)
	mk("default", "Implement issue 25 chat", models.CategoryChat, models.StatusCompleted)
	mk(other.ID, "Implement issue 25 elsewhere", models.CategoryActive, models.StatusPending)

	// Missing project context fails loudly.
	if _, err := ExecuteListTasksTool(ctx, taskRepo, "", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for empty project context")
	}

	// Partial title query returns compact identity sufficient for exact-target actions.
	out, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"query":"issue 25"}`))
	if err != nil {
		t.Fatalf("list_tasks query: %v", err)
	}
	result := decodeListTasksResult(t, out)
	if !result.OK {
		t.Fatal("expected ok=true")
	}
	if result.Total != 2 || result.Count != 2 {
		t.Fatalf("expected 2 matches (chat + other project excluded), got total=%d count=%d", result.Total, result.Count)
	}
	if result.Filter.Query != "issue 25" || result.Filter.Category != "" || result.Filter.Status != "" || result.Note != "" {
		t.Fatalf("unexpected filter echo/note for matching query: %+v", result)
	}
	var found bool
	for _, task := range result.Tasks {
		if task.Category == string(models.CategoryChat) {
			t.Fatalf("chat row leaked: %q", task.Title)
		}
		if task.TaskID == target.ID {
			found = true
			if task.Title != target.Title || task.Category != string(models.CategoryActive) || task.Status != string(models.StatusPending) {
				t.Fatalf("compact identity mismatch: %+v", task)
			}
			if task.UpdatedAt == "" {
				t.Fatal("expected updated_at in compact summary")
			}
		}
	}
	if !found {
		t.Fatalf("expected target task %q in results", target.ID)
	}

	// Category + status filters.
	out, err = ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"category":"active","status":"running"}`))
	if err != nil {
		t.Fatalf("list_tasks filter: %v", err)
	}
	filtered := decodeListTasksResult(t, out)
	if filtered.Total != 1 || len(filtered.Tasks) != 1 || filtered.Tasks[0].Title != "Unrelated task" {
		t.Fatalf("expected single running active task, got %+v", filtered)
	}
	if filtered.Filter.Category != "active" || filtered.Filter.Status != "running" {
		t.Fatalf("expected normalized category/status filter echo, got %+v", filtered.Filter)
	}

	// Exhausted empty pages explicitly identify the exact filter that was exhausted.
	out, err = ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"query":"missing issue","limit":10}`))
	if err != nil {
		t.Fatalf("list_tasks empty query: %v", err)
	}
	empty := decodeListTasksResult(t, out)
	if empty.Total != 0 || empty.Count != 0 || empty.HasMore || empty.Filter.Query != "missing issue" || !strings.Contains(empty.Note, "No tasks matched this exact list_tasks query/filter") || !strings.Contains(empty.Note, "filter object echoes the parameters you sent") || !strings.Contains(empty.Note, "do not immediately repeat the same query just to get an unfiltered echo") {
		t.Fatalf("unexpected exhausted empty response: %+v", empty)
	}

	// Invalid category/status are rejected.
	if _, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"category":"bogus"}`)); err == nil {
		t.Fatal("expected error for invalid category")
	}
	if _, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"status":"bogus"}`)); err == nil {
		t.Fatal("expected error for invalid status")
	}

	// Pagination contract: limit + offset + has_more.
	out, err = ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"limit":1,"offset":0}`))
	if err != nil {
		t.Fatalf("list_tasks page1: %v", err)
	}
	page1 := decodeListTasksResult(t, out)
	if page1.Total != 3 || page1.Count != 1 || page1.Limit != 1 || !page1.HasMore {
		t.Fatalf("unexpected page1 contract: %+v", page1)
	}

	out, err = ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"limit":1,"offset":2}`))
	if err != nil {
		t.Fatalf("list_tasks page3: %v", err)
	}
	page3 := decodeListTasksResult(t, out)
	if page3.Offset != 2 || page3.Count != 1 || page3.HasMore {
		t.Fatalf("unexpected final page contract: %+v", page3)
	}
}

func TestExecuteListTasksToolUsesCountAndBoundedPageOperations(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		task := &models.Task{
			ProjectID: "default",
			Title:     fmt.Sprintf("Count and page task %d", i),
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    "not selected",
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}

	counter.Reset()
	counter.SetEnabled(true)
	out, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"limit":50}`))
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ExecuteListTasksTool: %v", err)
	}
	statements := counter.Statements()
	if len(statements) != 2 {
		t.Fatalf("list_tasks used %d SQL operations, want count plus page: %#v", len(statements), statements)
	}
	if !strings.Contains(strings.ToUpper(statements[0]), "SELECT COUNT(*)") {
		t.Fatalf("first list_tasks operation = %q, want total count", statements[0])
	}
	if !strings.Contains(strings.ToUpper(statements[1]), "SELECT ID") || !strings.Contains(strings.ToUpper(statements[1]), "LIMIT") || !strings.Contains(strings.ToUpper(statements[1]), "OFFSET") {
		t.Fatalf("second list_tasks operation = %q, want bounded compact page", statements[1])
	}
	result := decodeListTasksResult(t, out)
	if result.Total != 3 || result.Count != 3 || result.Limit != 50 || result.Offset != 0 || result.HasMore {
		t.Fatalf("unexpected count/page result: %+v", result)
	}
}

func TestExecuteListTasksTool_PreservesCompactJSONContract(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	parent := &models.Task{
		ProjectID: "default",
		Title:     "Discovery JSON parent",
		Category:  models.CategoryChat,
		Priority:  1,
		Status:    models.StatusCompleted,
		Prompt:    "internal parent",
	}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	withOptionalFields := &models.Task{
		ProjectID:    "default",
		Title:        "Discovery JSON optional",
		Category:     models.CategoryActive,
		Priority:     4,
		Status:       models.StatusRunning,
		Prompt:       "not returned",
		ParentTaskID: &parent.ID,
		SwarmRole:    models.SwarmRoleWorker,
	}
	withoutOptionalFields := &models.Task{
		ProjectID: "default",
		Title:     "Discovery JSON nullable",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "not returned",
	}
	for _, task := range []*models.Task{withOptionalFields, withoutOptionalFields} {
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create %q: %v", task.Title, err)
		}
	}

	fixedTimestamp := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE id IN (?, ?)`, fixedTimestamp, withOptionalFields.ID, withoutOptionalFields.ID); err != nil {
		t.Fatalf("set fixture timestamps: %v", err)
	}

	for _, tc := range []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:  "optional fields present",
			query: `{"query":"Discovery JSON optional"}`,
			expected: fmt.Sprintf(`{"ok":true,"tasks":[{"task_id":"%s","title":"Discovery JSON optional","category":"active","status":"running","priority":4,"updated_at":"2024-01-02T03:04:05Z","parent_task_id":"%s","swarm_role":"worker"}],"count":1,"total":1,"limit":20,"offset":0,"has_more":false,"filter":{"query":"Discovery JSON optional","category":"","status":""}}`,
				withOptionalFields.ID, parent.ID)},
		{
			name:  "nullable optional fields omitted",
			query: `{"query":"Discovery JSON nullable"}`,
			expected: fmt.Sprintf(`{"ok":true,"tasks":[{"task_id":"%s","title":"Discovery JSON nullable","category":"backlog","status":"pending","priority":2,"updated_at":"2024-01-02T03:04:05Z"}],"count":1,"total":1,"limit":20,"offset":0,"has_more":false,"filter":{"query":"Discovery JSON nullable","category":"","status":""}}`,
				withoutOptionalFields.ID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(tc.query))
			if err != nil {
				t.Fatalf("ExecuteListTasksTool: %v", err)
			}
			if out != tc.expected {
				t.Fatalf("compact JSON changed:\nwant %s\n got %s", tc.expected, out)
			}
		})
	}
}

func TestDiscoveryToolsUseSharedInputDecoder(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	tools := map[string]func(json.RawMessage) (string, error){
		"list_tasks": func(input json.RawMessage) (string, error) {
			return ExecuteListTasksTool(ctx, taskRepo, "default", input)
		},
		"list_schedules": func(input json.RawMessage) (string, error) {
			return ExecuteListSchedulesTool(ctx, scheduleRepo, "default", input)
		},
	}

	for name, tool := range tools {
		t.Run(name, func(t *testing.T) {
			for _, input := range []json.RawMessage{nil, json.RawMessage(" \n\t ")} {
				out, err := tool(input)
				if err != nil {
					t.Fatalf("blank input: %v", err)
				}
				if !strings.Contains(out, `"ok":true`) {
					t.Fatalf("expected successful empty result, got %q", out)
				}
			}

			_, err := tool(json.RawMessage(`{"broken":`))
			if err == nil || !strings.Contains(err.Error(), name+`: invalid tool input JSON: unexpected end of JSON input`) {
				t.Fatalf("expected %s shared decoder error, got %v", name, err)
			}
		})
	}
}

func TestExecuteViewSwarmToolParentTitleReturnsOrderedCompactHierarchy(t *testing.T) {
	ctx := context.Background()
	taskRepo, project := setupViewSwarmServiceFixture(t)
	parent := createViewSwarmTask(t, taskRepo, project.ID, "Release swarm", func(task *models.Task) {
		task.SwarmRole = models.SwarmRoleParent
		task.SwarmStatus = "planning"
		task.Prompt = "parent prompt must not appear"
		task.SwarmConfig = `{"planner_notes":"large config must not appear"}`
	})
	createViewSwarmTask(t, taskRepo, project.ID, "worker later", func(task *models.Task) {
		task.ParentTaskID = &parent.ID
		task.SwarmRole = models.SwarmRoleWorker
		task.SwarmStatus = "blocked"
		task.SwarmSequence = 20
		task.Status = models.StatusBlocked
		task.Prompt = "worker prompt must not appear"
		task.WorktreeBranch = "task/worker-later"
	})
	createViewSwarmTask(t, taskRepo, project.ID, "planner", func(task *models.Task) {
		task.ParentTaskID = &parent.ID
		task.SwarmRole = models.SwarmRolePlanner
		task.SwarmStatus = "running"
		task.SwarmSequence = 0
		task.Status = models.StatusRunning
	})
	createViewSwarmTask(t, taskRepo, project.ID, "worker first", func(task *models.Task) {
		task.ParentTaskID = &parent.ID
		task.SwarmRole = models.SwarmRoleWorker
		task.SwarmStatus = "done"
		task.SwarmSequence = 10
		task.Status = models.StatusCompleted
	})
	createViewSwarmTask(t, taskRepo, project.ID, "reviewer", func(task *models.Task) {
		task.ParentTaskID = &parent.ID
		task.SwarmRole = models.SwarmRoleReviewer
		task.SwarmStatus = "pending"
		task.SwarmSequence = 30
	})
	createViewSwarmTask(t, taskRepo, project.ID, "merger", func(task *models.Task) {
		task.ParentTaskID = &parent.ID
		task.SwarmRole = models.SwarmRoleMerger
		task.SwarmStatus = "waiting"
		task.SwarmSequence = 40
		task.MergeStatus = models.MergeStatusPending
	})

	out, err := ExecuteViewSwarmTool(ctx, taskRepo, project.ID, json.RawMessage(`{"title":"Release swarm"}`))
	require.NoError(t, err)

	var got viewSwarmResult
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.True(t, got.IsSwarm)
	require.Equal(t, "parent", got.ResolvedFrom)
	require.Equal(t, parent.ID, got.ParentTaskID)
	require.Equal(t, parent.ID, got.Parent.TaskID)
	require.Equal(t, 5, got.ChildCount)
	require.Equal(t, []string{"planner", "worker first", "worker later", "reviewer", "merger"}, viewSwarmChildTitles(got.Children))
	require.Equal(t, "blocked", got.Children[2].Status)
	require.True(t, got.Children[2].HasDiff)
	require.True(t, got.Children[4].HasDiff)

	for _, forbidden := range []string{"parent prompt must not appear", "worker prompt must not appear", "large config must not appear", "prompt", "swarm_config", "chain_config", "worktree_path", "diff_output"} {
		require.NotContains(t, out, forbidden)
	}
}

func TestExecuteViewSwarmToolChildLookupResolvesParentHierarchy(t *testing.T) {
	ctx := context.Background()
	taskRepo, project := setupViewSwarmServiceFixture(t)
	parent := createViewSwarmTask(t, taskRepo, project.ID, "Parent", func(task *models.Task) {
		task.SwarmRole = models.SwarmRoleParent
	})
	child := createViewSwarmTask(t, taskRepo, project.ID, "Worker", func(task *models.Task) {
		task.ParentTaskID = &parent.ID
		task.SwarmRole = models.SwarmRoleWorker
		task.SwarmSequence = 1
	})

	out, err := ExecuteViewSwarmTool(ctx, taskRepo, project.ID, json.RawMessage(`{"task_id":"`+child.ID+`"}`))
	require.NoError(t, err)

	var got viewSwarmResult
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.IsSwarm)
	require.Equal(t, child.ID, got.RequestedTaskID)
	require.Equal(t, "child", got.ResolvedFrom)
	require.Equal(t, parent.ID, got.ParentTaskID)
	require.Len(t, got.Children, 1)
	require.Equal(t, child.ID, got.Children[0].TaskID)
}

func TestExecuteViewSwarmToolNonSwarmReturnsControlledResponse(t *testing.T) {
	ctx := context.Background()
	taskRepo, project := setupViewSwarmServiceFixture(t)
	task := createViewSwarmTask(t, taskRepo, project.ID, "Ordinary", nil)

	out, err := ExecuteViewSwarmTool(ctx, taskRepo, project.ID, json.RawMessage(`{"task_id":"`+task.ID+`"}`))
	require.NoError(t, err)

	var got viewSwarmResult
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.True(t, got.OK)
	require.False(t, got.IsSwarm)
	require.Equal(t, "non_swarm", got.ResolvedFrom)
	require.Contains(t, strings.ToLower(got.Message), "not a swarm")
	require.Empty(t, got.Children)
}

func TestExecuteViewSwarmToolDoesNotCrossProjects(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	current := &models.Project{Name: "Current"}
	foreign := &models.Project{Name: "Foreign"}
	require.NoError(t, projectRepo.Create(ctx, current))
	require.NoError(t, projectRepo.Create(ctx, foreign))
	foreignParent := createViewSwarmTask(t, taskRepo, foreign.ID, "Same swarm title", func(task *models.Task) {
		task.SwarmRole = models.SwarmRoleParent
	})

	_, err := ExecuteViewSwarmTool(ctx, taskRepo, current.ID, json.RawMessage(`{"task_id":"`+foreignParent.ID+`"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "current project")

	_, err = ExecuteViewSwarmTool(ctx, taskRepo, current.ID, json.RawMessage(`{"title":"Same swarm title"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "current project")
}

func setupViewSwarmServiceFixture(t *testing.T) (*repository.TaskRepo, *models.Project) {
	t.Helper()
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Swarm Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	return repository.NewTaskRepo(db, nil), project
}

func createViewSwarmTask(t *testing.T, taskRepo *repository.TaskRepo, projectID, title string, mutate func(*models.Task)) *models.Task {
	t.Helper()
	task := &models.Task{
		ProjectID: projectID,
		Title:     title,
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if mutate != nil {
		mutate(task)
	}
	require.NoError(t, taskRepo.Create(context.Background(), task))
	return task
}

func viewSwarmChildTitles(children []swarmTaskSummary) []string {
	titles := make([]string, 0, len(children))
	for _, child := range children {
		titles = append(titles, child.Title)
	}
	return titles
}
