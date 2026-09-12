package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestChannelCurrentProjectUsesIdentityProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{
		Name:        "Channel identity project",
		Description: strings.Repeat("d", 16<<10),
		RepoPath:    strings.Repeat("p", 2<<10),
		RepoURL:     strings.Repeat("u", 2<<10),
	}
	require.NoError(t, projectRepo.Create(ctx, project))

	for _, channelName := range []string{"Slack", "Telegram", "Discord", "Email"} {
		t.Run(channelName, func(t *testing.T) {
			handlers := buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
				ChannelDisplayName: channelName,
				ProjectID:          project.ID,
				ProjectRepo:        projectRepo,
			})

			counter.Reset()
			counter.SetEnabled(true)
			currentProject, err := handlers["get_current_project"](ctx, nil)
			counter.SetEnabled(false)
			require.NoError(t, err)
			require.Equal(t, "Current project: Channel identity project (id: "+project.ID+")", currentProject)
			requireProjectIdentityQuery(t, counter.Statements())

			missingID := "missing-channel-project"
			missingHandlers := buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
				ChannelDisplayName: channelName,
				ProjectID:          missingID,
				ProjectRepo:        projectRepo,
			})
			counter.Reset()
			counter.SetEnabled(true)
			missingProject, err := missingHandlers["get_current_project"](ctx, nil)
			counter.SetEnabled(false)
			require.NoError(t, err)
			require.Equal(t, "Current project ID: "+missingID+" (details unavailable)", missingProject)
			requireProjectIdentityQuery(t, counter.Statements())
		})
	}
}

func requireProjectIdentityQuery(t *testing.T, statements []string) {
	t.Helper()
	require.Len(t, statements, 1)
	require.Equal(t, "select id, name from projects where id = ?", strings.ToLower(strings.Join(strings.Fields(statements[0]), " ")))
}

type channelCurrentProjectProjectionFixture struct {
	repo           *repository.ProjectRepo
	compactHandler chatcontrol.RuntimeActionHandler
	lookupID       string
	expected       string
	fullRowBytes   int
	compactBytes   int
	responseBytes  int
}

func newChannelCurrentProjectProjectionFixture(tb testing.TB, large bool, missing bool) *channelCurrentProjectProjectionFixture {
	tb.Helper()
	db := testutil.NewTestDB(tb)
	repo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Benchmark channel project"}
	if large {
		project.Description = strings.Repeat("d", 16<<10)
		project.RepoPath = strings.Repeat("p", 2<<10)
		project.RepoURL = strings.Repeat("u", 2<<10)
	}
	if err := repo.Create(context.Background(), project); err != nil {
		tb.Fatalf("Create: %v", err)
	}
	lookupID := project.ID
	expected := fmt.Sprintf("Current project: %s (id: %s)", project.Name, project.ID)
	if missing {
		lookupID = "missing-channel-project"
		expected = "Current project ID: " + lookupID + " (details unavailable)"
	}
	fullRowBytes := 0
	if !missing {
		fullRowBytes = len(project.ID) + len(project.Name) + len(project.Description) + len(project.RepoPath) + len(project.RepoURL)
	}
	compactBytes := 0
	if !missing {
		compactBytes = len(project.ID) + len(project.Name)
	}
	return &channelCurrentProjectProjectionFixture{
		repo: repo,
		compactHandler: buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
			ChannelDisplayName: "Slack",
			ProjectID:          lookupID,
			ProjectRepo:        repo,
		})["get_current_project"],
		lookupID:      lookupID,
		expected:      expected,
		fullRowBytes:  fullRowBytes,
		compactBytes:  compactBytes,
		responseBytes: len(expected),
	}
}

func (f *channelCurrentProjectProjectionFixture) fullResult() (string, error) {
	return fullChannelCurrentProjectResult(context.Background(), f.repo, f.lookupID), nil
}

func (f *channelCurrentProjectProjectionFixture) compactResult() (string, error) {
	return f.compactHandler(context.Background(), json.RawMessage(nil))
}

func fullChannelCurrentProjectResult(ctx context.Context, projectRepo *repository.ProjectRepo, projectID string) string {
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return fmt.Sprintf("Current project ID: %s (details unavailable)", projectID)
	}
	return fmt.Sprintf("Current project: %s (id: %s)", project.Name, project.ID)
}

func BenchmarkChannelCurrentProjectProjection(b *testing.B) {
	for _, large := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			fixture := newChannelCurrentProjectProjectionFixture(b, large, missing)
			caseName := fmt.Sprintf("%s/%s", map[bool]string{false: "empty", true: "large"}[large], map[bool]string{false: "existing", true: "missing"}[missing])
			b.Run(caseName+"/full_GetByID", func(b *testing.B) {
				runChannelCurrentProjectProjectionBenchmark(b, fixture.fullResult, fixture.expected, fixture.fullRowBytes, fixture.responseBytes)
			})
			b.Run(caseName+"/compact_channel_handler", func(b *testing.B) {
				runChannelCurrentProjectProjectionBenchmark(b, func() (string, error) {
					return fixture.compactResult()
				}, fixture.expected, fixture.compactBytes, fixture.responseBytes)
			})
		}
	}
}

func runChannelCurrentProjectProjectionBenchmark(b *testing.B, lookup func() (string, error), expected string, selectedBytes, responseBytes int) {
	b.Helper()
	if got, err := lookup(); err != nil || got != expected {
		b.Fatalf("warm lookup = %q, err=%v, want %q", got, err, expected)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := lookup()
		if err != nil {
			b.Fatal(err)
		}
		if got != expected {
			b.Fatalf("lookup = %q, want %q", got, expected)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(selectedBytes), "selected_text_bytes/op")
	b.ReportMetric(float64(responseBytes), "response_bytes/op")
	b.ReportMetric(1, "sql_statements/op")
}
