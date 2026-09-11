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
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

const apiProjectProjectionSamples = 5

type apiProjectProjectionFixture struct {
	db          *sql.DB
	counter     *testutil.SQLStatementCounter
	projectRepo *repository.ProjectRepo
	projectSvc  *service.ProjectService
	h           *Handler
	e           *echo.Echo
}

type apiProjectProjectionMeasurement struct {
	latency          time.Duration
	allocatedBytes   uint64
	allocations      uint64
	selectedRowBytes int
	responseBytes    int
	sqlStatements    int
}

func TestAPIGetProjectsProjectionProductionPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped API project projection measurements in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newAPIProjectProjectionFixture(t, projectCount)
			full := fixture.measure(t, fixture.renderFullRowBaseline, true)
			compact := fixture.measure(t, fixture.renderCompactHandler, false)

			if compact.responseBytes != full.responseBytes {
				t.Fatalf("compact JSON response bytes = %d, full-row baseline = %d", compact.responseBytes, full.responseBytes)
			}
			if compact.sqlStatements != 1 || full.sqlStatements != 1 {
				t.Fatalf("SQL statements: compact=%d full=%d, want one each", compact.sqlStatements, full.sqlStatements)
			}
			if projectCount == 500 {
				if compact.selectedRowBytes*10 > full.selectedRowBytes {
					t.Fatalf("compact selected-row bytes = %d, want at least 90%% below full-row %d", compact.selectedRowBytes, full.selectedRowBytes)
				}
				if compact.allocatedBytes*10 > full.allocatedBytes {
					t.Fatalf("compact Go allocation bytes = %d, want at least 90%% below full-row %d", compact.allocatedBytes, full.allocatedBytes)
				}
				if compact.latency >= full.latency {
					t.Fatalf("compact median latency = %s, want lower than full-row %s", compact.latency, full.latency)
				}
			}

			t.Logf("%d projects median: full=%s/%d B/op/%d allocs/op/%d selected-row bytes/%d JSON bytes/%d SQL statements; compact=%s/%d B/op/%d allocs/op/%d selected-row bytes/%d JSON bytes/%d SQL statements",
				projectCount,
				full.latency, full.allocatedBytes, full.allocations, full.selectedRowBytes, full.responseBytes, full.sqlStatements,
				compact.latency, compact.allocatedBytes, compact.allocations, compact.selectedRowBytes, compact.responseBytes, compact.sqlStatements,
			)
		})
	}
}

func BenchmarkAPIGetProjectsProjection(b *testing.B) {
	for _, projectCount := range []int{1, 50, 500} {
		fixture := newAPIProjectProjectionFixture(b, projectCount)
		for _, tc := range []struct {
			name   string
			render func(testing.TB) (string, error)
			full   bool
		}{
			{name: "full-row baseline", render: fixture.renderFullRowBaseline, full: true},
			{name: "compact API handler", render: fixture.renderCompactHandler, full: false},
		} {
			b.Run(fmt.Sprintf("%d_projects/%s", projectCount, tc.name), func(b *testing.B) {
				if _, err := tc.render(b); err != nil {
					b.Fatalf("warm render: %v", err)
				}
				selectedRowBytes := fixture.selectedRowBytes(b, tc.full)
				responseBytes, sqlStatements := fixture.queryMetrics(b, tc.render)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := tc.render(b); err != nil {
						b.Fatalf("render: %v", err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(selectedRowBytes), "selected_row_bytes")
				b.ReportMetric(float64(responseBytes), "json_response_bytes")
				b.ReportMetric(float64(sqlStatements), "sql_statements")
			})
		}
	}
}

func newAPIProjectProjectionFixture(tb testing.TB, projectCount int) *apiProjectProjectionFixture {
	tb.Helper()
	db, counter := testutil.NewStatementCountingTestDB(tb)
	seedAPIProjectionProjects(tb, db, projectCount)
	repo := repository.NewProjectRepo(db)
	projectSvc := service.NewProjectService(repo)
	return &apiProjectProjectionFixture{
		db:          db,
		counter:     counter,
		projectRepo: repo,
		projectSvc:  projectSvc,
		h:           &Handler{projectSvc: projectSvc},
		e:           echo.New(),
	}
}

func seedAPIProjectionProjects(tb testing.TB, db *sql.DB, projectCount int) {
	tb.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("begin API project fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		tb.Fatalf("clear projects: %v", err)
	}
	largeDescription := strings.Repeat("production-shaped project description ", 4096)
	largePath := "/private/workspaces/" + strings.Repeat("nested-local-repository/", 64)
	largeURL := "https://github.example.test/openvibely/" + strings.Repeat("repository-configuration/", 64)
	for i := 0; i < projectCount; i++ {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("api-projection-project-%04d", i), fmt.Sprintf("API Projection Project %04d", i),
			largeDescription, largePath, largeURL, i == 0); err != nil {
			tb.Fatalf("insert API project %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit API project fixture: %v", err)
	}
}

func (fixture *apiProjectProjectionFixture) renderCompactHandler(tb testing.TB) (string, error) {
	tb.Helper()
	rec := httptest.NewRecorder()
	ctx := fixture.e.NewContext(httptest.NewRequest(http.MethodGet, "/api/projects", nil), rec)
	if err := fixture.h.APIGetProjects(ctx); err != nil {
		return "", err
	}
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), nil
}

func (fixture *apiProjectProjectionFixture) renderFullRowBaseline(tb testing.TB) (string, error) {
	tb.Helper()
	projects, err := fixture.projectSvc.List(context.Background())
	if err != nil {
		return "", err
	}
	rec := httptest.NewRecorder()
	ctx := fixture.e.NewContext(httptest.NewRequest(http.MethodGet, "/api/projects", nil), rec)
	if err := writeAPIProjectsResponse(ctx, projectResponsesFromFullProjects(projects)); err != nil {
		return "", err
	}
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), nil
}

func (fixture *apiProjectProjectionFixture) measure(tb testing.TB, render func(testing.TB) (string, error), full bool) apiProjectProjectionMeasurement {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	for range 2 {
		if _, err := render(tb); err != nil {
			tb.Fatalf("warm render: %v", err)
		}
	}

	latencies := make([]time.Duration, 0, apiProjectProjectionSamples)
	allocatedBytes := make([]uint64, 0, apiProjectProjectionSamples)
	allocations := make([]uint64, 0, apiProjectProjectionSamples)
	responseBytes := make([]int, 0, apiProjectProjectionSamples)
	for range apiProjectProjectionSamples {
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
	slices.Sort(latencies)
	slices.Sort(allocatedBytes)
	slices.Sort(allocations)
	slices.Sort(responseBytes)
	middle := apiProjectProjectionSamples / 2
	return apiProjectProjectionMeasurement{
		latency:          latencies[middle],
		allocatedBytes:   allocatedBytes[middle],
		allocations:      allocations[middle],
		selectedRowBytes: fixture.selectedRowBytes(tb, full),
		responseBytes:    responseBytes[middle],
		sqlStatements:    sqlStatements,
	}
}

func (fixture *apiProjectProjectionFixture) queryMetrics(tb testing.TB, render func(testing.TB) (string, error)) (int, int) {
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

func (fixture *apiProjectProjectionFixture) selectedRowBytes(tb testing.TB, full bool) int {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	if full {
		projects, err := fixture.projectRepo.List(context.Background())
		if err != nil {
			tb.Fatalf("full project rows for byte metric: %v", err)
		}
		return fullProjectSelectedRowBytes(projects)
	}
	projects, err := fixture.projectRepo.ListAPIProjects(context.Background())
	if err != nil {
		tb.Fatalf("compact project rows for byte metric: %v", err)
	}
	return compactProjectSelectedRowBytes(projects)
}

func fullProjectSelectedRowBytes(projects []models.Project) int {
	var total int
	for _, project := range projects {
		total += len(project.ID) + len(project.Name) + len(project.Description) + len(project.RepoPath) + len(project.RepoURL)
		total++ // is_default
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

func compactProjectSelectedRowBytes(projects []models.ProjectAPIItem) int {
	var total int
	for _, project := range projects {
		total += len(project.ID) + len(project.Name) + len(project.RepoPath) + len(project.CreatedAt.Format(time.RFC3339))
	}
	return total
}
