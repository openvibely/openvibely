package anthropicclient

import "strings"

// Strict slots protect argument contracts with the highest workflow impact.
// Keep every built-in tool classified explicitly. Unknown external tools use
// the remaining capacity; callers can override the ranking with StrictPriority.
var strictToolPriorities = func() map[string]int {
	groups := []struct {
		priority int
		names    string
	}{
		// Broad discovery must not accidentally acquire lifecycle filters.
		{600, `list_tasks`},
		// Destructive operations, publication, and outbound communication.
		{500, `github_replace_pull_request_branch merge_task delete_file delete_automation delete_schedule
		github_open_pull_request github_close_issue github_comment_on_issue github_create_issue github_add_issue_labels send_message`},
		// Local writes and execution, then persistent workflow configuration.
		{400, `bash edit_file write_file execute_tasks cancel_task create_task create_swarm_task edit_task
		schedule_task modify_schedule run_automation_now pause_automation resume_automation save_automation update_automation_template
		create_project update_project_settings create_agent update_agent register_automation_resources
		skill_manage skill_import agent_skill_manage`},
		// Human decisions, automation ownership, and goal state transitions.
		{300, `request_user_input decide_alert claim_alert create_alert_implementation_task link_alert_implementation_task
		complete_alert_processing fail_alert_processing release_alert_claim mark_task_goal_achieved report_task_goal_blocked
		set_task_goal clear_task_goal pause_task_goal resume_task_goal send_to_task create_notification create_alert delete_alert
		github_forward_pr_feedback_to_tasks`},
		// Filtered discovery and reads used to select mutation targets.
		{200, `list_alerts list_existing_automation_notifications list_schedules list_automations list_personalities
		github_list_my_assigned_issues github_list_existing_automation_issues github_list_assigned_issues github_list_assigned_issues_with_prs
		github_get_issue github_is_actor_authorized get_alert get_automation get_task_goal view_swarm view_task_thread
		view_usage_analytics read_file list_files grep_search skill_view memory_view agent_view read_input_artifact`},
		// Preferences and informational tools have the lowest built-in priority.
		{100, `toggle_alert set_personality save_custom_personality switch_project set_chat_mode
		preview_automation_description skills_list agent_list list_agents list_models get_model list_projects project_info
		get_current_project view_settings list_channels view_system_update get_chat_mode get_personality list_capabilities
		view_pulse github_get_project_inbox`},
	}
	out := make(map[string]int)
	for _, group := range groups {
		for _, name := range strings.Fields(group.names) {
			if _, exists := out[name]; exists {
				panic("duplicate strict tool priority: " + name)
			}
			out[name] = group.priority
		}
	}
	return out
}()

func strictToolPriority(t ToolDefinition) int {
	if t.StrictPriority != 0 {
		return t.StrictPriority
	}
	return strictToolPriorities[t.Name]
}
