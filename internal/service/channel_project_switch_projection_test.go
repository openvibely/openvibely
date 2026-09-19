package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func seedChannelSwitchProject(t testing.TB, db *sql.DB, id, name string, isDefault bool, description, repoPath, repoURL string) {
	t.Helper()
	defaultValue := 0
	if isDefault {
		defaultValue = 1
	}
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, description, repoPath, repoURL, defaultValue)
	require.NoError(t, err)
}

func TestChannelSwitchProjectUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	description := strings.Repeat("D", 16*1024)
	repoPath := strings.Repeat("p", 2*1024)
	repoURL := strings.Repeat("u", 2*1024)
	seedChannelSwitchProject(t, db, "switch-alpha", "Alpha", true, description, repoPath, repoURL)
	seedChannelSwitchProject(t, db, "switch-beta", "Beta", false, description, repoPath, repoURL)

	var switched *models.Project
	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID:   "switch-alpha",
		ProjectRepo: projectRepo,
		SwitchProject: func(_ context.Context, project *models.Project) error {
			copy := *project
			switched = &copy
			return nil
		},
	})

	counter.Reset()
	counter.SetEnabled(true)
	result, err := handlers["switch_project"](ctx, json.RawMessage(`{"project":"beta"}`))
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.Equal(t, "Switched to project: Beta. Future messages from this channel identity will use that project.", result)
	require.NotNil(t, switched)
	require.Equal(t, "switch-beta", switched.ID)
	require.Equal(t, "Beta", switched.Name)
	require.Empty(t, switched.Description)
	require.Empty(t, switched.RepoPath)
	require.Empty(t, switched.RepoURL)
	require.Empty(t, switched.DefaultAgentConfigID)
	require.Nil(t, switched.MaxWorkers)
	require.True(t, switched.CreatedAt.IsZero())
	require.True(t, switched.UpdatedAt.IsZero())

	statements := counter.Statements()
	require.Len(t, statements, 1)
	normalized := normalizeChannelSwitchProjectSQL(statements[0])
	require.Equal(t, "select id, name, is_default from projects order by is_default desc, name asc, id asc", normalized)
	for _, forbidden := range []string{"description", "repo_path", "repo_url", "default_agent_config_id", "max_workers", "created_at", "updated_at"} {
		require.NotContains(t, normalized, forbidden)
	}
	require.Less(t, counter.SelectedTextBytes(), 128)

	counter.Reset()
	counter.SetEnabled(true)
	listResult, err := handlers["list_projects"](ctx, nil)
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.Contains(t, listResult, "Beta - "+description)
	require.Len(t, counter.Statements(), 1)
	require.Contains(t, normalizeChannelSwitchProjectSQL(counter.Statements()[0]), "description")
}

func TestChannelSwitchProjectCompactProjectionPreservesResponses(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `DELETE FROM projects`)
	require.NoError(t, err)

	seedChannelSwitchProject(t, db, "switch-default", "Default", true, "Default description", "/repo/default", "https://example.test/default")
	seedChannelSwitchProject(t, db, "switch-alpha", "Alpha", false, "Alpha description", "/repo/alpha", "https://example.test/alpha")
	seedChannelSwitchProject(t, db, "switch-beta", "Beta", false, "Beta description", "/repo/beta", "https://example.test/beta")

	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{ProjectID: "switch-default", ProjectRepo: projectRepo})
	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "first name", input: `{"project":"Default"}`},
		{name: "last name", input: `{"project":"Beta"}`},
		{name: "id", input: `{"project":"switch-alpha"}`},
		{name: "case insensitive", input: `{"project":"alpha"}`},
		{name: "miss", input: `{"project":"Missing"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compact, err := handlers["switch_project"](ctx, json.RawMessage(tc.input))
			require.NoError(t, err)
			full, err := switchChannelProjectResultForTest(ctx, projectRepo.List, strings.TrimSpace(mustDecodeSwitchProjectForTest(t, tc.input).Project))
			require.NoError(t, err)
			require.Equal(t, full, compact)
		})
	}
}

func TestChannelSwitchProjectCompactProjectionQueryPlan(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()
	seedChannelSwitchProject(t, db, "switch-alpha", "Alpha", true, "", "", "")

	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{ProjectID: "switch-alpha", ProjectRepo: projectRepo})
	counter.Reset()
	counter.SetEnabled(true)
	_, err := handlers["switch_project"](ctx, json.RawMessage(`{"project":"missing"}`))
	counter.SetEnabled(false)
	require.NoError(t, err)
	statements := counter.Statements()
	require.Len(t, statements, 1)

	rows, err := db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+statements[0])
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	planText := plan.String()
	require.NotContains(t, planText, "USE TEMP B-TREE FOR ORDER BY")
	require.Contains(t, planText, "idx_projects_selector_order")
}

func normalizeChannelSwitchProjectSQL(statement string) string {
	return strings.ToLower(strings.Join(strings.Fields(statement), " "))
}

type channelSwitchProjectBenchmarkRunner struct {
	name string
	run  func(context.Context, json.RawMessage) (string, error)
}

func seedChannelSwitchProjectBenchmark(t testing.TB, db *sql.DB, count int) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `DELETE FROM projects`)
	require.NoError(t, err)
	description := strings.Repeat("D", 16*1024)
	repoPath := strings.Repeat("p", 2*1024)
	repoURL := strings.Repeat("u", 2*1024)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, 0)`)
	require.NoError(t, err)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("switch-bench-%06d", i)
		name := fmt.Sprintf("Switch Project %06d", i)
		_, err := stmt.ExecContext(ctx, id, name, description, repoPath, repoURL)
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
}

func channelSwitchProjectBenchmarkCases(count int) []struct {
	name  string
	input string
} {
	firstName := "Switch Project 000000"
	lastName := fmt.Sprintf("Switch Project %06d", count-1)
	return []struct {
		name  string
		input string
	}{
		{name: "first-match", input: fmt.Sprintf(`{"project":%q}`, firstName)},
		{name: "last-match", input: fmt.Sprintf(`{"project":%q}`, lastName)},
		{name: "id-match", input: `{"project":"switch-bench-000000"}`},
		{name: "case-insensitive-match", input: fmt.Sprintf(`{"project":%q}`, strings.ToLower(firstName))},
		{name: "miss", input: `{"project":"missing project"}`},
	}
}

func BenchmarkChannelSwitchProjectHandlerProjection(b *testing.B) {
	for _, count := range []int{1, 50, 500} {
		b.Run(fmt.Sprintf("projects=%d", count), func(b *testing.B) {
			db, counter := testutil.NewStatementCountingTestDB(b)
			seedChannelSwitchProjectBenchmark(b, db, count)
			projectRepo := repository.NewProjectRepo(db)
			ctx := context.Background()
			handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
				ProjectID:   "switch-bench-000000",
				ProjectRepo: projectRepo,
				SwitchProject: func(context.Context, *models.Project) error {
					return nil
				},
			})
			runners := []channelSwitchProjectBenchmarkRunner{
				{name: "compact-handler", run: handlers["switch_project"]},
				{name: "full-list-baseline", run: func(ctx context.Context, input json.RawMessage) (string, error) {
					req := mustDecodeSwitchProjectForBenchmark(b, input)
					return switchChannelProjectResultForTest(ctx, projectRepo.List, strings.TrimSpace(req.Project))
				}},
			}

			for _, tc := range channelSwitchProjectBenchmarkCases(count) {
				for _, runner := range runners {
					tc := tc
					runner := runner
					b.Run(tc.name+"/"+runner.name, func(b *testing.B) {
						input := json.RawMessage(tc.input)
						counter.Reset()
						counter.SetEnabled(true)
						want, err := runner.run(ctx, input)
						counter.SetEnabled(false)
						require.NoError(b, err)
						require.Len(b, counter.Statements(), 1)
						statementCount := len(counter.Statements())
						selectedTextBytes := counter.SelectedTextBytes()
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							got, err := runner.run(ctx, input)
							if err != nil {
								b.Fatal(err)
							}
							if got != want {
								b.Fatalf("response changed: got %q want %q", got, want)
							}
						}
						b.StopTimer()
						median := measureChannelSwitchProjectMedian(b, runner.run, ctx, input)
						b.ReportMetric(float64(len(want)), "response-bytes/op")
						b.ReportMetric(float64(statementCount), "sql-statements/op")
						b.ReportMetric(float64(selectedTextBytes), "selected-text-bytes/op")
						b.ReportMetric(float64(median.Nanoseconds()), "wall-median-ns/op")
					})
				}
			}
		})
	}
}

func measureChannelSwitchProjectMedian(t testing.TB, run func(context.Context, json.RawMessage) (string, error), ctx context.Context, input json.RawMessage) time.Duration {
	t.Helper()
	const samples = 7
	want, err := run(ctx, input)
	require.NoError(t, err)
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		started := time.Now()
		got, err := run(ctx, input)
		require.NoError(t, err)
		if got != want {
			t.Fatalf("response changed during measurement: got %q want %q", got, want)
		}
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

func switchChannelProjectResultForTest(ctx context.Context, listProjects func(context.Context) ([]models.Project, error), targetProject string) (string, error) {
	if targetProject == "" {
		return "Project switch requires a project name or ID.", nil
	}
	selection, err := selectChannelProjectWithList(ctx, listProjects, "", targetProject, nil)
	if err != nil {
		return "Error loading projects: " + err.Error(), nil
	}
	if selection.Target == nil {
		return fmt.Sprintf("Project not found: %q. Available projects: %s", selection.TargetName, strings.Join(selection.AvailableNames, ", ")), nil
	}
	return fmt.Sprintf("Switched to project: %s. Future messages from this channel identity will use that project.", selection.Target.Name), nil
}

func mustDecodeSwitchProjectForTest(t testing.TB, input string) SwitchProjectRequest {
	t.Helper()
	var req SwitchProjectRequest
	require.NoError(t, json.Unmarshal([]byte(input), &req))
	return req
}

func mustDecodeSwitchProjectForBenchmark(t testing.TB, input json.RawMessage) SwitchProjectRequest {
	t.Helper()
	var req SwitchProjectRequest
	require.NoError(t, json.Unmarshal(input, &req))
	return req
}
