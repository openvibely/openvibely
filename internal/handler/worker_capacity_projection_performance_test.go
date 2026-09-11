package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const workerCapacityProjectionSamples = 5

type workerCapacityProjectionFixture struct {
	connections *database.Connections
	h           *Handler
	e           http.Handler
	projectRepo *repository.ProjectRepo
}

type workerCapacityProjectionMeasurement struct {
	latency                time.Duration
	allocatedBytes         uint64
	allocations            uint64
	fragmentBytes          int
	fullProjectRowBytes    int
	compactProjectRowBytes int
	sqlStatementCount      int
}

func TestHandlerProjectWorkerCapacityProjectionProductionPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-backed Workers capacity projection measurements in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newWorkerCapacityProjectionFixture(t, projectCount)
			full := fixture.measure(t, fixture.renderFullRowBaseline)
			compact := fixture.measure(t, fixture.renderCompactHandler)

			if compact.fragmentBytes != full.fragmentBytes {
				t.Fatalf("rendered fragment bytes = %d, full-row baseline = %d", compact.fragmentBytes, full.fragmentBytes)
			}
			if compact.sqlStatementCount != full.sqlStatementCount {
				t.Fatalf("compact SQL statements = %d, full-row baseline = %d", compact.sqlStatementCount, full.sqlStatementCount)
			}
			if projectCount == 500 && compact.compactProjectRowBytes*10 > full.fullProjectRowBytes {
				t.Fatalf("compact project-row materialization = %d bytes, want at least 90%% below full-row %d bytes", compact.compactProjectRowBytes, full.fullProjectRowBytes)
			}
			if projectCount == 500 && compact.latency >= full.latency {
				t.Fatalf("compact median handler latency = %s, want lower than full-row baseline %s", compact.latency, full.latency)
			}

			t.Logf("%d projects median: full=%s/%d B/op/%d allocs/op/%d project-row bytes/%d fragment bytes/%d SQL statements; compact=%s/%d B/op/%d allocs/op/%d project-row bytes/%d fragment bytes/%d SQL statements",
				projectCount,
				full.latency, full.allocatedBytes, full.allocations, full.fullProjectRowBytes, full.fragmentBytes, full.sqlStatementCount,
				compact.latency, compact.allocatedBytes, compact.allocations, compact.compactProjectRowBytes, compact.fragmentBytes, compact.sqlStatementCount,
			)
		})
	}
}

func BenchmarkHandlerProjectWorkerCapacityProjection(b *testing.B) {
	for _, projectCount := range []int{1, 50, 500} {
		fixture := newWorkerCapacityProjectionFixture(b, projectCount)
		for _, tc := range []struct {
			name   string
			render func(testing.TB) (string, error)
		}{
			{name: "full-row baseline", render: fixture.renderFullRowBaseline},
			{name: "compact handler", render: fixture.renderCompactHandler},
		} {
			b.Run(fmt.Sprintf("%d_projects/%s", projectCount, tc.name), func(b *testing.B) {
				fragment, err := tc.render(b)
				if err != nil {
					b.Fatalf("warm render: %v", err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					fragment, err = tc.render(b)
					if err != nil {
						b.Fatalf("render: %v", err)
					}
				}
				b.ReportMetric(float64(len(fragment)), "rendered_fragment_bytes")
				b.ReportMetric(float64(workerCapacityPollSQLStatementCount), "sql_statements")
			})
		}
	}
}

func newWorkerCapacityProjectionFixture(tb testing.TB, projectCount int) *workerCapacityProjectionFixture {
	tb.Helper()
	connections, err := database.NewReadWrite(filepath.Join(tb.TempDir(), "worker-capacity-projection.db"))
	if err != nil {
		tb.Fatalf("open file-backed production database: %v", err)
	}
	tb.Cleanup(func() {
		if err := connections.Close(); err != nil {
			tb.Errorf("close file-backed production database: %v", err)
		}
	})
	if connections.Reader == connections.Writer || connections.Reader.Stats().MaxOpenConnections != 1 || connections.Writer.Stats().MaxOpenConnections != 1 {
		tb.Fatalf("Workers capacity fixture requires production 1W + 1R topology: reader=%p writer=%p reader_max=%d writer_max=%d",
			connections.Reader, connections.Writer, connections.Reader.Stats().MaxOpenConnections, connections.Writer.Stats().MaxOpenConnections)
	}
	unregister := repository.RegisterDedicatedWriter(connections.Reader, connections.Writer)
	tb.Cleanup(unregister)

	seedWorkerCapacityProjectionFixture(tb, connections.Writer, projectCount)
	h, e, _ := setupTestHandlerForDB(tb, connections.Reader)
	return &workerCapacityProjectionFixture{
		connections: connections,
		h:           h,
		e:           e,
		projectRepo: repository.NewProjectRepo(connections.Reader),
	}
}

func seedWorkerCapacityProjectionFixture(tb testing.TB, db *sql.DB, projectCount int) {
	tb.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("begin fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		tb.Fatalf("clear default project: %v", err)
	}
	projectStmt, err := tx.PrepareContext(ctx, `INSERT INTO projects
		(id, name, description, repo_path, repo_url, is_default, max_workers)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatalf("prepare project fixture: %v", err)
	}
	defer projectStmt.Close()
	taskStmt, err := tx.PrepareContext(ctx, `INSERT INTO tasks
		(id, project_id, title, prompt, category, status)
		VALUES (?, ?, ?, ?, 'active', ?)`)
	if err != nil {
		tb.Fatalf("prepare task fixture: %v", err)
	}
	defer taskStmt.Close()

	largeDescription := strings.Repeat("production-shaped Workers project description ", 384)
	largePath := "/private/workspaces/" + strings.Repeat("nested-local-repository/", 96)
	largeURL := "https://github.example.test/openvibely/" + strings.Repeat("workers-capacity-repository/", 96)
	for i := range projectCount {
		projectID := fmt.Sprintf("worker-capacity-project-%04d", i)
		var maxWorkers any
		switch i % 3 {
		case 1:
			maxWorkers = 3
		case 2:
			maxWorkers = 9
		}
		if _, err := projectStmt.ExecContext(ctx, projectID, fmt.Sprintf("Workers Capacity Project %04d", i),
			largeDescription, largePath, largeURL, i == 0, maxWorkers); err != nil {
			tb.Fatalf("insert project %d: %v", i, err)
		}
		status := "pending"
		if i%2 == 1 {
			status = "queued"
		}
		if _, err := taskStmt.ExecContext(ctx, fmt.Sprintf("worker-capacity-task-%04d", i), projectID,
			"Workers capacity pending task", "representative queued work", status); err != nil {
			tb.Fatalf("insert pending task %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit fixture transaction: %v", err)
	}
}

func (fixture *workerCapacityProjectionFixture) measure(t *testing.T, render func(testing.TB) (string, error)) workerCapacityProjectionMeasurement {
	t.Helper()
	for range 2 {
		if _, err := render(t); err != nil {
			t.Fatalf("warm render: %v", err)
		}
	}

	latencies := make([]time.Duration, 0, workerCapacityProjectionSamples)
	allocatedBytes := make([]uint64, 0, workerCapacityProjectionSamples)
	allocations := make([]uint64, 0, workerCapacityProjectionSamples)
	fragmentBytes := make([]int, 0, workerCapacityProjectionSamples)
	for range workerCapacityProjectionSamples {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		startedAt := time.Now()
		fragment, err := render(t)
		elapsed := time.Since(startedAt)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		latencies = append(latencies, elapsed)
		allocatedBytes = append(allocatedBytes, after.TotalAlloc-before.TotalAlloc)
		allocations = append(allocations, after.Mallocs-before.Mallocs)
		fragmentBytes = append(fragmentBytes, len(fragment))
	}
	fullProjects, err := fixture.projectRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list full project baseline: %v", err)
	}
	capacityProjects, err := fixture.projectRepo.ListWorkerCapacityProjects(context.Background())
	if err != nil {
		t.Fatalf("list compact project rows: %v", err)
	}

	fullProjectRowBytes, compactProjectRowBytes := projectWorkerRowBytes(fullProjects, capacityProjects)

	slices.Sort(latencies)
	slices.Sort(allocatedBytes)
	slices.Sort(allocations)
	slices.Sort(fragmentBytes)
	middle := workerCapacityProjectionSamples / 2
	return workerCapacityProjectionMeasurement{
		latency:                latencies[middle],
		allocatedBytes:         allocatedBytes[middle],
		allocations:            allocations[middle],
		fragmentBytes:          fragmentBytes[middle],
		fullProjectRowBytes:    fullProjectRowBytes,
		compactProjectRowBytes: compactProjectRowBytes,
		sqlStatementCount:      workerCapacityPollSQLStatementCount,
	}
}

func (fixture *workerCapacityProjectionFixture) renderCompactHandler(tb testing.TB) (string, error) {
	tb.Helper()
	rec := httptest.NewRecorder()
	fixture.e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workers/stats/projects", nil))
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), nil
}

func (fixture *workerCapacityProjectionFixture) renderFullRowBaseline(tb testing.TB) (string, error) {
	tb.Helper()
	return renderFullProjectWorkerStats(context.Background(), fixture.h)
}

func projectWorkerRowBytes(full []models.Project, compact []models.ProjectWorkerCapacity) (fullBytes, compactBytes int) {
	for _, project := range full {
		fullBytes += len(project.ID) + len(project.Name) + len(project.Description) + len(project.RepoPath) + len(project.RepoURL)
		if project.DefaultAgentConfigID != nil {
			fullBytes += len(*project.DefaultAgentConfigID)
		}
		if project.MaxWorkers != nil {
			fullBytes += 8
		}
		fullBytes += 17 // is_default plus created_at and updated_at scalar values
	}
	for _, project := range compact {
		compactBytes += len(project.ID) + len(project.Name)
		if project.MaxWorkers != nil {
			compactBytes += 8
		}
	}
	return fullBytes, compactBytes
}
