package chatcontrol

import "github.com/openvibely/openvibely/pkg/toolschema"

// SupportsProjectSelection identifies actions whose implicit project can be
// selected by an interactive web/API conversation. Task workers and channel
// conversations retain their original project boundary.
func SupportsProjectSelection(name string) bool {
	switch name {
	case "create_task", "create_swarm_task", "edit_task", "execute_tasks", "cancel_task",
		"list_tasks", "view_swarm", "view_task_thread", "send_to_task", "merge_task",
		"set_task_goal", "clear_task_goal", "get_task_goal", "pause_task_goal", "resume_task_goal",
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
	if _, exists := copy.Properties["project_id"]; !exists {
		copy.Properties["project_id"] = &toolschema.Schema{Type: "string", Description: "Optional destination/filter project ID from list_projects. Defaults to the current project. Available in interactive web/API chat only; task follow-ups remain restricted to their owning project."}
	}
	return &copy
}
