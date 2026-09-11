package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// Worker capacity polls read the compact project list, pending task counts, and
// global worker settings once each.
const workerCapacityPollSQLStatementCount = 3

// TestHandlerWorkerCapacityRowsUseCompactProjectProjection exercises every
// Workers render that constructs capacity rows. Large project-only metadata
// must remain available to detail reads without being loaded for this table.
func TestHandlerWorkerCapacityRowsUseCompactProjectProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, _ := setupTestHandlerForDB(t, db)
	ctx := context.Background()

	limit := 3
	project := &models.Project{
		Name:        "Workers Capacity Projection",
		Description: strings.Repeat("description ", 4096),
		RepoPath:    "/private/workspaces/" + strings.Repeat("local-path/", 512),
		RepoURL:     "https://github.example.test/" + strings.Repeat("repository/", 512),
		MaxWorkers:  &limit,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	requests := []struct {
		name string
		run  func() *httptest.ResponseRecorder
	}{
		{
			name: "three-second poll",
			run: func() *httptest.ResponseRecorder {
				return htmxGet(e, "/workers/stats/projects")
			},
		},
		{
			name: "initial Workers page",
			run: func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workers", nil))
				return rec
			},
		},
		{
			name: "global worker limit refresh",
			run: func() *httptest.ResponseRecorder {
				return htmxPost(e, "/workers", url.Values{"max_workers": {"4"}})
			},
		},
		{
			name: "project worker limit refresh",
			run: func() *httptest.ResponseRecorder {
				return htmxPost(e, "/workers/projects/"+project.ID+"/limit", url.Values{"max_workers": {"2"}})
			},
		},
	}

	for _, tc := range requests {
		t.Run(tc.name, func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			resp := tc.run()
			counter.SetEnabled(false)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
			}
			assertWorkerCapacityProjectProjection(t, counter)
		})
	}

	counter.Reset()
	counter.SetEnabled(true)
	fullTable, err := renderFullProjectWorkerStats(ctx, h)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("render full-row baseline: %v", err)
	}
	fullStatementCount := len(counter.Statements())
	if fullStatementCount != workerCapacityPollSQLStatementCount {
		t.Fatalf("full-row baseline statements = %d, want %d", fullStatementCount, workerCapacityPollSQLStatementCount)
	}

	counter.Reset()
	counter.SetEnabled(true)
	compactTable := htmxGet(e, "/workers/stats/projects")
	counter.SetEnabled(false)
	if compactTable.Code != http.StatusOK {
		t.Fatalf("compact poll status = %d, want %d", compactTable.Code, http.StatusOK)
	}
	if compactTable.Body.String() != fullTable {
		t.Fatalf("compact poll fragment changed:\ncompact=%s\nfull=%s", compactTable.Body.String(), fullTable)
	}
	if compactStatementCount := len(counter.Statements()); compactStatementCount != fullStatementCount {
		t.Fatalf("compact poll statements = %d, full-row baseline statements = %d", compactStatementCount, fullStatementCount)
	}
	assertWorkerCapacityProjectProjection(t, counter)

	full, err := h.projectSvc.GetByID(ctx, project.ID)
	if err != nil || full == nil {
		t.Fatalf("get full project: project=%v err=%v", full, err)
	}
	if full.Description != project.Description || full.RepoPath != project.RepoPath || full.RepoURL != project.RepoURL {
		t.Fatal("full project detail no longer retains metadata omitted from capacity rows")
	}
}

func renderFullProjectWorkerStats(ctx context.Context, h *Handler) (string, error) {
	projects, err := h.projectSvc.List(ctx)
	if err != nil {
		return "", err
	}
	pendingCounts, err := h.taskRepo.CountPendingByProject(ctx)
	if err != nil {
		pendingCounts = make(map[string]int)
	}
	projectStats := make([]pages.ProjectWorkerStats, len(projects))
	for i, project := range projects {
		projectStats[i] = pages.ProjectWorkerStats{
			ID:         project.ID,
			Name:       project.Name,
			Running:    h.workerSvc.ProjectRunning(project.ID),
			QueueSize:  pendingCounts[project.ID],
			MaxWorkers: project.MaxWorkers,
		}
	}
	maxWorkers, _ := h.workerRepo.GetMaxWorkers(ctx)
	var body bytes.Buffer
	if err := pages.ProjectStatsTableBody(maxWorkers, h.workerSvc.NumWorkers(), h.workerSvc.TotalRunning(), h.workerSvc.QueueSize(), projectStats).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func assertWorkerCapacityProjectProjection(t *testing.T, counter *testutil.SQLStatementCounter) {
	t.Helper()
	const compact = "select id, name, max_workers from projects order by is_default desc, name asc"
	const selector = "select id, name, is_default from projects order by is_default desc, name asc, id asc"

	compactCount := 0
	for _, statement := range counter.Statements() {
		query := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if !strings.Contains(query, "from projects") || !strings.Contains(query, "order by is_default desc, name asc") {
			continue
		}
		switch query {
		case compact:
			compactCount++
		case selector:
			// The initial full-page render separately loads the sidebar selector.
		default:
			t.Fatalf("Workers capacity render used an unexpected ordered project query: %q", query)
		}
	}
	if compactCount != 1 {
		t.Fatalf("Workers capacity render used %d compact project queries, want 1; statements: %q", compactCount, counter.Statements())
	}
}
