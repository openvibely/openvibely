package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

const (
	validationProjectionSamples    = 7
	validationProjectionIterations = 3
)

type validationProjectionFixture struct {
	counter     storefixture.IOCounter
	projectRepo persistence.ProjectStore
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

func TestValidateRepoPathsUsesCompactProductionProjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-backed startup validation fixture in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newValidationProjectionFixture(t, projectCount)
			loadStatements := fixture.countProjectLoadStatements(t)
			if loadStatements != 1 {
				t.Fatalf("compact project-load SQL statements = %d, want one", loadStatements)
			}
			validationStatements := fixture.countCompleteValidationStatements(t)
			if validationStatements != 1 {
				t.Fatalf("compact validation SQL statements = %d, want one", validationStatements)
			}
			if fixture.selectedBytes(t) == 0 {
				t.Fatal("compact project load selected no data")
			}
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
			statementCount := fixture.counter.Snapshot().Requests
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
			statementCount := fixture.counter.Snapshot().Requests
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
	env := teststores.New(tb, teststores.WithProductionTopology(), teststores.WithIOCounting())

	repoPath := filepath.Join(tb.TempDir(), "existing-repository")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		tb.Fatalf("create existing repository path: %v", err)
	}
	seedValidationProjectionProjects(tb, env, projectCount, repoPath)
	repo := env.Projects
	return &validationProjectionFixture{
		counter:     env.IO,
		projectRepo: repo,
		projectSvc:  NewProjectService(repo),
		repoPath:    repoPath,
	}
}

func seedValidationProjectionProjects(tb testing.TB, env *teststores.Env, projectCount int, repoPath string) {
	tb.Helper()
	ctx := context.Background()
	if err := env.Fixtures.RemoveAllProjects(ctx); err != nil {
		tb.Fatal(err)
	}
	var projects []models.Project
	largeDescription := strings.Repeat("production-shaped project description ", 16<<10/len("production-shaped project description "))
	largeURL := "https://github.example.test/openvibely/" + strings.Repeat("repository-configuration/", 80)
	for i := 0; i < projectCount; i++ {
		p := models.Project{ID: fmt.Sprintf("validation-projection-project-%04d", i), Name: fmt.Sprintf("Validation Project %04d", i), Description: largeDescription, RepoPath: repoPath, RepoURL: largeURL, IsDefault: i == 0}
		projects = append(projects, p)
	}
	if err := env.Fixtures.SeedProjects(ctx, projects); err != nil {
		tb.Fatal(err)
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
	return fixture.counter.Snapshot().Requests
}

func (fixture *validationProjectionFixture) countCompleteValidationStatements(tb testing.TB) int {
	tb.Helper()
	fixture.counter.Reset()
	fixture.counter.SetEnabled(true)
	if got := fixture.projectSvc.ValidateRepoPaths(context.Background()); len(got) != 0 {
		tb.Fatalf("compact validation warnings = %v", got)
	}
	fixture.counter.SetEnabled(false)
	return fixture.counter.Snapshot().Requests
}

func (fixture *validationProjectionFixture) selectedBytes(tb testing.TB) int {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	return validationCompactSelectedBytes(tb, fixture.projectRepo)
}

func validationCompactSelectedBytes(tb testing.TB, repo persistence.ProjectStore) int {
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
