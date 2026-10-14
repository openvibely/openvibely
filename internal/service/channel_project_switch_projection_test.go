package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func seedChannelSwitchProject(t testing.TB, db *teststores.Env, id, name string, isDefault bool, description, repoPath, repoURL string) {
	t.Helper()
	require.NoError(t, db.Fixtures.SeedProjects(t.Context(), []models.Project{{ID: id, Name: name, IsDefault: isDefault, Description: description, RepoPath: repoPath, RepoURL: repoURL}}))
}

func TestChannelSwitchProjectUsesCompactProjection(t *testing.T) {
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	projectRepo := db.Projects
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

	require.Equal(t, 1, counter.Snapshot().Requests)
	require.Less(t, counter.Snapshot().ReadTextBytes, 128)

	counter.Reset()
	counter.SetEnabled(true)
	listResult, err := handlers["list_projects"](ctx, nil)
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.Contains(t, listResult, "Beta - "+description)
	require.Equal(t, 1, counter.Snapshot().Requests)
	require.Greater(t, counter.Snapshot().ReadTextBytes, len(description))
}

func TestChannelSwitchProjectCompactProjectionPreservesResponses(t *testing.T) {
	db := teststores.New(t)
	projectRepo := db.Projects
	ctx := context.Background()
	require.NoError(t, db.Fixtures.RemoveAllProjects(ctx))

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

type channelSwitchProjectBenchmarkRunner struct {
	name string
	run  func(context.Context, json.RawMessage) (string, error)
}

func seedChannelSwitchProjectBenchmark(t testing.TB, db *teststores.Env, count int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.Fixtures.RemoveAllProjects(ctx))
	description := strings.Repeat("D", 16*1024)
	repoPath := strings.Repeat("p", 2*1024)
	repoURL := strings.Repeat("u", 2*1024)
	projects := make([]models.Project, 0, count)
	for i := 0; i < count; i++ {
		projects = append(projects, models.Project{ID: fmt.Sprintf("switch-bench-%06d", i), Name: fmt.Sprintf("Switch Project %06d", i), Description: description, RepoPath: repoPath, RepoURL: repoURL})
	}
	require.NoError(t, db.Fixtures.SeedProjects(ctx, projects))
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
			db := teststores.New(b, teststores.WithIOCounting())
			counter := db.IO
			seedChannelSwitchProjectBenchmark(b, db, count)
			projectRepo := db.Projects
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
						require.Equal(b, 1, counter.Snapshot().Requests)
						statementCount := counter.Snapshot().Requests
						selectedTextBytes := counter.Snapshot().ReadTextBytes
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
						b.ReportMetric(float64(statementCount), "backend-requests/op")
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
