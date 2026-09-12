package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

const validationProjectionSamples = 7

type validationProjectionFixture struct {
	db          *sql.DB
	counter     *testutil.SQLStatementCounter
	projectRepo *repository.ProjectRepo
	projectSvc  *ProjectService
	repoPath    string
}

type validationProjectionMeasurement struct {
	latency        time.Duration
	p95Latency     time.Duration
	allocatedBytes uint64
	allocations    uint64
	selectedBytes  int
	statements     int
}

type validationProjectionFixtureMeasurement struct {
	load           validationProjectionMeasurement
	fullValidation validationProjectionMeasurement
}

func TestValidateRepoPathsProjectionProductionPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-backed startup validation projection measurements in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newValidationProjectionFixture(t, projectCount)
			full := fixture.measure(t, false)
			compact := fixture.measure(t, true)

			if full.load.statements != 1 || compact.load.statements != 1 {
				t.Fatalf("project-load SQL statements: full=%d compact=%d, want one each", full.load.statements, compact.load.statements)
			}
			if full.fullValidation.statements != 1 || compact.fullValidation.statements != 1 {
				t.Fatalf("full-validation SQL statements: full=%d compact=%d, want one each", full.fullValidation.statements, compact.fullValidation.statements)
			}
			if projectCount == 500 {
				if compact.load.selectedBytes*5 > full.load.selectedBytes {
					t.Fatalf("compact selected/scanned bytes = %d, full-row baseline = %d; want at least 80%% reduction", compact.load.selectedBytes, full.load.selectedBytes)
				}
				if compact.load.latency*2 > full.load.latency {
					t.Fatalf("compact project-load median = %s, full-row baseline = %s; want at least 50%% reduction", compact.load.latency, full.load.latency)
				}
				if compact.fullValidation.latency > full.fullValidation.latency {
					t.Fatalf("compact full-validation median = %s, full-row baseline = %s; filesystem validation regressed", compact.fullValidation.latency, full.fullValidation.latency)
				}
			}

			t.Logf("%d projects: full load median/p95=%s/%s, %d B/op, %d allocs/op, %d selected/scanned bytes, %d SQL statements; compact=%s/%s, %d B/op, %d allocs/op, %d selected/scanned bytes, %d SQL statements; full validation median/p95=%s/%s, %d B/op, %d allocs/op, %d SQL statements vs compact=%s/%s, %d B/op, %d allocs/op, %d SQL statements",
				projectCount,
				full.load.latency, full.load.p95Latency, full.load.allocatedBytes, full.load.allocations, full.load.selectedBytes, full.load.statements,
				compact.load.latency, compact.load.p95Latency, compact.load.allocatedBytes, compact.load.allocations, compact.load.selectedBytes, compact.load.statements,
				full.fullValidation.latency, full.fullValidation.p95Latency, full.fullValidation.allocatedBytes, full.fullValidation.allocations, full.fullValidation.statements,
				compact.fullValidation.latency, compact.fullValidation.p95Latency, compact.fullValidation.allocatedBytes, compact.fullValidation.allocations, compact.fullValidation.statements,
			)
		})
	}
}

func BenchmarkValidateRepoPathsProjectionFileBacked(b *testing.B) {
	for _, projectCount := range []int{1, 50, 500} {
		fixture := newValidationProjectionFixture(b, projectCount)
		b.Run(fmt.Sprintf("%d_projects/project_load", projectCount), func(b *testing.B) {
			fixture.counter.SetEnabled(false)
			if _, err := fixture.projectRepo.ListRepoValidationProjects(context.Background()); err != nil {
				b.Fatalf("warm compact project load: %v", err)
			}
			fixture.counter.Reset()
			fixture.counter.SetEnabled(true)
			if _, err := fixture.projectRepo.ListRepoValidationProjects(context.Background()); err != nil {
				b.Fatalf("count compact project load: %v", err)
			}
			statementCount := len(fixture.counter.Statements())
			fixture.counter.SetEnabled(false)
			selectedBytes := validationCompactSelectedBytes(b, fixture.projectRepo)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := fixture.projectRepo.ListRepoValidationProjects(context.Background()); err != nil {
					b.Fatalf("compact project load: %v", err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(selectedBytes), "selected_scanned_bytes/op")
			b.ReportMetric(float64(statementCount), "sql_statements/op")
		})

		b.Run(fmt.Sprintf("%d_projects/complete_validation", projectCount), func(b *testing.B) {
			fixture.counter.SetEnabled(false)
			if got := fixture.projectSvc.ValidateRepoPaths(context.Background()); len(got) != 0 {
				b.Fatalf("warm validation warnings = %v", got)
			}
			fixture.counter.Reset()
			fixture.counter.SetEnabled(true)
			if got := fixture.projectSvc.ValidateRepoPaths(context.Background()); len(got) != 0 {
				b.Fatalf("count validation warnings = %v", got)
			}
			statementCount := len(fixture.counter.Statements())
			fixture.counter.SetEnabled(false)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := fixture.projectSvc.ValidateRepoPaths(context.Background()); len(got) != 0 {
					b.Fatalf("validation warnings = %v", got)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(statementCount), "sql_statements/op")
		})
	}
}

func newValidationProjectionFixture(tb testing.TB, projectCount int) *validationProjectionFixture {
	tb.Helper()
	db, counter := testutil.NewFileBackedStatementCountingTestDB(tb)
	repoPath := filepath.Join(tb.TempDir(), "existing-repository")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		tb.Fatalf("create existing repository path: %v", err)
	}
	seedValidationProjectionProjects(tb, db, projectCount, repoPath)
	repo := repository.NewProjectRepo(db)
	return &validationProjectionFixture{
		db:          db,
		counter:     counter,
		projectRepo: repo,
		projectSvc:  NewProjectService(repo),
		repoPath:    repoPath,
	}
}

func seedValidationProjectionProjects(tb testing.TB, db *sql.DB, projectCount int, repoPath string) {
	tb.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("begin validation projection fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		tb.Fatalf("clear projects: %v", err)
	}
	largeDescription := strings.Repeat("production-shaped project description ", 16<<10/len("production-shaped project description "))
	largeURL := "https://github.example.test/openvibely/" + strings.Repeat("repository-configuration/", 80)
	for i := 0; i < projectCount; i++ {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("validation-projection-project-%04d", i), fmt.Sprintf("Validation Project %04d", i),
			largeDescription, repoPath, largeURL, i == 0); err != nil {
			tb.Fatalf("insert validation project %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit validation projection fixture: %v", err)
	}
}

func (fixture *validationProjectionFixture) measure(tb testing.TB, compact bool) validationProjectionFixtureMeasurement {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	for range 2 {
		fixture.measureProjectLoad(tb, compact)
		fixture.measureCompleteValidation(tb, compact)
	}

	loadLatencies := make([]time.Duration, 0, validationProjectionSamples)
	loadBytes := make([]uint64, 0, validationProjectionSamples)
	loadAllocs := make([]uint64, 0, validationProjectionSamples)
	for range validationProjectionSamples {
		measurement := fixture.measureProjectLoad(tb, compact)
		loadLatencies = append(loadLatencies, measurement.latency)
		loadBytes = append(loadBytes, measurement.allocatedBytes)
		loadAllocs = append(loadAllocs, measurement.allocations)
	}
	loadStatements := fixture.countProjectLoadStatements(tb, compact)
	load := summarizeValidationMeasurement(tb, loadLatencies, loadBytes, loadAllocs, fixture.selectedBytes(tb, compact), loadStatements)

	validationLatencies := make([]time.Duration, 0, validationProjectionSamples)
	validationBytes := make([]uint64, 0, validationProjectionSamples)
	validationAllocs := make([]uint64, 0, validationProjectionSamples)
	for range validationProjectionSamples {
		measurement := fixture.measureCompleteValidation(tb, compact)
		validationLatencies = append(validationLatencies, measurement.latency)
		validationBytes = append(validationBytes, measurement.allocatedBytes)
		validationAllocs = append(validationAllocs, measurement.allocations)
	}
	validationStatements := fixture.countCompleteValidationStatements(tb, compact)
	fullValidation := summarizeValidationMeasurement(tb, validationLatencies, validationBytes, validationAllocs, 0, validationStatements)
	return validationProjectionFixtureMeasurement{load: load, fullValidation: fullValidation}
}

func (fixture *validationProjectionFixture) measureProjectLoad(tb testing.TB, compact bool) validationProjectionMeasurement {
	tb.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startedAt := time.Now()
	if compact {
		if _, err := fixture.projectRepo.ListRepoValidationProjects(context.Background()); err != nil {
			tb.Fatalf("compact project load: %v", err)
		}
	} else if _, err := fixture.projectRepo.List(context.Background()); err != nil {
		tb.Fatalf("full project load: %v", err)
	}
	elapsed := time.Since(startedAt)
	runtime.ReadMemStats(&after)
	return validationProjectionMeasurement{
		latency:        elapsed,
		allocatedBytes: after.TotalAlloc - before.TotalAlloc,
		allocations:    after.Mallocs - before.Mallocs,
	}
}

func (fixture *validationProjectionFixture) measureCompleteValidation(tb testing.TB, compact bool) validationProjectionMeasurement {
	tb.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startedAt := time.Now()
	if compact {
		if got := fixture.projectSvc.ValidateRepoPaths(context.Background()); len(got) != 0 {
			tb.Fatalf("compact validation warnings = %v", got)
		}
	} else if got := validateRepoPathsWithFullProjects(context.Background(), fixture.projectRepo); len(got) != 0 {
		tb.Fatalf("full validation warnings = %v", got)
	}
	elapsed := time.Since(startedAt)
	runtime.ReadMemStats(&after)
	return validationProjectionMeasurement{
		latency:        elapsed,
		allocatedBytes: after.TotalAlloc - before.TotalAlloc,
		allocations:    after.Mallocs - before.Mallocs,
	}
}

func (fixture *validationProjectionFixture) countProjectLoadStatements(tb testing.TB, compact bool) int {
	tb.Helper()
	fixture.counter.Reset()
	fixture.counter.SetEnabled(true)
	fixture.measureProjectLoad(tb, compact)
	fixture.counter.SetEnabled(false)
	return len(fixture.counter.Statements())
}

func (fixture *validationProjectionFixture) countCompleteValidationStatements(tb testing.TB, compact bool) int {
	tb.Helper()
	fixture.counter.Reset()
	fixture.counter.SetEnabled(true)
	fixture.measureCompleteValidation(tb, compact)
	fixture.counter.SetEnabled(false)
	return len(fixture.counter.Statements())
}

func (fixture *validationProjectionFixture) selectedBytes(tb testing.TB, compact bool) int {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	if compact {
		return validationCompactSelectedBytes(tb, fixture.projectRepo)
	}
	projects, err := fixture.projectRepo.List(context.Background())
	if err != nil {
		tb.Fatalf("full project rows for selected-byte metric: %v", err)
	}
	var total int
	for _, project := range projects {
		total += len(project.ID) + len(project.Name) + len(project.Description) + len(project.RepoPath) + len(project.RepoURL)
		total++
		if project.DefaultAgentConfigID != nil {
			total += len(*project.DefaultAgentConfigID)
		}
		if project.MaxWorkers != nil {
			total += 8
		}
		total += len(project.CreatedAt.Format(time.RFC3339)) + len(project.UpdatedAt.Format(time.RFC3339))
	}
	return total
}

func validationCompactSelectedBytes(tb testing.TB, repo *repository.ProjectRepo) int {
	tb.Helper()
	projects, err := repo.ListRepoValidationProjects(context.Background())
	if err != nil {
		tb.Fatalf("compact project rows for selected-byte metric: %v", err)
	}
	var total int
	for _, project := range projects {
		total += len(project.ID) + len(project.Name) + len(project.RepoPath) + len(project.RepoURL)
	}
	return total
}

func summarizeValidationMeasurement(tb testing.TB, latencies []time.Duration, allocatedBytes, allocations []uint64, selectedBytes, statements int) validationProjectionMeasurement {
	tb.Helper()
	if len(latencies) == 0 || len(latencies) != len(allocatedBytes) || len(latencies) != len(allocations) {
		tb.Fatalf("invalid validation measurement samples: latencies=%d bytes=%d allocations=%d", len(latencies), len(allocatedBytes), len(allocations))
	}
	slices.Sort(latencies)
	slices.Sort(allocatedBytes)
	slices.Sort(allocations)
	p95Index := (len(latencies)*95+99)/100 - 1
	middle := len(latencies) / 2
	return validationProjectionMeasurement{
		latency:        latencies[middle],
		p95Latency:     latencies[p95Index],
		allocatedBytes: allocatedBytes[middle],
		allocations:    allocations[middle],
		selectedBytes:  selectedBytes,
		statements:     statements,
	}
}

func validateRepoPathsWithFullProjects(ctx context.Context, repo *repository.ProjectRepo) []string {
	projects, err := repo.List(ctx)
	if err != nil {
		return nil
	}
	var missing []string
	for _, project := range projects {
		if project.RepoPath == "" {
			continue
		}
		if _, err := os.Stat(project.RepoPath); os.IsNotExist(err) {
			message := fmt.Sprintf("project %q (id=%s): repo_path %q does not exist on disk", project.Name, project.ID, project.RepoPath)
			if project.RepoURL != "" {
				message += fmt.Sprintf(" (repo_url=%s — may need re-clone or volume mount fix)", project.RepoURL)
			} else {
				message += " (local repo — ensure the path is mounted into the container)"
			}
			missing = append(missing, message)
		}
	}
	return missing
}
