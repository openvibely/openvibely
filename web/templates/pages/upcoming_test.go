package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestUpcomingTaskTagsMatchTaskCardBadgeMapping(t *testing.T) {
	tests := []struct {
		name string
		tag  models.TaskTag
	}{
		{name: "feature", tag: models.TagFeature},
		{name: "bug", tag: models.TagBug},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := models.Task{
				ID:        "task-" + tt.name,
				ProjectID: "project-1",
				Title:     "Tagged " + tt.name,
				Category:  models.CategoryActive,
				Status:    models.StatusRunning,
				Tag:       tt.tag,
			}

			var taskCard bytes.Buffer
			if err := components.TaskCard(task, "project-1", "active", nil, nil).Render(context.Background(), &taskCard); err != nil {
				t.Fatalf("render task card: %v", err)
			}

			upcoming := &models.Upcoming{
				GeneratedAt:  time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
				RunningTasks: []models.UpcomingTask{{Task: task, AgentName: "Test Agent"}},
			}
			var pulse bytes.Buffer
			if err := UpcomingContent(upcoming, "project-1").Render(context.Background(), &pulse); err != nil {
				t.Fatalf("render upcoming content: %v", err)
			}

			label := components.TagLabel(tt.tag)
			class := components.TagBadgeClass(tt.tag)
			assertRenderedTagBadge(t, taskCard.String(), label, class)
			assertRenderedTagBadge(t, pulse.String(), label, class)
			if strings.Contains(pulse.String(), ">"+string(tt.tag)+"</span>") {
				t.Fatalf("pulse should render shared display label %q instead of raw tag value %q: %s", label, string(tt.tag), pulse.String())
			}
		})
	}
}

func TestUpcomingContentRendersQueuedTaskAsQueued(t *testing.T) {
	upcoming := &models.Upcoming{
		GeneratedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		WaitingTasks: []models.UpcomingTask{{
			Task: models.Task{
				ID:        "queued-task",
				ProjectID: "project-1",
				Title:     "Queued follow-up",
				Category:  models.CategoryActive,
				Status:    models.StatusQueued,
			},
			AgentName: "Test Agent",
		}},
		QueuedTasks: []models.UpcomingTask{{
			Task: models.Task{
				ID:        "queued-task",
				ProjectID: "project-1",
				Title:     "Queued follow-up",
				Category:  models.CategoryActive,
				Status:    models.StatusQueued,
			},
			AgentName: "Test Agent",
		}},
	}

	var pulse bytes.Buffer
	if err := UpcomingContent(upcoming, "project-1").Render(context.Background(), &pulse); err != nil {
		t.Fatalf("render upcoming content: %v", err)
	}
	body := pulse.String()
	if !strings.Contains(body, ">Queued</span>") {
		t.Fatalf("queued task must expose its persisted queued status: %s", body)
	}
	if !strings.Contains(body, "0 pending · 1 queued") {
		t.Fatalf("queued-only agenda must expose a truthful waiting breakdown: %s", body)
	}
	if strings.Contains(body, "No active work right now") {
		t.Fatalf("queued-only agenda must not render the empty state: %s", body)
	}
}

func TestUpcomingContentRendersBlockedTaskAsDependencyWaiting(t *testing.T) {
	upcoming := &models.Upcoming{
		GeneratedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		BlockedTasks: []models.UpcomingTask{{
			Task: models.Task{
				ID:        "blocked-task",
				ProjectID: "project-1",
				Title:     "Waiting chain child",
				Category:  models.CategoryBacklog,
				Status:    models.StatusBlocked,
				Priority:  4,
			},
			AgentName: "Test Agent",
		}},
		TaskSummary: &models.TaskSummary{BlockedCount: 1, TotalPending: 1},
	}

	var pulse bytes.Buffer
	if err := UpcomingContent(upcoming, "project-1").Render(context.Background(), &pulse); err != nil {
		t.Fatalf("render upcoming content: %v", err)
	}
	body := pulse.String()
	for _, want := range []string{"Waiting for Parent", "Waiting chain child", "P4", "Test Agent", "unfinished"} {
		if !strings.Contains(body, want) {
			t.Fatalf("blocked pulse missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `data-upcoming-stop`) {
		t.Fatalf("blocked card must not expose a stop control: %s", body)
	}
	if !strings.Contains(body, `data-upcoming-task-id="blocked-task"`) {
		t.Fatalf("blocked card must retain task detail navigation target: %s", body)
	}
	if strings.Contains(body, "No active work right now") {
		t.Fatalf("blocked-only agenda must not render the empty state: %s", body)
	}
}

func TestUpcomingContentUsesAggregateBlockedCountForCappedProjection(t *testing.T) {
	upcoming := &models.Upcoming{
		GeneratedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		BlockedTasks: []models.UpcomingTask{{
			Task: models.Task{
				ID:        "blocked-task",
				ProjectID: "project-1",
				Title:     "Visible blocked task",
				Category:  models.CategoryBacklog,
				Status:    models.StatusBlocked,
			},
		}},
		TaskSummary: &models.TaskSummary{BlockedCount: 201},
	}
	if got := upcomingBlockedTaskCount(upcoming); got != 201 {
		t.Fatalf("blocked count = %d, want aggregate count 201", got)
	}
	if got := upcomingBlockedTaskCount(&models.Upcoming{BlockedTasks: upcoming.BlockedTasks}); got != 1 {
		t.Fatalf("blocked fallback count = %d, want visible count 1", got)
	}

	var pulse bytes.Buffer
	if err := UpcomingContent(upcoming, "project-1").Render(context.Background(), &pulse); err != nil {
		t.Fatalf("render upcoming content: %v", err)
	}
	if want := `<div class="stat-title">Blocked</div><div class="stat-value text-xl">201</div>`; !strings.Contains(pulse.String(), want) {
		t.Fatalf("blocked stat missing aggregate count %q: %s", want, pulse.String())
	}
}

func TestUpcomingTaskCardsRenderStopOnlyForEligibleTasks(t *testing.T) {
	currentProjectID := "project-1"
	tests := []struct {
		name       string
		status     models.TaskStatus
		category   models.TaskCategory
		projectID  string
		wantStop   bool
		wantPrompt string
	}{
		{name: "running", status: models.StatusRunning, category: models.CategoryActive, projectID: currentProjectID, wantStop: true, wantPrompt: "Stop this running task?"},
		{name: "active pending", status: models.StatusPending, category: models.CategoryActive, projectID: currentProjectID, wantStop: true, wantPrompt: "Stop this waiting task?"},
		{name: "active queued", status: models.StatusQueued, category: models.CategoryActive, projectID: currentProjectID, wantStop: true, wantPrompt: "Stop this waiting task?"},
		{name: "scheduled", status: models.StatusPending, category: models.CategoryScheduled, projectID: currentProjectID, wantStop: false},
		{name: "completed", status: models.StatusCompleted, category: models.CategoryCompleted, projectID: currentProjectID, wantStop: false},
		{name: "failed", status: models.StatusFailed, category: models.CategoryBacklog, projectID: currentProjectID, wantStop: false},
		{name: "cancelled", status: models.StatusCancelled, category: models.CategoryBacklog, projectID: currentProjectID, wantStop: false},
		{name: "backlog pending", status: models.StatusPending, category: models.CategoryBacklog, projectID: currentProjectID, wantStop: false},
		{name: "chat", status: models.StatusRunning, category: models.CategoryChat, projectID: currentProjectID, wantStop: false},
		{name: "foreign", status: models.StatusRunning, category: models.CategoryActive, projectID: "project-foreign", wantStop: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bt := models.UpcomingTask{Task: models.Task{
				ID: "task-" + tt.name, ProjectID: tt.projectID, Title: "Task " + tt.name,
				Status: tt.status, Category: tt.category,
			}, AgentName: "Test Agent"}
			var rendered bytes.Buffer
			if err := upcomingTaskCardWithOrder(bt, currentProjectID, 1).Render(context.Background(), &rendered); err != nil {
				t.Fatalf("render task card: %v", err)
			}
			body := rendered.String()
			if got := strings.Contains(body, `data-upcoming-stop`); got != tt.wantStop {
				t.Fatalf("stop control present=%v, want %v: %s", got, tt.wantStop, body)
			}
			if tt.wantStop {
				for _, want := range []string{
					`aria-label="Stop task Task ` + tt.name + `"`,
					`hx-post="/tasks/task-` + tt.name + `/cancel?pulse=1&amp;project_id=project-1"`,
					`hx-confirm="` + tt.wantPrompt + `"`,
					`hx-disabled-elt="this"`,
					`onclick="event.stopPropagation()"`,
				} {
					if !strings.Contains(body, want) {
						t.Fatalf("eligible card missing %q: %s", want, body)
					}
				}
			}
		})
	}
}
func TestUpcomingTaskWithoutTagRendersNoTagBadge(t *testing.T) {
	upcoming := &models.Upcoming{
		GeneratedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		RunningTasks: []models.UpcomingTask{{
			Task: models.Task{
				ID:        "task-untagged",
				ProjectID: "project-1",
				Title:     "Untagged active task",
				Category:  models.CategoryActive,
				Status:    models.StatusRunning,
			},
			AgentName: "Test Agent",
		}},
	}

	var pulse bytes.Buffer
	if err := UpcomingContent(upcoming, "project-1").Render(context.Background(), &pulse); err != nil {
		t.Fatalf("render upcoming content: %v", err)
	}
	body := pulse.String()
	for _, label := range []string{components.TagLabel(models.TagFeature), components.TagLabel(models.TagBug), string(models.TagFeature), string(models.TagBug)} {
		if strings.Contains(body, ">"+label+"</span>") {
			t.Fatalf("untagged pulse task should not render tag label %q: %s", label, body)
		}
	}
}

func assertRenderedTagBadge(t *testing.T, body, label, class string) {
	t.Helper()
	labelMarker := ">" + label + "</span>"
	labelIndex := strings.Index(body, labelMarker)
	if labelIndex == -1 {
		t.Fatalf("expected tag label %q in rendered output: %s", label, body)
	}
	spanStart := strings.LastIndex(body[:labelIndex], "<span")
	if spanStart == -1 {
		t.Fatalf("expected tag label %q to be inside a span: %s", label, body)
	}
	spanOpenEnd := strings.Index(body[spanStart:], ">")
	if spanOpenEnd == -1 {
		t.Fatalf("expected opening span for tag label %q: %s", label, body)
	}
	spanOpen := body[spanStart : spanStart+spanOpenEnd]
	for _, want := range []string{"badge", "badge-sm", class} {
		if !strings.Contains(spanOpen, want) {
			t.Fatalf("expected tag badge span for %q to contain class %q, got %s", label, want, spanOpen)
		}
	}
}
