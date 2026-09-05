package handler

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func TestBroadReadOnlyRouteSmokeExercisesProjectTaskAndSettingsSurfaces(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Broad route project").Build()
	other := tc.CreateProject().WithName("Other broad project").Build()
	agent := tc.CreateLLMConfig().WithName("Broad model").WithProvider(models.ProviderTest).WithModel("test-model").AsDefault().Build()
	active := tc.CreateTask(project.ID).WithTitle("Broad active task").WithCategory(models.CategoryActive).WithPriority(4).Build()
	active.AgentID = &agent.ID
	if err := tc.taskRepo.Update(ctx, active); err != nil {
		t.Fatalf("set task agent: %v", err)
	}
	backlog := tc.CreateTask(project.ID).WithTitle("Broad backlog task").WithCategory(models.CategoryBacklog).WithPriority(2).Build()
	completed := tc.CreateTask(project.ID).WithTitle("Broad completed task").WithCategory(models.CategoryCompleted).Build()
	if err := tc.taskRepo.UpdateStatus(ctx, completed.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	schedule := &models.Schedule{TaskID: active.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	if err := tc.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	exec := &models.Execution{TaskID: active.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "broad prompt"}
	if err := tc.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if err := tc.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "broad output", "diff --git a/file b/file", 12, 5); err != nil {
		t.Fatalf("complete execution: %v", err)
	}
	if err := tc.alertRepo.Create(ctx, &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityWarning, Title: "Broad alert", Message: "needs review"}); err != nil {
		t.Fatalf("create alert: %v", err)
	}
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.SetAgentRepo(agentRepo)
	if err := agentRepo.Create(ctx, &models.Agent{Name: "Broad Agent", Key: "broad-agent", Scope: models.AgentScopeProject, ProjectID: project.ID, Description: "Handles broad smoke routes", Model: agent.Model}); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}

	projectQuery := "?project_id=" + url.QueryEscape(project.ID)
	getPaths := []string{
		"/" + projectQuery,
		"/tasks" + projectQuery,
		"/tasks?project_id=" + url.QueryEscape(project.ID) + "&category=active&sort=priority",
		"/schedule" + projectQuery,
		"/analytics" + projectQuery,
		"/projects" + projectQuery,
		"/automations" + projectQuery,
		"/agents" + projectQuery,
		"/agents/" + agent.ID + "/json" + projectQuery,
		"/skills" + projectQuery,
		"/channels" + projectQuery,
		"/settings" + projectQuery,
		"/models" + projectQuery,
		"/tasks/" + active.ID + projectQuery,
		"/tasks/" + active.ID + "/executions" + projectQuery,
		"/tasks/" + active.ID + "/detail-status" + projectQuery,
		"/tasks/" + active.ID + "/detail-actions" + projectQuery,
		"/tasks/" + active.ID + "/changes" + projectQuery,
		"/tasks/" + active.ID + "/thread" + projectQuery,
		"/tasks/" + active.ID + "/thread/composer-action" + projectQuery,
		"/tasks/" + active.ID + "/thread/executions/" + exec.ID + "/fragment" + projectQuery,
		"/tasks/" + active.ID + "/goal" + projectQuery,
		"/api/tasks/" + active.ID + "/swarm" + projectQuery,
		"/executions/" + exec.ID + projectQuery,
		"/api/analytics/usage" + projectQuery,
		"/api/analytics/success-failure-rates" + projectQuery,
		"/api/analytics/avg-execution-time-by-task" + projectQuery,
		"/api/analytics/avg-execution-time-by-agent" + projectQuery,
		"/api/analytics/execution-trends-by-hour" + projectQuery,
		"/api/analytics/agent-usage-by-project" + projectQuery,
		"/api/analytics/most-frequent-tasks" + projectQuery,
		"/api/analytics/failed-task-patterns" + projectQuery,
	}
	for _, path := range getPaths {
		rec := tc.HTTP().Get(path).Execute()
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s returned %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	htmxPaths := []string{
		"/tasks" + projectQuery,
		"/schedule" + projectQuery,
		"/agents" + projectQuery,
		"/channels" + projectQuery,
		"/models" + projectQuery,
		"/tasks/" + active.ID + projectQuery,
	}
	for _, path := range htmxPaths {
		rec := tc.HTMX().Get(path).Execute()
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("HTMX GET %s returned %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	postForms := []struct {
		path string
		form url.Values
	}{
		{"/ui/preferences", url.Values{"selected_project_id": {project.ID}, "theme": {"dark"}}},
		{"/tasks/backlog/priority-counts" + projectQuery, nil},
		{"/tasks/backlog/sort" + projectQuery, url.Values{"sort": {"priority"}}},
		{"/tasks/completed/sort" + projectQuery, url.Values{"sort": {"updated"}}},
		{"/tasks/" + backlog.ID + "/goal" + projectQuery, url.Values{"goal": {"Finish the broad route task"}}},
		{"/tasks/" + backlog.ID + "/goal/pause" + projectQuery, nil},
		{"/tasks/" + backlog.ID + "/goal/resume" + projectQuery, nil},
		{"/tasks/" + backlog.ID + "/goal/clear" + projectQuery, nil},
		{"/tasks/" + active.ID + "/schedule" + projectQuery, url.Values{"run_at": {time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")}, "repeat_type": {string(models.RepeatOnce)}, "repeat_interval": {"1"}}},
		{"/schedules/" + schedule.ID + "/toggle" + projectQuery, nil},
		{"/schedules/" + schedule.ID + "/reschedule" + projectQuery, url.Values{"run_at": {time.Now().Add(3 * time.Hour).Format(time.RFC3339)}}},
		{"/tasks/move-completed" + projectQuery, nil},
		{"/tasks/batch-category" + projectQuery, url.Values{"task_ids": {backlog.ID}, "category": {string(models.CategoryActive)}}},
		{"/tasks/" + other.ID + "/schedule" + projectQuery, url.Values{"run_at": {time.Now().Add(time.Hour).Format("2006-01-02T15:04")}}},
	}
	for _, tt := range postForms {
		rec := tc.HTMX().Post(tt.path).WithForm(tt.form).Execute()
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("POST %s returned %d: %s", tt.path, rec.Code, rec.Body.String())
		}
	}

	put := tc.HTMX().Put("/tasks/" + active.ID + projectQuery).WithForm(url.Values{
		"title": {"Broad active task renamed"}, "prompt": {"Updated prompt"}, "priority": {"3"}, "category": {string(models.CategoryActive)}, "agent_id": {agent.ID},
	}).Execute()
	if put.Code >= http.StatusInternalServerError {
		t.Fatalf("PUT task returned %d: %s", put.Code, put.Body.String())
	}
	patch := tc.HTMX().Patch("/tasks/" + active.ID + "/category" + projectQuery).WithForm(url.Values{"category": {string(models.CategoryBacklog)}}).Execute()
	if patch.Code >= http.StatusInternalServerError {
		t.Fatalf("PATCH category returned %d: %s", patch.Code, patch.Body.String())
	}
	patch = tc.HTMX().Patch("/tasks/" + active.ID + "/status" + projectQuery).WithForm(url.Values{"status": {string(models.StatusPending)}}).Execute()
	if patch.Code >= http.StatusInternalServerError {
		t.Fatalf("PATCH status returned %d: %s", patch.Code, patch.Body.String())
	}

	deleteSchedule := tc.HTMX().Delete("/schedules/" + schedule.ID + projectQuery).Execute()
	if deleteSchedule.Code >= http.StatusInternalServerError {
		t.Fatalf("DELETE schedule returned %d: %s", deleteSchedule.Code, deleteSchedule.Body.String())
	}

	if strings.TrimSpace(tc.handler.executeProjectInfo(ctx, project.ID)) == "" {
		t.Fatal("project info summary should not be empty")
	}
	if strings.TrimSpace(tc.handler.executeListProjects(ctx, project.ID)) == "" {
		t.Fatal("project list summary should not be empty")
	}
}

func TestBroadMutationRouteContractsExerciseMoreHandlers(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Broad mutation project").Build()
	agent := tc.CreateLLMConfig().WithName("Broad mutation model").WithProvider(models.ProviderTest).WithModel("test-model").AsDefault().Build()
	active := tc.CreateTask(project.ID).WithTitle("Broad mutation active").WithCategory(models.CategoryActive).WithPriority(3).Build()
	active.AgentID = &agent.ID
	if err := tc.taskRepo.Update(ctx, active); err != nil {
		t.Fatalf("set task agent: %v", err)
	}
	backlog := tc.CreateTask(project.ID).WithTitle("Broad mutation backlog").WithCategory(models.CategoryBacklog).WithPriority(1).Build()
	completed := tc.CreateTask(project.ID).WithTitle("Broad mutation completed").WithCategory(models.CategoryCompleted).Build()
	if err := tc.taskRepo.UpdateStatus(ctx, completed.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	schedule := &models.Schedule{TaskID: active.ID, RunAt: time.Now().UTC().Add(90 * time.Minute), RepeatType: models.RepeatWeekly, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	if err := tc.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	alert := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "Broad mutation alert", Message: "exercise alert handlers"}
	if err := tc.alertRepo.Create(ctx, alert); err != nil {
		t.Fatalf("create alert: %v", err)
	}
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.SetAgentRepo(agentRepo)
	tc.handler.SetWebhookRepo(repository.NewWebhookRepo(tc.db))
	tc.handler.SetCustomPersonalityRepo(repository.NewCustomPersonalityRepo(tc.db))

	projectQuery := "?project_id=" + url.QueryEscape(project.ID)
	getPaths := []string{
		"/api/system/health",
		"/projects/new" + projectQuery,
		"/projects/" + project.ID + "/edit" + projectQuery,
		"/agents/plugins/state" + projectQuery,
		"/agents/" + agent.ID + "/skills" + projectQuery,
		"/agents/" + agent.ID + "/lifecycle-hooks" + projectQuery,
		"/models/" + agent.ID + "/edit-details" + projectQuery,
		"/models/openai-compatible/available" + projectQuery,
		"/workers" + projectQuery,
		"/workers/stats/global" + projectQuery,
		"/workers/stats/projects" + projectQuery,
		"/workers/stats/models" + projectQuery,
		"/api/capacity/global" + projectQuery,
		"/api/capacity/projects" + projectQuery,
		"/api/capacity/projects/" + project.ID + projectQuery,
		"/api/capacity/models" + projectQuery,
		"/api/capacity/models/" + agent.ID + projectQuery,
		"/channels/outbound-targets" + projectQuery,
		"/channels/outbound-targets/card" + projectQuery,
		"/personality" + projectQuery,
		"/alerts" + projectQuery,
		"/alerts/" + alert.ID + "/details" + projectQuery,
		"/alerts/unread-count" + projectQuery,
		"/insights" + projectQuery,
		"/insights/by-type" + projectQuery,
		"/insights/reports" + projectQuery,
		"/chat" + projectQuery,
		"/chat/composer-action" + projectQuery,
		"/chat/pending-inputs" + projectQuery,
	}
	for _, path := range getPaths {
		rec := tc.HTTP().Get(path).Execute()
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s returned %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	postForms := []struct {
		path string
		form url.Values
	}{
		{"/projects", url.Values{"name": {"Created through broad mutation"}, "repo_source": {"none"}}},
		{"/projects/" + project.ID + projectQuery, url.Values{"name": {"Broad mutation project renamed"}, "repo_source": {"none"}}},
		{"/automations/builder" + projectQuery, url.Values{"description": {"When a task completes, create an alert"}}},
		{"/automations/yaml/parse" + projectQuery, url.Values{"yaml": {"name: Broad Automation\nversion: 1\nnodes: []\nedges: []\n"}}},
		{"/tasks" + projectQuery, url.Values{"title": {"Created broad task"}, "prompt": {"Do broad coverage"}, "priority": {"2"}, "category": {string(models.CategoryBacklog)}, "agent_id": {agent.ID}}},
		{"/tasks/backlog/execute" + projectQuery, nil},
		{"/tasks/" + active.ID + "/run" + projectQuery, nil},
		{"/tasks/" + active.ID + "/cancel" + projectQuery, nil},
		{"/tasks/" + active.ID + "/thread/model" + projectQuery, url.Values{"agent_id": {agent.ID}}},
		{"/tasks/" + active.ID + "/thread/steer" + projectQuery, url.Values{"message": {"steer the active thread"}}},
		{"/tasks/" + active.ID + "/changes/live" + projectQuery, nil},
		{"/tasks/" + active.ID + "/worktree/auto-merge" + projectQuery, url.Values{"auto_merge_enabled": {"true"}}},
		{"/settings/worktree" + projectQuery, url.Values{"auto_sync_on_start": {"true"}, "auto_merge_enabled": {"false"}, "dirty_policy": {"commit"}}},
		{"/schedules/" + schedule.ID + projectQuery, url.Values{"run_at": {time.Now().Add(2 * time.Hour).Format("2006-01-02T15:04")}, "repeat_type": {string(models.RepeatDaily)}, "repeat_interval": {"1"}}},
		{"/api/schedules/" + schedule.ID + "/toggle" + projectQuery, nil},
		{"/agents", url.Values{"name": {"Broad Agent"}, "description": {"Broad mutation agent"}, "model": {agent.Model}, "model_id": {agent.ID}, "scope": {string(models.AgentScopeProject)}, "project_id": {project.ID}}},
		{"/agents/generate" + projectQuery, url.Values{"prompt": {"Generate a concise test agent"}, "model_id": {agent.ID}}},
		{"/agents/" + agent.ID + "/skills" + projectQuery, url.Values{"name": {"Broad Skill"}, "body": {"# Broad Skill\n\nUse this skill for route coverage."}}},
		{"/agents/" + agent.ID + "/lifecycle-hooks" + projectQuery, url.Values{"hooks_json": {"[]"}}},
		{"/models", url.Values{"name": {"Broad Created Model"}, "provider": {string(models.ProviderTest)}, "model": {"test-model"}, "api_key": {"test-key"}}},
		{"/models/" + agent.ID + projectQuery, url.Values{"name": {"Broad mutation model updated"}, "provider": {string(models.ProviderTest)}, "model": {"test-model"}, "api_key": {"test-key"}}},
		{"/models/" + agent.ID + "/set-default" + projectQuery, nil},
		{"/workers", url.Values{"max_workers": {"3"}}},
		{"/workers/projects/" + project.ID + "/limit" + projectQuery, url.Values{"max_workers": {"2"}}},
		{"/channels/webhooks" + projectQuery, url.Values{"name": {"Broad webhook"}, "path": {"broad-webhook"}, "enabled": {"true"}}},
		{"/personality/save" + projectQuery, url.Values{"personality": {"concise"}}},
		{"/personality/custom" + projectQuery, url.Values{"name": {"Broad Custom"}, "prompt": {"Be broad."}}},
		{"/chat/send" + projectQuery, url.Values{"message": {"hello broad chat"}, "agent_id": {agent.ID}}},
		{"/chat/steer" + projectQuery, url.Values{"message": {"steer broad chat"}}},
		{"/chat/stop" + projectQuery, nil},
		{"/upcoming/summary" + projectQuery, nil},
		{"/history/summary" + projectQuery, nil},
		{"/alerts/" + alert.ID + "/read" + projectQuery, nil},
		{"/alerts/" + alert.ID + "/approve" + projectQuery, nil},
		{"/alerts/" + alert.ID + "/reject" + projectQuery, nil},
		{"/alerts/" + alert.ID + "/dismiss" + projectQuery, nil},
		{"/alerts/read-all" + projectQuery, nil},
		{"/insights/analyze" + projectQuery, nil},
		{"/insights/extract-knowledge" + projectQuery, nil},
		{"/insights/health-check" + projectQuery, nil},
		{"/history/grade-ideas" + projectQuery, nil},
	}
	for _, tt := range postForms {
		rec := tc.HTMX().Post(tt.path).WithForm(tt.form).Execute()
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("POST %s returned %d: %s", tt.path, rec.Code, rec.Body.String())
		}
	}

	patchForms := []struct {
		path string
		form url.Values
	}{
		{"/tasks/" + active.ID + "/reorder" + projectQuery, url.Values{"after_id": {backlog.ID}}},
		{"/insights/nonexistent/status" + projectQuery, url.Values{"status": {"dismissed"}}},
	}
	for _, tt := range patchForms {
		rec := tc.HTMX().Patch(tt.path).WithForm(tt.form).Execute()
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("PATCH %s returned %d: %s", tt.path, rec.Code, rec.Body.String())
		}
	}

	deletePaths := []string{
		"/alerts/" + alert.ID + projectQuery,
		"/alerts" + projectQuery,
		"/tasks/backlog" + projectQuery,
		"/tasks/completed" + projectQuery,
		"/tasks/" + completed.ID + projectQuery,
		"/insights/nonexistent" + projectQuery,
		"/insights/knowledge/nonexistent" + projectQuery,
	}
	for _, path := range deletePaths {
		rec := tc.HTMX().Delete(path).Execute()
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("DELETE %s returned %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}
