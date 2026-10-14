package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

const workerCapacityProjectionSamples = 5

type workerCapacityProjectionFixture struct {
	h           *Handler
	e           http.Handler
	projectRepo persistence.ProjectStore
}

type workerCapacityProjectionMeasurement struct {
	latency                time.Duration
	allocatedBytes         uint64
	allocations            uint64
	fragmentBytes          int
	compactProjectRowBytes int
	sqlStatementCount      int
}

func TestHandlerProjectWorkerCapacityUsesCompactProductionProjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping file-backed Workers capacity fixture in short mode")
	}

	for _, projectCount := range []int{1, 50, 500} {
		t.Run(fmt.Sprintf("%d projects", projectCount), func(t *testing.T) {
			fixture := newWorkerCapacityProjectionFixture(t, projectCount)
			fragment, err := fixture.renderCompactHandler(t)
			if err != nil {
				t.Fatal(err)
			}
			projects, err := fixture.projectRepo.ListWorkerCapacityProjects(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if fragment == "" || projectWorkerRowBytes(projects) == 0 {
				t.Fatal("compact worker capacity projection returned empty output")
			}
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
	stores := teststores.New(tb, teststores.WithProductionTopology(), teststores.WithIOCounting())
	if err := stores.Fixtures.ClearProjects(tb.Context()); err != nil {
		tb.Fatal(err)
	}
	seedWorkerCapacityProjectionFixture(tb, stores, projectCount)
	env := wireTestHandler(tb, stores.Stores, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.workerSvc.SetProjectRepo(h.projectRepo)
	return &workerCapacityProjectionFixture{h: h, e: e, projectRepo: stores.Projects}
}
func seedWorkerCapacityProjectionFixture(tb testing.TB, stores *teststores.Env, projectCount int) {
	tb.Helper()
	ctx := tb.Context()
	var projects []models.Project
	for i := 0; i < projectCount; i++ {
		var maxWorkers *int
		switch i % 3 {
		case 1:
			v := 3
			maxWorkers = &v
		case 2:
			v := 9
			maxWorkers = &v
		}
		projects = append(projects, models.Project{ID: fmt.Sprintf("worker-capacity-project-%04d", i), Name: fmt.Sprintf("Workers Capacity Project %04d", i), Description: strings.Repeat("production-shaped Workers project description ", 384), RepoPath: "/private/workspaces/" + strings.Repeat("nested-local-repository/", 96), RepoURL: "https://github.example.test/openvibely/" + strings.Repeat("workers-capacity-repository/", 96), IsDefault: i == 0, MaxWorkers: maxWorkers})
	}
	if err := stores.Fixtures.SeedProjects(ctx, projects); err != nil {
		tb.Fatal(err)
	}
	for i, p := range projects {
		status := models.StatusPending
		if i%2 == 1 {
			status = models.StatusQueued
		}
		tasks := []models.Task{{ProjectID: p.ID, Title: "Workers capacity pending task", Prompt: "representative queued work", Category: models.CategoryActive, Status: status}}

		for _, task := range tasks {
			if err := stores.Tasks.Create(ctx, &task); err != nil {
				tb.Fatal(err)
			}
		}
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
	capacityProjects, err := fixture.projectRepo.ListWorkerCapacityProjects(context.Background())
	if err != nil {
		t.Fatalf("list compact project rows: %v", err)
	}

	compactProjectRowBytes := projectWorkerRowBytes(capacityProjects)

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

func projectWorkerRowBytes(compact []models.ProjectWorkerCapacity) (compactBytes int) {
	for _, project := range compact {
		compactBytes += len(project.ID) + len(project.Name)
		if project.MaxWorkers != nil {
			compactBytes += 8
		}
	}
	return compactBytes
}
