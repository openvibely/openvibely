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

const (
	validationProjectionSamples    = 7
	validationProjectionIterations = 3
)

type validationProjectionFixture struct {
	reader      *sql.DB
	writer      *sql.DB
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
			compact := fixture.measure(t)

			if compact.load.statements != 1 {
				t.Fatalf("compact project-load SQL statements = %d, want one", compact.load.statements)
			}
			if compact.fullValidation.statements != 1 {
				t.Fatalf("compact validation SQL statements = %d, want one", compact.fullValidation.statements)
			}
			if projectCount == 500 {
				if compact.load.allocatedBytes > 4*1024*1024 {
					t.Fatalf("compact project-load allocated bytes = %d, want at most %d", compact.load.allocatedBytes, 4*1024*1024)
				}
			}

			t.Logf("%d projects: compact load median/p95=%s/%s, %d B/op, %d allocs/op, %d selected/scanned bytes, %d SQL statements; validation=%s/%s, %d B/op, %d allocs/op, %d SQL statements",
				projectCount,
				compact.load.latency, compact.load.p95Latency, compact.load.allocatedBytes, compact.load.allocations, compact.load.selectedBytes, compact.load.statements,
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
	reader, writer, counter := testutil.NewFileBackedSplitStatementCountingTestDB(tb)
	if reader.Stats().MaxOpenConnections != 1 || writer.Stats().MaxOpenConnections != 1 {
		tb.Fatalf("validation fixture requires production 1W + 1R topology: reader_max=%d writer_max=%d", reader.Stats().MaxOpenConnections, writer.Stats().MaxOpenConnections)
	}
	var readerQueryOnly, writerQueryOnly int
	if err := reader.QueryRow(`PRAGMA query_only`).Scan(&readerQueryOnly); err != nil {
		tb.Fatalf("read reader query_only pragma: %v", err)
	}
	if err := writer.QueryRow(`PRAGMA query_only`).Scan(&writerQueryOnly); err != nil {
		tb.Fatalf("read writer query_only pragma: %v", err)
	}
	if readerQueryOnly != 1 || writerQueryOnly != 0 {
		tb.Fatalf("validation fixture query_only: reader=%d writer=%d, want 1/0", readerQueryOnly, writerQueryOnly)
	}
	unregisterWriter := repository.RegisterDedicatedWriter(reader, writer)
	tb.Cleanup(unregisterWriter)

	repoPath := filepath.Join(tb.TempDir(), "existing-repository")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		tb.Fatalf("create existing repository path: %v", err)
	}
	seedValidationProjectionProjects(tb, writer, projectCount, repoPath)
	repo := repository.NewProjectRepo(reader)
	return &validationProjectionFixture{
		reader:      reader,
		writer:      writer,
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

func (fixture *validationProjectionFixture) measure(tb testing.TB) validationProjectionFixtureMeasurement {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	for range 2 {
		fixture.measureProjectLoad(tb)
		fixture.measureCompleteValidation(tb)
	}

	loadLatencies := make([]time.Duration, 0, validationProjectionSamples)
	loadBytes := make([]uint64, 0, validationProjectionSamples)
	loadAllocs := make([]uint64, 0, validationProjectionSamples)
	for range validationProjectionSamples {
		measurement := fixture.measureProjectLoad(tb)
		loadLatencies = append(loadLatencies, measurement.latency)
		loadBytes = append(loadBytes, measurement.allocatedBytes)
		loadAllocs = append(loadAllocs, measurement.allocations)
	}
	loadStatements := fixture.countProjectLoadStatements(tb)
	load := summarizeValidationMeasurement(tb, loadLatencies, loadBytes, loadAllocs, fixture.selectedBytes(tb), loadStatements)

	validationLatencies := make([]time.Duration, 0, validationProjectionSamples)
	validationBytes := make([]uint64, 0, validationProjectionSamples)
	validationAllocs := make([]uint64, 0, validationProjectionSamples)
	for range validationProjectionSamples {
		measurement := fixture.measureCompleteValidation(tb)
		validationLatencies = append(validationLatencies, measurement.latency)
		validationBytes = append(validationBytes, measurement.allocatedBytes)
		validationAllocs = append(validationAllocs, measurement.allocations)
	}
	validationStatements := fixture.countCompleteValidationStatements(tb)
	fullValidation := summarizeValidationMeasurement(tb, validationLatencies, validationBytes, validationAllocs, 0, validationStatements)
	return validationProjectionFixtureMeasurement{load: load, fullValidation: fullValidation}
}

func (fixture *validationProjectionFixture) measureProjectLoad(tb testing.TB) validationProjectionMeasurement {
	tb.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startedAt := time.Now()
	for range validationProjectionIterations {
		if _, err := fixture.projectRepo.ListRepoValidationProjects(context.Background()); err != nil {
			tb.Fatalf("compact project load: %v", err)
		}
	}
	elapsed := time.Since(startedAt)
	runtime.ReadMemStats(&after)
	return validationProjectionMeasurement{
		latency:        elapsed / validationProjectionIterations,
		allocatedBytes: (after.TotalAlloc - before.TotalAlloc) / validationProjectionIterations,
		allocations:    (after.Mallocs - before.Mallocs) / validationProjectionIterations,
	}
}

func (fixture *validationProjectionFixture) measureCompleteValidation(tb testing.TB) validationProjectionMeasurement {
	tb.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startedAt := time.Now()
	if got := fixture.projectSvc.ValidateRepoPaths(context.Background()); len(got) != 0 {
		tb.Fatalf("compact validation warnings = %v", got)
	}
	elapsed := time.Since(startedAt)
	runtime.ReadMemStats(&after)
	return validationProjectionMeasurement{
		latency:        elapsed,
		allocatedBytes: after.TotalAlloc - before.TotalAlloc,
		allocations:    after.Mallocs - before.Mallocs,
	}
}

func (fixture *validationProjectionFixture) countProjectLoadStatements(tb testing.TB) int {
	tb.Helper()
	fixture.counter.Reset()
	fixture.counter.SetEnabled(true)
	if _, err := fixture.projectRepo.ListRepoValidationProjects(context.Background()); err != nil {
		tb.Fatalf("count compact project load: %v", err)
	}
	fixture.counter.SetEnabled(false)
	return len(fixture.counter.Statements())
}

func (fixture *validationProjectionFixture) countCompleteValidationStatements(tb testing.TB) int {
	tb.Helper()
	fixture.counter.Reset()
	fixture.counter.SetEnabled(true)
	fixture.measureCompleteValidation(tb)
	fixture.counter.SetEnabled(false)
	return len(fixture.counter.Statements())
}

func (fixture *validationProjectionFixture) selectedBytes(tb testing.TB) int {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	return validationCompactSelectedBytes(tb, fixture.projectRepo)
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
