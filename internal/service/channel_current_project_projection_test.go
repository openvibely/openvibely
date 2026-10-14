package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestChannelCurrentProjectUsesIdentityProjection(t *testing.T) {
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	ctx := context.Background()
	projectRepo := db.Projects
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
			require.Equal(t, 1, counter.Snapshot().Requests)
			require.Less(t, counter.Snapshot().ReadTextBytes, 1024)

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
			require.Equal(t, 1, counter.Snapshot().Requests)
			require.Less(t, counter.Snapshot().ReadTextBytes, 1024)
		})
	}
}

func TestChannelCurrentProjectUsesCompactProjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped channel project fixture in short mode")
	}

	for _, fileBacked := range []bool{false, true} {
		topology := "in_memory"
		if fileBacked {
			topology = "file_backed_1w_1r"
		}
		for _, large := range []bool{false, true} {
			for _, missing := range []bool{false, true} {
				caseName := fmt.Sprintf("%s/%s/%s", topology, map[bool]string{false: "empty", true: "large"}[large], map[bool]string{false: "existing", true: "missing"}[missing])
				t.Run(caseName, func(t *testing.T) {
					fixture := newChannelCurrentProjectProjectionFixture(t, fileBacked, large, missing)
					compactObserved := fixture.observeLookup(t, fixture.compactResult)

					require.Equal(t, compactObserved.sqlStatements, 1)
					require.Less(t, compactObserved.selectedTextBytes, 1024, "identity lookup selected text must stay bounded")
				})
			}
		}
	}
}

type channelCurrentProjectProjectionFixture struct {
	counter        storefixture.IOCounter
	compactHandler chatcontrol.RuntimeActionHandler
	expected       string
}

func newChannelCurrentProjectProjectionFixture(tb testing.TB, fileBacked, large, missing bool) *channelCurrentProjectProjectionFixture {
	tb.Helper()

	opts := []teststores.Option{teststores.WithIOCounting()}
	if fileBacked {
		opts = append(opts, teststores.WithProductionTopology())
	}
	env := teststores.New(tb, opts...)
	projectRepo := env.Projects
	counter := env.IO

	project := &models.Project{Name: "Benchmark channel project"}
	if large {
		project.Description = strings.Repeat("d", 16<<10)
		project.RepoPath = strings.Repeat("p", 2<<10)
		project.RepoURL = strings.Repeat("u", 2<<10)
	}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		tb.Fatalf("Create: %v", err)
	}
	lookupID := project.ID
	expected := fmt.Sprintf("Current project: %s (id: %s)", project.Name, project.ID)
	if missing {
		lookupID = "missing-channel-project"
		expected = "Current project ID: " + lookupID + " (details unavailable)"
	}
	return &channelCurrentProjectProjectionFixture{
		counter: counter,
		compactHandler: buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
			ChannelDisplayName: "Slack",
			ProjectID:          lookupID,
			ProjectRepo:        projectRepo,
		})["get_current_project"],
		expected: expected,
	}
}

func (f *channelCurrentProjectProjectionFixture) compactResult() (string, error) {
	return f.compactHandler(context.Background(), json.RawMessage(nil))
}

func BenchmarkChannelCurrentProjectProjection(b *testing.B) {
	for _, fileBacked := range []bool{false, true} {
		topology := "in_memory"
		if fileBacked {
			topology = "file_backed_1w_1r"
		}
		for _, large := range []bool{false, true} {
			for _, missing := range []bool{false, true} {
				fixture := newChannelCurrentProjectProjectionFixture(b, fileBacked, large, missing)
				caseName := fmt.Sprintf("%s/%s", map[bool]string{false: "empty", true: "large"}[large], map[bool]string{false: "existing", true: "missing"}[missing])
				b.Run(topology+"/"+caseName+"/compact_channel_handler", func(b *testing.B) {
					runChannelCurrentProjectProjectionBenchmark(b, fixture, fixture.compactResult)
				})
			}
		}
	}
}

type channelCurrentProjectProjectionMeasurement struct {
	responseBytes     int
	selectedTextBytes int
	sqlStatements     int
}

func (f *channelCurrentProjectProjectionFixture) observeLookup(tb testing.TB, lookup func() (string, error)) channelCurrentProjectProjectionMeasurement {
	tb.Helper()
	f.counter.Reset()
	f.counter.SetEnabled(true)
	got, err := lookup()
	f.counter.SetEnabled(false)
	if err != nil {
		tb.Fatalf("observed lookup: %v", err)
	}
	if got != f.expected {
		tb.Fatalf("observed lookup = %q, want %q", got, f.expected)
	}
	stats := f.counter.Snapshot()
	if stats.Requests != 1 {
		tb.Fatalf("observed SQL statements = %d, want one: %v", stats.Requests, stats)
	}
	return channelCurrentProjectProjectionMeasurement{
		responseBytes:     len(got),
		selectedTextBytes: stats.ReadTextBytes,
		sqlStatements:     stats.Requests,
	}
}

func runChannelCurrentProjectProjectionBenchmark(b *testing.B, fixture *channelCurrentProjectProjectionFixture, lookup func() (string, error)) {
	b.Helper()
	observed := fixture.observeLookup(b, lookup)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := lookup()
		if err != nil {
			b.Fatal(err)
		}
		if got != fixture.expected {
			b.Fatalf("lookup = %q, want %q", got, fixture.expected)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(observed.selectedTextBytes), "selected_text_bytes/op")
	b.ReportMetric(float64(observed.responseBytes), "response_bytes/op")
	b.ReportMetric(float64(observed.sqlStatements), "sql_statements/op")
}
