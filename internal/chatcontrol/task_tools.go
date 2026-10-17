package chatcontrol

import "strings"

// TaskToolNames is the common default policy for initial runs and task follow-ups.
// Return a fresh map so per-request restrictions cannot mutate the defaults.
func TaskToolNames(createdVia string) map[string]bool {
	allowed := map[string]bool{
		"list_tasks":                             true,
		"view_swarm":                             true,
		"view_task_thread":                       true,
		"send_to_task":                           true,
		"send_message":                           true,
		"create_task":                            true,
		"edit_task":                              true,
		"execute_tasks":                          true,
		"create_swarm_task":                      true,
		"list_schedules":                         true,
		"view_usage_analytics":                   true,
		"view_pulse":                             true,
		"view_system_update":                     true,
		"schedule_task":                          true,
		"delete_schedule":                        true,
		"modify_schedule":                        true,
		"create_alert":                           true,
		"create_notification":                    true,
		"list_alerts":                            true,
		"get_alert":                              true,
		"list_existing_automation_notifications": true,
		"decide_alert":                           true,
		"claim_alert":                            true,
		"create_alert_implementation_task":       true,
		"link_alert_implementation_task":         true,
		"complete_alert_processing":              true,
		"fail_alert_processing":                  true,
		"release_alert_claim":                    true,
		"github_create_issue":                    true,
		"github_get_issue":                       true,
		"github_get_project_inbox":               true,
		"github_is_actor_authorized":             true,
		"github_list_my_assigned_issues":         true,
		"github_list_existing_automation_issues": true,
		"github_list_assigned_issues":            true,
		"github_list_assigned_issues_with_prs":   true,
		"github_comment_on_issue":                true,
		"github_add_issue_labels":                true,
		"github_close_issue":                     true,
		"github_open_pull_request":               true, "github_replace_pull_request_branch": true,
		"github_forward_pr_feedback_to_tasks": true,
		"set_task_goal":                       true,
		"clear_task_goal":                     true,
		"get_task_goal":                       true,
		"pause_task_goal":                     true,
		"resume_task_goal":                    true,
		"list_capabilities":                   true,
	}
	if strings.HasPrefix(createdVia, "automation:") && strings.HasSuffix(createdVia, ":auditor") {
		for _, name := range []string{"create_task", "edit_task", "create_swarm_task", "execute_tasks", "send_to_task", "create_alert_implementation_task", "link_alert_implementation_task"} {
			delete(allowed, name)
		}
	}
	return allowed
}
