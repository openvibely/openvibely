package handler

import (
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

const projectCapacityAPIProjectionSamples = 7

type projectCapacityAPIProjectionFixture struct {
	counter     storefixture.IOCounter
	projectRepo persistence.ProjectStore
	h           *Handler
	e           *echo.Echo
}

type projectCapacityAPIProjectionMeasurement struct {
	medianLatency        time.Duration
	p95Latency           time.Duration
	allocatedBytes       uint64
	allocations          uint64
	selectedProjectBytes int
	responseBody         string
	responseBytes        int
	sqlStatements        int
}

func TestGetProjectCapacitiesUsesCompactProjectionOnProductionFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-backed project capacity fixture in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newProjectCapacityAPIProjectionFixture(t, projectCount)
			responseBytes, sqlStatements := fixture.queryMetrics(t, fixture.renderCompactHandler)
			if sqlStatements != 2 {
				t.Fatalf("compact SQL statements = %d, want project list plus pending count", sqlStatements)
			}
			if responseBytes == 0 || fixture.selectedProjectBytes(t) == 0 {
				t.Fatal("compact capacity projection returned an empty response")
			}
		})
	}
}

func BenchmarkGetProjectCapacitiesProjection(b *testing.B) {
	for _, projectCount := range []int{1, 50, 500} {
		fixture := newProjectCapacityAPIProjectionFixture(b, projectCount)
		for _, tc := range []struct {
			name   string
			render func(testing.TB) (string, error)
		}{
			{name: "compact API handler", render: fixture.renderCompactHandler},
		} {
			b.Run(fmt.Sprintf("%d_projects/%s", projectCount, tc.name), func(b *testing.B) {
				body, err := tc.render(b)
				if err != nil {
					b.Fatalf("warm render: %v", err)
				}
				selectedProjectBytes := fixture.selectedProjectBytes(b)
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
	stores := teststores.New(tb, teststores.WithProductionTopology(), teststores.WithIOCounting())
	if err := stores.Fixtures.ClearProjects(tb.Context()); err != nil {
		tb.Fatal(err)
	}
	seedProjectCapacityAPIProjectionFixture(tb, stores, projectCount)
	env := wireTestHandler(tb, stores.Stores, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.workerSvc.SetProjectRepo(h.projectRepo)
	return &projectCapacityAPIProjectionFixture{counter: stores.IO, h: h, e: e, projectRepo: stores.Projects}
}
func seedProjectCapacityAPIProjectionFixture(tb testing.TB, stores *teststores.Env, projectCount int) {
	tb.Helper()
	ctx := tb.Context()
	var projects []models.Project
	for i := 0; i < projectCount; i++ {
		var maxWorkers *int
		switch i % 4 {
		case 1:
			v := 3
			maxWorkers = &v
		case 2:
			v := 9
			maxWorkers = &v
		case 3:
			v := 0
			maxWorkers = &v
		}
		projects = append(projects, models.Project{ID: fmt.Sprintf("capacity-api-project-%04d", i), Name: fmt.Sprintf("Capacity API Project %04d", i), Description: strings.Repeat("d", 16*1024), RepoPath: strings.Repeat("p", 2*1024), RepoURL: strings.Repeat("u", 2*1024), IsDefault: i == 0, MaxWorkers: maxWorkers})
	}
	if err := stores.Fixtures.SeedProjects(ctx, projects); err != nil {
		tb.Fatal(err)
	}
	for i, p := range projects {
		status := models.StatusPending
		if i%2 == 1 {
			status = models.StatusQueued
		}
		tasks := []models.Task{{ProjectID: p.ID, Title: "capacity API pending task", Prompt: "representative pending work", Category: models.CategoryActive, Status: status}}
		if i%10 == 0 {
			tasks = append(tasks, models.Task{ProjectID: p.ID, Title: "completed task", Prompt: "completed work", Category: models.CategoryActive, Status: models.StatusCompleted})
		}
		if i%15 == 0 {
			tasks = append(tasks, models.Task{ProjectID: p.ID, Title: "backlog task", Prompt: "backlog work", Category: models.CategoryBacklog, Status: models.StatusPending})
		}
		for _, task := range tasks {
			if err := stores.Tasks.Create(ctx, &task); err != nil {
				tb.Fatal(err)
			}
		}
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

func (fixture *projectCapacityAPIProjectionFixture) measure(tb testing.TB, render func(testing.TB) (string, error)) projectCapacityAPIProjectionMeasurement {
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
	responseBodies := make([]string, 0, projectCapacityAPIProjectionSamples)
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
		responseBodies = append(responseBodies, body)
	}

	measuredResponseBytes, sqlStatements := fixture.queryMetrics(tb, render)
	if len(responseBodies) == 0 {
		tb.Fatalf("no response samples collected")
	}
	for i, body := range responseBodies[1:] {
		if body != responseBodies[0] {
			tb.Fatalf("response bytes varied between measured samples at sample %d", i+1)
		}
	}
	if measuredResponseBytes != len(responseBodies[0]) {
		tb.Fatalf("response size varied between measured samples: %d versus %d", len(responseBodies[0]), measuredResponseBytes)
	}
	selectedProjectBytes := fixture.selectedProjectBytes(tb)
	slices.Sort(latencies)
	slices.Sort(allocatedBytes)
	slices.Sort(allocations)
	responseBody := responseBodies[0]
	return projectCapacityAPIProjectionMeasurement{
		medianLatency:        latencies[len(latencies)/2],
		p95Latency:           latencies[len(latencies)-1],
		allocatedBytes:       allocatedBytes[len(allocatedBytes)/2],
		allocations:          allocations[len(allocations)/2],
		selectedProjectBytes: selectedProjectBytes,
		responseBody:         responseBody,
		responseBytes:        len(responseBody),
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
	return len(body), fixture.counter.Snapshot().Requests
}

func (fixture *projectCapacityAPIProjectionFixture) selectedProjectBytes(tb testing.TB) int {
	tb.Helper()
	fixture.counter.SetEnabled(true)
	defer fixture.counter.SetEnabled(false)
	fixture.counter.Reset()
	ctx := context.Background()
	if _, err := fixture.projectRepo.ListWorkerCapacityProjects(ctx); err != nil {
		tb.Fatalf("list compact project rows for byte metric: %v", err)
	}
	return fixture.counter.Snapshot().ReadTextBytes
}
