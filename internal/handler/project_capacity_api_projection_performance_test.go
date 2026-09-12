package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

const projectCapacityAPIProjectionSamples = 7

type projectCapacityAPIProjectionFixture struct {
	counter     *testutil.SQLStatementCounter
	projectRepo *repository.ProjectRepo
	projectSvc  *service.ProjectService
	h           *Handler
	e           *echo.Echo
}

type projectCapacityAPIProjectionMeasurement struct {
	medianLatency        time.Duration
	p95Latency           time.Duration
	allocatedBytes       uint64
	allocations          uint64
	selectedProjectBytes int
	responseBytes        int
	sqlStatements        int
}

func TestGetProjectCapacitiesProjectionProductionPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-backed project capacity API projection measurements in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newProjectCapacityAPIProjectionFixture(t, projectCount)
			full := fixture.measure(t, fixture.renderFullRowBaseline, true)
			compact := fixture.measure(t, fixture.renderCompactHandler, false)

			if compact.responseBytes != full.responseBytes {
				t.Fatalf("compact JSON response bytes = %d, full-row baseline = %d", compact.responseBytes, full.responseBytes)
			}
			if compact.sqlStatements != full.sqlStatements {
				t.Fatalf("compact SQL statements = %d, full-row baseline = %d", compact.sqlStatements, full.sqlStatements)
			}
			if projectCount == 500 {
				if compact.selectedProjectBytes*10 > full.selectedProjectBytes {
					t.Fatalf("compact selected project bytes = %d, want at least 90%% below full-row %d bytes", compact.selectedProjectBytes, full.selectedProjectBytes)
				}
				if compact.medianLatency >= full.medianLatency {
					t.Fatalf("compact median latency = %s, want lower than full-row baseline %s", compact.medianLatency, full.medianLatency)
				}
			}

			t.Logf("%d projects: full median/p95=%s/%s, %d B/op, %d allocs/op, %d selected project bytes, %d JSON bytes, %d SQL statements; compact median/p95=%s/%s, %d B/op, %d allocs/op, %d selected project bytes, %d JSON bytes, %d SQL statements",
				projectCount,
				full.medianLatency, full.p95Latency, full.allocatedBytes, full.allocations, full.selectedProjectBytes, full.responseBytes, full.sqlStatements,
				compact.medianLatency, compact.p95Latency, compact.allocatedBytes, compact.allocations, compact.selectedProjectBytes, compact.responseBytes, compact.sqlStatements,
			)
		})
	}
}

func BenchmarkGetProjectCapacitiesProjection(b *testing.B) {
	for _, projectCount := range []int{1, 50, 500} {
		fixture := newProjectCapacityAPIProjectionFixture(b, projectCount)
		for _, tc := range []struct {
			name   string
			render func(testing.TB) (string, error)
			full   bool
		}{
			{name: "full-row baseline", render: fixture.renderFullRowBaseline, full: true},
			{name: "compact API handler", render: fixture.renderCompactHandler, full: false},
		} {
			b.Run(fmt.Sprintf("%d_projects/%s", projectCount, tc.name), func(b *testing.B) {
				body, err := tc.render(b)
				if err != nil {
					b.Fatalf("warm render: %v", err)
				}
				selectedProjectBytes := fixture.selectedProjectBytes(b, tc.full)
				responseBytes, sqlStatements := fixture.queryMetrics(b, tc.render)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					body, err = tc.render(b)
					if err != nil {
						b.Fatalf("render: %v", err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(len(body)), "json_response_bytes")
				b.ReportMetric(float64(responseBytes), "validated_json_response_bytes")
				b.ReportMetric(float64(selectedProjectBytes), "selected_project_bytes")
				b.ReportMetric(float64(sqlStatements), "sql_statements")
			})
		}
	}
}

func newProjectCapacityAPIProjectionFixture(tb testing.TB, projectCount int) *projectCapacityAPIProjectionFixture {
	tb.Helper()
	reader, writer, counter := testutil.NewFileBackedSplitStatementCountingTestDB(tb)
	if reader == writer || reader.Stats().MaxOpenConnections != 1 || writer.Stats().MaxOpenConnections != 1 {
		tb.Fatalf("project capacity fixture requires production 1W + 1R topology: reader=%p writer=%p reader_max=%d writer_max=%d",
			reader, writer, reader.Stats().MaxOpenConnections, writer.Stats().MaxOpenConnections)
	}
	unregister := repository.RegisterDedicatedWriter(reader, writer)
	tb.Cleanup(unregister)
	seedProjectCapacityAPIProjectionFixture(tb, writer, projectCount)

	h, e, _ := setupTestHandlerForDB(tb, reader)
	return &projectCapacityAPIProjectionFixture{
		counter:     counter,
		projectRepo: repository.NewProjectRepo(reader),
		projectSvc:  h.projectSvc,
		h:           h,
		e:           e,
	}
}

func seedProjectCapacityAPIProjectionFixture(tb testing.TB, db *sql.DB, projectCount int) {
	tb.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("begin project capacity fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM tasks"); err != nil {
		tb.Fatalf("clear tasks: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM projects"); err != nil {
		tb.Fatalf("clear projects: %v", err)
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
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatalf("prepare task fixture: %v", err)
	}
	defer taskStmt.Close()

	description := strings.Repeat("d", 16*1024)
	repoPath := strings.Repeat("p", 2*1024)
	repoURL := strings.Repeat("u", 2*1024)
	for i := 0; i < projectCount; i++ {
		projectID := fmt.Sprintf("capacity-api-project-%04d", i)
		var maxWorkers any
		switch i % 4 {
		case 1:
			maxWorkers = 3
		case 2:
			maxWorkers = 9
		case 3:
			maxWorkers = 0
		}
		if _, err := projectStmt.ExecContext(ctx, projectID, fmt.Sprintf("Capacity API Project %04d", i), description, repoPath, repoURL, i == 0, maxWorkers); err != nil {
			tb.Fatalf("insert project %d: %v", i, err)
		}

		status := "pending"
		if i%2 == 1 {
			status = "queued"
		}
		if _, err := taskStmt.ExecContext(ctx, fmt.Sprintf("capacity-api-pending-task-%04d", i), projectID, "capacity API pending task", "representative pending work", "active", status); err != nil {
			tb.Fatalf("insert pending task %d: %v", i, err)
		}
		if i%10 == 0 {
			if _, err := taskStmt.ExecContext(ctx, fmt.Sprintf("capacity-api-completed-task-%04d", i), projectID, "completed task", "completed work", "active", "completed"); err != nil {
				tb.Fatalf("insert completed task %d: %v", i, err)
			}
		}
		if i%15 == 0 {
			if _, err := taskStmt.ExecContext(ctx, fmt.Sprintf("capacity-api-backlog-task-%04d", i), projectID, "backlog task", "backlog work", "backlog", "pending"); err != nil {
				tb.Fatalf("insert backlog task %d: %v", i, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit project capacity fixture: %v", err)
	}
}

func (fixture *projectCapacityAPIProjectionFixture) renderCompactHandler(tb testing.TB) (string, error) {
	tb.Helper()
	rec := httptest.NewRecorder()
	fixture.e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/capacity/projects", nil))
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), nil
}

func (fixture *projectCapacityAPIProjectionFixture) renderFullRowBaseline(tb testing.TB) (string, error) {
	tb.Helper()
	ctx := context.Background()
	projects, err := fixture.projectSvc.List(ctx)
	if err != nil {
		return "", err
	}
	pendingCounts, err := fixture.h.taskRepo.CountPendingByProject(ctx)
	if err != nil {
		pendingCounts = make(map[string]int)
	}
	capacities := make([]ProjectCapacityResponse, len(projects))
	for i := range projects {
		capacities[i] = fixture.h.projectCapacityResponse(&projects[i], pendingCounts[projects[i].ID])
	}

	rec := httptest.NewRecorder()
	echoContext := fixture.e.NewContext(httptest.NewRequest(http.MethodGet, "/api/capacity/projects", nil), rec)
	if err := echoContext.JSON(http.StatusOK, capacities); err != nil {
		return "", err
	}
	return rec.Body.String(), nil
}

func (fixture *projectCapacityAPIProjectionFixture) measure(tb testing.TB, render func(testing.TB) (string, error), full bool) projectCapacityAPIProjectionMeasurement {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	for range 2 {
		if _, err := render(tb); err != nil {
			tb.Fatalf("warm render: %v", err)
		}
	}

	latencies := make([]time.Duration, 0, projectCapacityAPIProjectionSamples)
	allocatedBytes := make([]uint64, 0, projectCapacityAPIProjectionSamples)
	allocations := make([]uint64, 0, projectCapacityAPIProjectionSamples)
	responseBytes := make([]int, 0, projectCapacityAPIProjectionSamples)
	for range projectCapacityAPIProjectionSamples {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		startedAt := time.Now()
		body, err := render(tb)
		elapsed := time.Since(startedAt)
		runtime.ReadMemStats(&after)
		if err != nil {
			tb.Fatalf("render: %v", err)
		}
		latencies = append(latencies, elapsed)
		allocatedBytes = append(allocatedBytes, after.TotalAlloc-before.TotalAlloc)
		allocations = append(allocations, after.Mallocs-before.Mallocs)
		responseBytes = append(responseBytes, len(body))
	}

	measuredResponseBytes, sqlStatements := fixture.queryMetrics(tb, render)
	if len(responseBytes) == 0 || measuredResponseBytes != responseBytes[0] {
		tb.Fatalf("response size varied between measured samples: %v versus %d", responseBytes, measuredResponseBytes)
	}
	selectedProjectBytes := fixture.selectedProjectBytes(tb, full)
	slices.Sort(latencies)
	slices.Sort(allocatedBytes)
	slices.Sort(allocations)
	slices.Sort(responseBytes)
	return projectCapacityAPIProjectionMeasurement{
		medianLatency:        latencies[len(latencies)/2],
		p95Latency:           latencies[len(latencies)-1],
		allocatedBytes:       allocatedBytes[len(allocatedBytes)/2],
		allocations:          allocations[len(allocations)/2],
		selectedProjectBytes: selectedProjectBytes,
		responseBytes:        responseBytes[len(responseBytes)/2],
		sqlStatements:        sqlStatements,
	}
}

func (fixture *projectCapacityAPIProjectionFixture) queryMetrics(tb testing.TB, render func(testing.TB) (string, error)) (int, int) {
	tb.Helper()
	fixture.counter.SetEnabled(true)
	fixture.counter.Reset()
	body, err := render(tb)
	fixture.counter.SetEnabled(false)
	if err != nil {
		tb.Fatalf("query-metric render: %v", err)
	}
	return len(body), len(fixture.counter.Statements())
}

func (fixture *projectCapacityAPIProjectionFixture) selectedProjectBytes(tb testing.TB, full bool) int {
	tb.Helper()
	fixture.counter.SetEnabled(true)
	fixture.counter.Reset()
	ctx := context.Background()
	if full {
		if _, err := fixture.projectRepo.List(ctx); err != nil {
			tb.Fatalf("list full project rows for byte metric: %v", err)
		}
	} else {
		if _, err := fixture.projectRepo.ListWorkerCapacityProjects(ctx); err != nil {
			tb.Fatalf("list compact project rows for byte metric: %v", err)
		}
	}
	fixture.counter.SetEnabled(false)
	return fixture.counter.SelectedTextBytes()
}
