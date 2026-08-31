package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

func TestSetCompletedSort(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	seedCompletedSortTasks(t, h, ctx)

	tests := []struct {
		name          string
		sortBy        string
		wantStatus    int
		wantCookie    bool
		wantCookieVal string
	}{
		{name: "title ascending", sortBy: "title_asc", wantStatus: http.StatusOK, wantCookie: true, wantCookieVal: "title_asc"},
		{name: "title descending", sortBy: "title_desc", wantStatus: http.StatusOK, wantCookie: true, wantCookieVal: "title_desc"},
		{name: "completed ascending", sortBy: "completed_asc", wantStatus: http.StatusOK, wantCookie: true, wantCookieVal: "completed_asc"},
		{name: "completed descending", sortBy: "completed_desc", wantStatus: http.StatusOK, wantCookie: true, wantCookieVal: "completed_desc"},
		{name: "priority ascending", sortBy: "priority_asc", wantStatus: http.StatusOK, wantCookie: true, wantCookieVal: "priority_asc"},
		{name: "priority descending", sortBy: "priority_desc", wantStatus: http.StatusOK, wantCookie: true, wantCookieVal: "priority_desc"},
		{name: "invalid sort", sortBy: "invalid_sort", wantStatus: http.StatusBadRequest, wantCookie: false},
		// created_asc/created_desc are backlog-only keys and must be rejected by the completed endpoint
		{name: "created_asc rejected", sortBy: "created_asc", wantStatus: http.StatusBadRequest, wantCookie: false},
		{name: "created_desc rejected", sortBy: "created_desc", wantStatus: http.StatusBadRequest, wantCookie: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/tasks/completed/sort?project_id=default&sort="+tt.sortBy, nil)
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := h.SetCompletedSort(c)

			if tt.wantStatus == http.StatusOK {
				if err != nil {
					t.Fatalf("SetCompletedSort() error = %v", err)
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("SetCompletedSort() status = %d, want %d", rec.Code, http.StatusOK)
				}
			} else {
				if err == nil {
					t.Fatalf("SetCompletedSort() expected error, got nil")
				}
			}

			if tt.wantCookie {
				found := false
				for _, cookie := range rec.Result().Cookies() {
					if cookie.Name == completedSortCookieName {
						found = true
						if cookie.Value != tt.wantCookieVal {
							t.Fatalf("cookie value = %s, want %s", cookie.Value, tt.wantCookieVal)
						}
					}
				}
				if !found {
					t.Fatal("completed_sort cookie not set")
				}
			}
		})
	}
}

func TestSetCompletedSort_ActualSorting(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	for _, task := range []struct {
		title    string
		priority int
	}{
		{"Zulu Done", 2},
		{"Alpha Done", 4},
		{"Mike Done", 1},
	} {
		if err := h.taskSvc.Create(ctx, &models.Task{
			ProjectID: "default",
			Title:     task.title,
			Category:  models.CategoryCompleted,
			Status:    models.StatusCompleted,
			Prompt:    "test prompt",
			Priority:  task.priority,
		}); err != nil {
			t.Fatalf("Create completed task %s: %v", task.title, err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/completed/sort?project_id=default&sort=title_asc", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.SetCompletedSort(c); err != nil {
		t.Fatalf("SetCompletedSort() error = %v", err)
	}

	completed := completedDropZone(rec.Body.String())
	alphaIdx := strings.Index(completed, "Alpha Done")
	mikeIdx := strings.Index(completed, "Mike Done")
	zuluIdx := strings.Index(completed, "Zulu Done")
	if alphaIdx == -1 || mikeIdx == -1 || zuluIdx == -1 {
		t.Fatalf("expected completed tasks in response; got section: %s", completed)
	}
	if !(alphaIdx < mikeIdx && mikeIdx < zuluIdx) {
		t.Fatalf("completed tasks not sorted by title asc: alpha=%d mike=%d zulu=%d", alphaIdx, mikeIdx, zuluIdx)
	}
}

func TestCompletedSort_SortsByCompletionDate(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()

	// Create three tasks in the completed category.  They will all get the same
	// created_at because SQLite datetime('now') has second precision in tests.
	// We set distinct completed_at values (the authoritative completion timestamp)
	// and a deliberately different updated_at to confirm the sort uses completed_at,
	// not updated_at.
	type taskSpec struct {
		title       string
		completedAt string // explicit completion timestamp (oldest → newest)
		updatedAt   string // intentionally different from completedAt to verify field
	}
	specs := []taskSpec{
		{"MiddleCompleted", "2024-01-02 10:00:00", "2024-06-01 10:00:00"},
		{"OldestCompleted", "2024-01-01 10:00:00", "2024-06-02 10:00:00"},
		{"NewestCompleted", "2024-01-03 10:00:00", "2024-06-03 10:00:00"},
	}
	for _, s := range specs {
		task := &models.Task{
			ProjectID: "default",
			Title:     s.title,
			Category:  models.CategoryCompleted,
			Status:    models.StatusCompleted,
			Prompt:    "test prompt",
			Priority:  2,
		}
		if err := h.taskSvc.Create(ctx, task); err != nil {
			t.Fatalf("Create task %s: %v", s.title, err)
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE tasks SET completed_at = ?, updated_at = ? WHERE id = ?`,
			s.completedAt, s.updatedAt, task.ID); err != nil {
			t.Fatalf("Set timestamps for %s: %v", s.title, err)
		}
	}

	assertOrder := func(sortKey string, wantOrder []string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/tasks/completed/sort?project_id=default&sort="+sortKey, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.SetCompletedSort(c); err != nil {
			t.Fatalf("SetCompletedSort(%s) error = %v", sortKey, err)
		}

		body := completedDropZone(rec.Body.String())
		positions := make([]int, len(wantOrder))
		for i, name := range wantOrder {
			positions[i] = strings.Index(body, name)
			if positions[i] == -1 {
				t.Fatalf("sort=%s: %q not found in completed dropzone", sortKey, name)
			}
		}
		for i := 1; i < len(positions); i++ {
			if positions[i-1] >= positions[i] {
				t.Fatalf("sort=%s: want %q before %q, got positions %v",
					sortKey, wantOrder[i-1], wantOrder[i], positions)
			}
		}
	}

	// completed_desc: newest completion first
	assertOrder("completed_desc", []string{"NewestCompleted", "MiddleCompleted", "OldestCompleted"})
	// completed_asc: oldest completion first
	assertOrder("completed_asc", []string{"OldestCompleted", "MiddleCompleted", "NewestCompleted"})
}

func TestCompletedSort_UpdatesWhenTasksCompletedAndUncompleted(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	alpha := &models.Task{
		ProjectID: "default",
		Title:     "Alpha Move",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		Priority:  2,
	}
	zulu := &models.Task{
		ProjectID: "default",
		Title:     "Zulu Move",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		Priority:  2,
	}

	if err := h.taskSvc.Create(ctx, zulu); err != nil {
		t.Fatalf("Create zulu: %v", err)
	}
	if err := h.taskSvc.Create(ctx, alpha); err != nil {
		t.Fatalf("Create alpha: %v", err)
	}

	moveTaskToCategory(t, h, e, zulu.ID, "completed", "")
	bodyAfterSecondComplete := moveTaskToCategory(t, h, e, alpha.ID, "completed", "")

	completedAfterComplete := completedDropZone(bodyAfterSecondComplete)
	alphaIdx := strings.Index(completedAfterComplete, "Alpha Move")
	zuluIdx := strings.Index(completedAfterComplete, "Zulu Move")
	if alphaIdx == -1 || zuluIdx == -1 {
		t.Fatalf("expected both tasks in completed zone after completion: %s", completedAfterComplete)
	}
	if alphaIdx > zuluIdx {
		t.Fatalf("completed zone order incorrect after completion: alpha=%d zulu=%d", alphaIdx, zuluIdx)
	}

	bodyAfterUncomplete := moveTaskToCategory(t, h, e, alpha.ID, "backlog", "")
	completedAfterUncomplete := completedDropZone(bodyAfterUncomplete)
	if strings.Contains(completedAfterUncomplete, "Alpha Move") {
		t.Fatalf("Alpha Move should not remain in completed zone after uncomplete: %s", completedAfterUncomplete)
	}
	if !strings.Contains(completedAfterUncomplete, "Zulu Move") {
		t.Fatalf("Zulu Move should remain in completed zone after uncomplete: %s", completedAfterUncomplete)
	}
}

// TestGetCompletedSortPreference_MigratesStaleCreatedCookie verifies that
// legacy cookies written with created_asc/created_desc by older code are
// transparently migrated to the completed_* equivalents without requiring
// the user to click a sort button.
func TestGetCompletedSortPreference_MigratesStaleCreatedCookie(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	cases := []struct {
		cookieVal string
		wantVal   string
	}{
		{"created_asc", "completed_asc"},
		{"created_desc", "completed_desc"},
		{"completed_asc", "completed_asc"},
		{"completed_desc", "completed_desc"},
		{"title_asc", "title_asc"},
		{"", ""},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.cookieVal != "" {
			req.AddCookie(&http.Cookie{Name: completedSortCookieName, Value: tc.cookieVal})
		}
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		got := getCompletedSortPreference(c)
		if got != tc.wantVal {
			t.Errorf("getCompletedSortPreference(cookie=%q) = %q, want %q", tc.cookieVal, got, tc.wantVal)
		}
	}
}

func seedCompletedSortTasks(t *testing.T, h *Handler, ctx context.Context) {
	t.Helper()
	for _, title := range []string{"One Done", "Two Done", "Three Done"} {
		if err := h.taskSvc.Create(ctx, &models.Task{
			ProjectID: "default",
			Title:     title,
			Category:  models.CategoryCompleted,
			Status:    models.StatusCompleted,
			Prompt:    "test prompt",
			Priority:  2,
		}); err != nil {
			t.Fatalf("Create task %s: %v", title, err)
		}
	}
}

func moveTaskToCategory(t *testing.T, h *Handler, e *echo.Echo, taskID string, category string, completedSort string) string {
	t.Helper()
	form := url.Values{}
	form.Set("category", category)
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+taskID+"/category", strings.NewReader(form.Encode()))
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if completedSort != "" {
		req.AddCookie(&http.Cookie{Name: completedSortCookieName, Value: completedSort})
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:taskId/category")
	c.SetParamNames("taskId")
	c.SetParamValues(taskID)

	if err := h.UpdateTaskCategory(c); err != nil {
		t.Fatalf("UpdateTaskCategory(%s -> %s): %v", taskID, category, err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("UpdateTaskCategory(%s -> %s) status = %d", taskID, category, rec.Code)
	}

	return rec.Body.String()
}

func completedDropZone(body string) string {
	const marker = `data-category="completed"`
	idx := strings.Index(body, marker)
	if idx == -1 {
		return ""
	}
	return body[idx:]
}
