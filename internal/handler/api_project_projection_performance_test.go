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

func TestAPIGetProjectsUsesCompactProjectionOnProductionFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped API project fixture in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newAPIProjectProjectionFixture(t, projectCount)
			responseBytes, sqlStatements := fixture.queryMetrics(t, fixture.renderCompactHandler)
			if sqlStatements != 1 {
				t.Fatalf("compact SQL statements = %d, want one", sqlStatements)
			}
			if responseBytes == 0 || fixture.selectedRowBytes(t) == 0 {
				t.Fatal("compact projection returned an empty response")
			}
		})
	}
}

func BenchmarkAPIGetProjectsProjection(b *testing.B) {
	for _, projectCount := range []int{1, 50, 500} {
		fixture := newAPIProjectProjectionFixture(b, projectCount)
		for _, tc := range []struct {
			name   string
			render func(testing.TB) (string, error)
		}{
			{name: "compact API handler", render: fixture.renderCompactHandler},
		} {
			b.Run(fmt.Sprintf("%d_projects/%s", projectCount, tc.name), func(b *testing.B) {
				if _, err := tc.render(b); err != nil {
					b.Fatalf("warm render: %v", err)
				}
				selectedRowBytes := fixture.selectedRowBytes(b)
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

func (fixture *apiProjectProjectionFixture) measure(tb testing.TB, render func(testing.TB) (string, error)) apiProjectProjectionMeasurement {
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
		selectedRowBytes: fixture.selectedRowBytes(tb),
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

func (fixture *apiProjectProjectionFixture) selectedRowBytes(tb testing.TB) int {
	tb.Helper()
	fixture.counter.SetEnabled(false)
	projects, err := fixture.projectRepo.ListAPIProjects(context.Background())
	if err != nil {
		tb.Fatalf("compact project rows for byte metric: %v", err)
	}
	return compactProjectSelectedRowBytes(projects)
}

func compactProjectSelectedRowBytes(projects []models.ProjectAPIItem) int {
	var total int
	for _, project := range projects {
		total += len(project.ID) + len(project.Name) + len(project.RepoPath) + len(project.CreatedAt.Format(time.RFC3339))
	}
	return total
}
