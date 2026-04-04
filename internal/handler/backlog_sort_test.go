package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestSetBacklogSort(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create some backlog tasks with different titles
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Zebra Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		Priority:  2,
	}
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Apple Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		Priority:  2,
	}
	task3 := &models.Task{
		ProjectID: "default",
		Title:     "Mango Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		Priority:  2,
	}

	if err := h.taskSvc.Create(ctx, task1); err != nil {
		t.Fatalf("Create task1: %v", err)
	}
	if err := h.taskSvc.Create(ctx, task2); err != nil {
		t.Fatalf("Create task2: %v", err)
	}
	if err := h.taskSvc.Create(ctx, task3); err != nil {
		t.Fatalf("Create task3: %v", err)
	}

	tests := []struct {
		name           string
		sortBy         string
		wantStatus     int
		wantCookie     bool
		wantCookieVal  string
	}{
		{
			name:          "title ascending",
			sortBy:        "title_asc",
			wantStatus:    http.StatusOK,
			wantCookie:    true,
			wantCookieVal: "title_asc",
		},
		{
			name:          "title descending",
			sortBy:        "title_desc",
			wantStatus:    http.StatusOK,
			wantCookie:    true,
			wantCookieVal: "title_desc",
		},
		{
			name:          "created ascending",
			sortBy:        "created_asc",
			wantStatus:    http.StatusOK,
			wantCookie:    true,
			wantCookieVal: "created_asc",
		},
		{
			name:          "created descending",
			sortBy:        "created_desc",
			wantStatus:    http.StatusOK,
			wantCookie:    true,
			wantCookieVal: "created_desc",
		},
		{
			name:          "priority ascending",
			sortBy:        "priority_asc",
			wantStatus:    http.StatusOK,
			wantCookie:    true,
			wantCookieVal: "priority_asc",
		},
		{
			name:          "priority descending",
			sortBy:        "priority_desc",
			wantStatus:    http.StatusOK,
			wantCookie:    true,
			wantCookieVal: "priority_desc",
		},
		{
			name:       "invalid sort",
			sortBy:     "invalid_sort",
			wantStatus: http.StatusBadRequest,
			wantCookie: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/tasks/backlog/sort?project_id=default&sort="+tt.sortBy, nil)
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()

			c := e.NewContext(req, rec)

			err := h.SetBacklogSort(c)

			if tt.wantStatus == http.StatusOK {
				if err != nil {
					t.Errorf("SetBacklogSort() error = %v", err)
				}
				if rec.Code != http.StatusOK {
					t.Errorf("SetBacklogSort() status = %d, want %d", rec.Code, http.StatusOK)
				}
			} else if tt.wantStatus == http.StatusBadRequest {
				if err == nil {
					t.Errorf("SetBacklogSort() expected error, got nil")
				}
			}

			if tt.wantCookie {
				cookies := rec.Result().Cookies()
				found := false
				for _, cookie := range cookies {
					if cookie.Name == "backlog_sort" {
						found = true
						if cookie.Value != tt.wantCookieVal {
							t.Errorf("SetBacklogSort() cookie value = %s, want %s", cookie.Value, tt.wantCookieVal)
						}
					}
				}
				if !found {
					t.Errorf("SetBacklogSort() cookie not set")
				}
			}
		})
	}
}

func TestSetBacklogSort_ActualSorting(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create tasks with different titles for alphabetical sorting test
	tasks := []struct {
		title    string
		priority int
	}{
		{"Zebra Task", 3},
		{"Apple Task", 1},
		{"Mango Task", 4},
		{"Banana Task", 2},
	}

	for _, task := range tasks {
		if err := h.taskSvc.Create(ctx, &models.Task{
			ProjectID: "default",
			Title:     task.title,
			Category:  models.CategoryBacklog,
			Status:    models.StatusPending,
			Prompt:    "test prompt",
			Priority:  task.priority,
		}); err != nil {
			t.Fatalf("Create task %s: %v", task.title, err)
		}
	}

	// Test title ascending sort
	req := httptest.NewRequest(http.MethodPost, "/tasks/backlog/sort?project_id=default&sort=title_asc", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.SetBacklogSort(c); err != nil {
		t.Fatalf("SetBacklogSort() error = %v", err)
	}

	// Verify the response contains sorted tasks
	body := rec.Body.String()

	// Check that Apple appears before Banana, Banana before Mango, Mango before Zebra in the HTML
	appleIdx := indexOf(body, "Apple Task")
	bananaIdx := indexOf(body, "Banana Task")
	mangoIdx := indexOf(body, "Mango Task")
	zebraIdx := indexOf(body, "Zebra Task")

	if appleIdx == -1 || bananaIdx == -1 || mangoIdx == -1 || zebraIdx == -1 {
		t.Fatalf("Not all tasks found in response")
	}

	if !(appleIdx < bananaIdx && bananaIdx < mangoIdx && mangoIdx < zebraIdx) {
		t.Errorf("Tasks not sorted correctly: Apple=%d, Banana=%d, Mango=%d, Zebra=%d", appleIdx, bananaIdx, mangoIdx, zebraIdx)
	}

	// Test priority descending sort
	req = httptest.NewRequest(http.MethodPost, "/tasks/backlog/sort?project_id=default&sort=priority_desc", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)

	if err := h.SetBacklogSort(c); err != nil {
		t.Fatalf("SetBacklogSort() error = %v", err)
	}

	body = rec.Body.String()

	// Check that Mango (4) appears before Zebra (3), Zebra before Banana (2), Banana before Apple (1)
	appleIdx = indexOf(body, "Apple Task")
	bananaIdx = indexOf(body, "Banana Task")
	mangoIdx = indexOf(body, "Mango Task")
	zebraIdx = indexOf(body, "Zebra Task")

	if !(mangoIdx < zebraIdx && zebraIdx < bananaIdx && bananaIdx < appleIdx) {
		t.Errorf("Tasks not sorted by priority correctly: Mango(4)=%d, Zebra(3)=%d, Banana(2)=%d, Apple(1)=%d", mangoIdx, zebraIdx, bananaIdx, appleIdx)
	}
}

func indexOf(s, substr string) int {
	idx := 0
	for {
		i := indexOfAfter(s[idx:], substr)
		if i == -1 {
			return -1
		}
		return idx + i
	}
}

func indexOfAfter(s, substr string) int {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
