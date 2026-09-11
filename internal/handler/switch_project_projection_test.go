package handler

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func seedSwitchProject(t testing.TB, db *sql.DB, id, name string, isDefault bool, description, repoPath, repoURL string) {
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

func TestWebAPISwitchProjectUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, _, _ := setupTestHandlerForDB(t, db)
	ctx := context.Background()

	project := &models.Project{
		ID:          "switch-heavy",
		Name:        "Heavy Switch Project",
		Description: strings.Repeat("D", 16*1024),
		RepoPath:    strings.Repeat("p", 2*1024),
		RepoURL:     strings.Repeat("u", 2*1024),
	}
	seedSwitchProject(t, db, project.ID, project.Name, false, project.Description, project.RepoPath, project.RepoURL)

	counter.Reset()
	counter.SetEnabled(true)
	result := h.executeSwitchProject(ctx, "", []byte(`{"project":"heavy switch project"}`))
	counter.SetEnabled(false)

	require.Contains(t, result, "Switched to project: Heavy Switch Project (id: switch-heavy)")
	statements := counter.Statements()
	require.Len(t, statements, 1)
	require.Equal(t,
		"select id, name, is_default from projects order by is_default desc, name asc, id asc",
		normalizeSwitchProjectSQL(statements[0]),
	)

	full, err := h.projectRepo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, project.Description, full.Description)
	require.Equal(t, project.RepoPath, full.RepoPath)
	require.Equal(t, project.RepoURL, full.RepoURL)
}

func TestWebAPISwitchProjectPreservesLookupSemantics(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		t.Fatalf("clear projects: %v", err)
	}
	seedSwitchProject(t, db, "switch-default", "Default", true, "", "", "")
	seedSwitchProject(t, db, "switch-z", "Duplicate", false, "", "", "")
	seedSwitchProject(t, db, "switch-a", "Duplicate", false, "", "", "")
	seedSwitchProject(t, db, "switch-b", "Beta", false, "", "", "")

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "id lookup",
			input: `{"project":"switch-b"}`,
			want:  "Switched to project: Beta (id: switch-b). Use this project for subsequent actions.",
		},
		{
			name:  "case insensitive name lookup",
			input: `{"project":"dUpLiCaTe"}`,
			want:  "Switched to project: Duplicate (id: switch-a). Use this project for subsequent actions.",
		},
		{
			name:  "default project lookup",
			input: `{"project":"default"}`,
			want:  "Switched to project: Default (id: switch-default). Use this project for subsequent actions.",
		},
		{
			name:  "available-name miss",
			input: `{"project":"missing"}`,
			want:  `Project not found: "missing". Available projects: Default, Beta, Duplicate, Duplicate`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, h.executeSwitchProject(ctx, "", []byte(tc.input)))
		})
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		t.Fatalf("clear projects for empty workspace: %v", err)
	}
	require.Equal(t,
		`Project not found: "missing". Available projects: `,
		h.executeSwitchProject(ctx, "", []byte(`{"project":"missing"}`)),
	)
}

func normalizeSwitchProjectSQL(statement string) string {
	return strings.ToLower(strings.Join(strings.Fields(statement), " "))
}

func seedSwitchProjectBenchmark(t testing.TB, db *sql.DB, count int) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		t.Fatalf("clear benchmark projects: %v", err)
	}

	description := strings.Repeat("D", 16*1024)
	repoPath := strings.Repeat("p", 2*1024)
	repoURL := strings.Repeat("u", 2*1024)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin benchmark seed: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, 0)`)
	if err != nil {
		tx.Rollback()
		t.Fatalf("prepare benchmark seed: %v", err)
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("switch-bench-%06d", i)
		name := fmt.Sprintf("Switch Project %06d", i)
		if _, err := stmt.ExecContext(ctx, id, name, description, repoPath, repoURL); err != nil {
			stmt.Close()
			tx.Rollback()
			t.Fatalf("seed benchmark project %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		t.Fatalf("close benchmark seed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit benchmark seed: %v", err)
	}
}

func switchProjectBenchmarkCases(count int) []struct {
	name  string
	input string
} {
	firstName := "Switch Project 000000"
	lastName := fmt.Sprintf("Switch Project %06d", count-1)
	return []struct {
		name  string
		input string
	}{
		{name: "first-name", input: fmt.Sprintf(`{"project":%q}`, firstName)},
		{name: "last-name", input: fmt.Sprintf(`{"project":%q}`, lastName)},
		{name: "id", input: fmt.Sprintf(`{"project":%q}`, "switch-bench-000000")},
		{name: "case-insensitive-name", input: fmt.Sprintf(`{"project":%q}`, strings.ToLower(firstName))},
		{name: "miss", input: `{"project":"missing project"}`},
	}
}

func switchProjectListName(variant string) string {
	if variant == "full" {
		return "full-row"
	}
	return "compact"
}

func runSwitchProjectLookup(h *Handler, ctx context.Context, input []byte, variant string) string {
	if variant == "full" {
		return h.executeSwitchProjectWithList(ctx, "", input, h.projectRepo.List)
	}
	return h.executeSwitchProject(ctx, "", input)
}

func measureSwitchProjectMedian(t testing.TB, h *Handler, ctx context.Context, input []byte, variant string) time.Duration {
	t.Helper()
	const sampleCount = 7
	samples := make([]time.Duration, 0, sampleCount)
	want := runSwitchProjectLookup(h, ctx, input, variant)
	for i := 0; i < sampleCount; i++ {
		started := time.Now()
		got := runSwitchProjectLookup(h, ctx, input, variant)
		if got != want {
			t.Fatalf("switch_project response changed during measurement: got %q want %q", got, want)
		}
		samples = append(samples, time.Since(started))
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[len(samples)/2]
}

func BenchmarkExecuteSwitchProjectProjection(b *testing.B) {
	for _, count := range []int{1, 50, 500} {
		b.Run(fmt.Sprintf("projects=%d", count), func(b *testing.B) {
			db, counter := testutil.NewStatementCountingTestDB(b)
			seedSwitchProjectBenchmark(b, db, count)
			h, _, _ := setupTestHandlerForDB(b, db)
			ctx := context.Background()

			for _, tc := range switchProjectBenchmarkCases(count) {
				for _, variant := range []string{"full", "compact"} {
					variant := variant
					tc := tc
					b.Run(tc.name+"/"+switchProjectListName(variant), func(b *testing.B) {
						counter.Reset()
						counter.SetEnabled(true)
						want := runSwitchProjectLookup(h, ctx, []byte(tc.input), variant)
						counter.SetEnabled(false)
						if statements := len(counter.Statements()); statements != 1 {
							b.Fatalf("%s lookup statements = %d, want 1", variant, statements)
						}

						b.ReportAllocs()
						b.ReportMetric(float64(len(want)), "response-bytes/op")
						b.ReportMetric(1, "sql-statements/op")
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							if got := runSwitchProjectLookup(h, ctx, []byte(tc.input), variant); got != want {
								b.Fatalf("response changed: got %q want %q", got, want)
							}
						}
						b.StopTimer()
						median := measureSwitchProjectMedian(b, h, ctx, []byte(tc.input), variant)
						b.ReportMetric(float64(median.Nanoseconds()), "wall-median-ns/op")
					})
				}
			}
		})
	}
}

func TestWebAPISwitchProjectCompactProjectionPerformance(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	seedSwitchProjectBenchmark(t, db, 500)
	h, _, _ := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	input := []byte(`{"project":"missing project"}`)

	for _, variant := range []string{"full", "compact"} {
		counter.Reset()
		counter.SetEnabled(true)
		response := runSwitchProjectLookup(h, ctx, input, variant)
		counter.SetEnabled(false)
		require.Len(t, counter.Statements(), 1, "%s-row lookup statement count", variant)
		require.Contains(t, response, "Available projects:")
	}

	fullResponse := runSwitchProjectLookup(h, ctx, input, "full")
	compactResponse := runSwitchProjectLookup(h, ctx, input, "compact")
	require.Equal(t, fullResponse, compactResponse)

	full := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			runSwitchProjectLookup(h, ctx, input, "full")
		}
	})
	compact := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			runSwitchProjectLookup(h, ctx, input, "compact")
		}
	})
	fullMedian := measureSwitchProjectMedian(t, h, ctx, input, "full")
	compactMedian := measureSwitchProjectMedian(t, h, ctx, input, "compact")

	t.Logf("500-project miss full: median=%s ns/op=%d B/op=%d allocs/op=%d response-bytes=%d sql-statements=1", fullMedian, full.NsPerOp(), full.AllocedBytesPerOp(), full.AllocsPerOp(), len(fullResponse))
	t.Logf("500-project miss compact: median=%s ns/op=%d B/op=%d allocs/op=%d response-bytes=%d sql-statements=1", compactMedian, compact.NsPerOp(), compact.AllocedBytesPerOp(), compact.AllocsPerOp(), len(compactResponse))
	if compact.AllocedBytesPerOp()*10 > full.AllocedBytesPerOp() {
		t.Fatalf("compact B/op=%d is not at least 90%% lower than full B/op=%d", compact.AllocedBytesPerOp(), full.AllocedBytesPerOp())
	}
	if compactMedian >= fullMedian {
		t.Fatalf("compact median wall time=%s is not lower than full=%s", compactMedian, fullMedian)
	}
}
