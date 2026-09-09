package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestHandlerTaskUIAgentProjectionAcrossRenders(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, _ := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	project := "default"
	otherProject := createProject(t, h, "Task UI projection other project")
	global := createRichTaskUIAgent(t, agentRepo, "Alpha Task UI Agent")
	global.Model = "task-ui-model"
	if err := agentRepo.Update(ctx, global); err != nil {
		t.Fatalf("update global task UI agent: %v", err)
	}
	projectAgent := createRichTaskUIAgent(t, agentRepo, "Project Task UI Agent")
	projectAgent.Scope = models.AgentScopeProject
	projectAgent.ProjectID = project
	if err := agentRepo.Update(ctx, projectAgent); err != nil {
		t.Fatalf("update project task UI agent: %v", err)
	}
	foreign := createRichTaskUIAgent(t, agentRepo, "Foreign Task UI Agent")
	foreign.Scope = models.AgentScopeProject
	foreign.ProjectID = otherProject.ID
	if err := agentRepo.Update(ctx, foreign); err != nil {
		t.Fatalf("update foreign task UI agent: %v", err)
	}
	disabled := createRichTaskUIAgent(t, agentRepo, "Disabled Task UI Agent")
	disabled.Enabled = false
	disabled.SelectableAsPrimary = false
	if err := agentRepo.Update(ctx, disabled); err != nil {
		t.Fatalf("disable task UI agent: %v", err)
	}
	for i := 0; i < 996; i++ {
		createRichTaskUIAgent(t, agentRepo, fmt.Sprintf("Task UI Filler Agent %04d", i))
	}

	cardTask := createTask(t, h, project, "Task UI Agent Badge", func(task *models.Task) {
		task.AgentDefinitionID = &global.ID
	})
	detailTask := createTask(t, h, project, "Task UI Assigned Fallback", func(task *models.Task) {
		task.AgentDefinitionID = &disabled.ID
		task.Category = models.CategoryBacklog
	})
	createTask(t, h, project, "Task UI No Agent")

	type renderCase struct {
		name    string
		request func() *http.Request
		want    []string
	}
	cases := []renderCase{
		{
			name: "full page",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/tasks?project_id="+project, nil)
			},
			want: []string{global.Name, projectAgent.Name, "No Agent"},
		},
		{
			name: "main content HTMX",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+project, nil)
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Target", "main-content")
				return req
			},
			want: []string{global.Name, projectAgent.Name, "No Agent"},
		},
		{
			name: "board only HTMX",
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+project, nil)
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Target", "kanban-board")
				return req
			},
			want: []string{global.Name, disabled.Name, cardTask.Title},
		},
		{
			name: "initial task detail",
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/tasks/"+detailTask.ID, nil)
			},
			want: []string{disabled.Name, "No Agent"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, tc.request())
			counter.SetEnabled(false)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("response missing %q: %s", want, body)
				}
			}
			if strings.Contains(body, "PRIVATE_TASK_UI_AGENT_CONFIGURATION") {
				t.Fatalf("response leaked private Agent configuration")
			}
			assertTaskUIAgentProjectionQuery(t, counter)
		})
	}

	fullReq := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+project, nil)
	fullRec := httptest.NewRecorder()
	e.ServeHTTP(fullRec, fullReq)
	body := fullRec.Body.String()
	if strings.Contains(body, `value="`+foreign.ID+`"`) || strings.Contains(body, `value="`+disabled.ID+`"`) {
		t.Fatalf("new Task selector includes unavailable Agent: %s", body)
	}
	globalPosition := strings.Index(body, `value="`+global.ID+`"`)
	projectPosition := strings.Index(body, `value="`+projectAgent.ID+`"`)
	if globalPosition < 0 || projectPosition < 0 || globalPosition > projectPosition {
		t.Fatalf("Task selector did not retain name ordering: global=%d project=%d", globalPosition, projectPosition)
	}

	form := url.Values{
		"title":    {"Task UI Post Action Refresh"},
		"prompt":   {"refresh board with compact Agent projection"},
		"category": {string(models.CategoryBacklog)},
		"priority": {"2"},
	}
	postReq := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project, strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("HX-Request", "true")
	counter.Reset()
	counter.SetEnabled(true)
	postRec := httptest.NewRecorder()
	e.ServeHTTP(postRec, postReq)
	counter.SetEnabled(false)
	if postRec.Code != http.StatusOK || !strings.Contains(postRec.Body.String(), "kanban-board") || !strings.Contains(postRec.Body.String(), global.Name) {
		t.Fatalf("post-action board refresh = status %d body %s", postRec.Code, postRec.Body.String())
	}
	assertTaskUIAgentProjectionQuery(t, counter)
}

func createRichTaskUIAgent(t *testing.T, repo *repository.AgentRepo, name string) *models.Agent {
	t.Helper()
	agent := &models.Agent{
		Name:                name,
		Description:         "Task UI projection test Agent",
		SystemPrompt:        strings.Repeat("PRIVATE_TASK_UI_AGENT_CONFIGURATION ", 256),
		Model:               "inherit",
		Tools:               []string{"Read", "Write"},
		ToolConfig:          models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read"}}}},
		Plugins:             []string{"task-ui-plugin"},
		MCPServers:          []models.MCPServerConfig{{Name: "task-ui-mcp", Command: []string{"task-ui-command"}}},
		Skills:              []models.SkillConfig{{Name: "task-ui-skill", Content: strings.Repeat("skill ", 128)}},
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5"},
		SourceRefs:          []string{"agents/task-ui/SKILLS.md"},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create rich task UI agent: %v", err)
	}
	return agent
}

func assertTaskUIAgentProjectionQuery(t *testing.T, counter *testutil.SQLStatementCounter) {
	t.Helper()
	var agentStatements []string
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from agents") {
			agentStatements = append(agentStatements, statement)
		}
	}
	if len(agentStatements) != 1 {
		t.Fatalf("Agent statements = %#v, want one compact catalog query", agentStatements)
	}
	query := strings.ToLower(strings.Join(strings.Fields(agentStatements[0]), " "))
	projection := strings.Split(query, " from agents ")[0]
	for _, forbidden := range []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("Task UI query selected forbidden Agent column %q: %s", forbidden, agentStatements[0])
		}
	}
}
