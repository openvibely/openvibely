package chatcontrol

import (
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

const projectSelectionHelp = "Optional destination/filter project ID from list_projects. In an individual-project conversation, omission uses the current project and any supplied ID must match it. Cross-project selection is only available in All projects chat. In All projects chat an explicit real project ID is required; call once per project for a subset or all. Available in interactive web/API chat only; task follow-ups remain restricted to their owning project."

// SupportsProjectSelection identifies actions whose implicit project can be
// selected by an interactive web/API conversation. Task workers and channel
// conversations retain their original project boundary.
func SupportsProjectSelection(name string) bool {
	switch name {
	case "create_task", "create_swarm_task", "edit_task", "execute_tasks", "cancel_task",
		"list_tasks", "view_swarm", "view_task_thread", "send_to_task", "merge_task",
		"set_task_goal", "clear_task_goal", "get_task_goal", "pause_task_goal", "resume_task_goal",
		"github_is_actor_authorized", "github_open_pull_request", "github_replace_pull_request_branch", "github_forward_pr_feedback_to_tasks",
		"list_alerts", "get_alert", "create_alert", "create_notification", "decide_alert", "delete_alert", "toggle_alert",
		"update_project_settings", "create_agent", "update_agent",
		"schedule_task", "delete_schedule", "modify_schedule", "list_schedules",
		"view_pulse", "view_usage_analytics", "list_channels", "project_info",
		"list_automations", "get_automation", "preview_automation_description", "save_automation",
		"update_automation_template", "run_automation_now", "pause_automation", "resume_automation", "delete_automation",
		"send_message", "github_create_issue", "github_get_issue", "github_get_project_inbox",
		"github_list_my_assigned_issues", "github_list_existing_automation_issues", "github_list_assigned_issues",
		"github_list_assigned_issues_with_prs", "github_comment_on_issue", "github_add_issue_labels", "github_close_issue":
		return true
	}
	return false
}

// projectSelectionSchema copies the schema before adding a contextual field;
// shared registry schemas must never be mutated by concurrent requests.
func projectSelectionSchema(name string, surface Surface, schema *toolschema.Schema) *toolschema.Schema {
	if !SupportsProjectSelection(name) || (surface != SurfaceWeb && surface != SurfaceAPI) || schema == nil {
		return schema
	}
	copy := *schema
	copy.Properties = make(map[string]*toolschema.Schema, len(schema.Properties)+1)
	for key, value := range schema.Properties {
		copy.Properties[key] = value
	}
	projectID := toolschema.Schema{Type: "string"}
	if existing := copy.Properties["project_id"]; existing != nil {
		projectID = *existing
	}
	projectID.Description = projectSelectionHelp
	copy.Properties["project_id"] = &projectID
	if name == "update_project_settings" {
		if existing := copy.Properties["project_name"]; existing != nil {
			projectName := *existing
			projectName.Description = "Optional assertion matching the selected project's name exactly, case-insensitive. Defaults to the current project when project_id is omitted."
			copy.Properties["project_name"] = &projectName
		}
	}
	return &copy
}

func projectSelectionDescription(name string, surface Surface, description string) string {
	if !SupportsProjectSelection(name) || (surface != SurfaceWeb && surface != SurfaceAPI) {
		return description
	}
	description = strings.NewReplacer("current project", "selected project", "current-project", "selected-project").Replace(description)
	if name == "update_project_settings" {
		description = strings.ReplaceAll(description, "optional project_id/project_name are selected-project assertions", "project_id selects the project; project_name optionally asserts its name")
	}
	return description
}

// ToolDefsForAllProjectsContext adds routing fields only for global web/API chat.
// Ordinary conversations and channel runtimes use the original registry schemas.
func ToolDefsForAllProjectsContext(mode models.ChatMode, surface Surface, includeThreadTools bool) []llmcontracts.RuntimeToolDefinition {
	defs := ToolDefsForContext(mode, surface, includeThreadTools)
	for i := range defs {
		defs[i].Description = projectSelectionDescription(defs[i].Name, surface, defs[i].Description)
		defs[i].Schema = projectSelectionSchema(defs[i].Name, surface, defs[i].Schema)
	}
	return defs
}
