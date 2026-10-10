package handler

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAutomationBuilderActionInstructionsSubmission(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	repo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(repo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(repo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, nil)
	seed, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
	require.NoError(t, err)
	seed.Name = "Instruction mapping"
	seed.Nodes = []models.AutomationDraftNode{{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review changes.", "category": "backlog", "priority": 2}}}
	saved, err := compiler.Save(context.Background(), service.AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: seed})
	require.NoError(t, err)

	for _, route := range []struct{ name, path string }{
		{"create", "/automations/builder"},
		{"edit", "/automations/" + saved.Definition.Automation.ID + "/builder"},
	} {
		for _, test := range []struct {
			name, value   string
			missing, yaml bool
		}{
			{name: "text", value: "  Keep whitespace.\nSecond line.  "},
			{name: "empty", value: ""},
			{name: "missing", missing: true},
			{name: "yaml", value: "Ignored browser instructions", yaml: true},
		} {
			t.Run(route.name+"/"+test.name, func(t *testing.T) {
				candidate := seed
				candidate.Nodes = []models.AutomationDraftNode{
					{Key: "notify", Name: "Notify", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Prior instructions"}},
					{Key: "issue", Name: "Issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"labels": []string{"bug", "triage"}, "instructions": "Prior instructions"}},
					{Key: "pr", Name: "Pull request", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"base": "release", "draft": true, "instructions": "Prior instructions"}},
					{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review changes.", "category": "backlog", "priority": 2}},
				}
				form := url.Values{"project_id": {project.ID}, "builder_source": {"blank"}, "candidate_json": {automationDraftCandidateJSONForTest(t, candidate)}, "node_pr_draft": {"true"}}
				for _, key := range []string{"notify", "issue", "pr", "task"} {
					if !test.missing {
						form.Set("node_"+key+"_instructions", test.value)
					}
				}
				if test.yaml {
					document, err := service.EncodeAutomationDraftYAML(candidate)
					require.NoError(t, err)
					form.Set("automation_yaml", document)
					form.Set("node_notify_notification_type", "ignored")
					form.Set("node_issue_labels", "ignored")
					form.Set("node_pr_base", "ignored")
					form.Set("node_pr_draft", "false")
				}
				response := tc.HTMX().Post(route.path + "?project_id=" + project.ID).WithForm(form).Execute()
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				result := automationCandidateFromResponse(t, response)
				want := test.value
				if test.missing || test.yaml {
					want = "Prior instructions"
				}
				for _, key := range []string{"notify", "issue", "pr"} {
					require.Equal(t, want, automationDraftNodeByKeyHandler(t, result, key).Config["instructions"], key)
				}
				require.NotContains(t, automationDraftNodeByKeyHandler(t, result, "task").Config, "instructions")
				require.Equal(t, "change_proposal", automationDraftNodeByKeyHandler(t, result, "notify").Config["notification_type"])
				require.EqualValues(t, []any{"bug", "triage"}, automationDraftNodeByKeyHandler(t, result, "issue").Config["labels"])
				pr := automationDraftNodeByKeyHandler(t, result, "pr")
				require.Equal(t, "release", pr.Config["base"])
				require.Equal(t, true, pr.Config["draft"])
			})
		}
	}
}
