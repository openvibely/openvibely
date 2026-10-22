package chatcontrol

import "testing"

func TestTaskToolPolicy(t *testing.T) {
	ordinary := TaskToolNames("")
	for name := range ordinary {
		if Get(name) == nil {
			t.Errorf("task tool %s has no registry definition", name)
		}
	}
	for _, name := range []string{"cancel_task", "merge_task", "mark_task_goal_achieved", "report_task_goal_blocked", "request_user_input"} {
		if ordinary[name] {
			t.Errorf("protected tool %s exposed by default", name)
		}
	}
	auditor := TaskToolNames("automation:loop:auditor")
	for _, name := range []string{"create_task", "edit_task", "create_swarm_task", "execute_tasks", "send_to_task", "create_alert_implementation_task", "link_alert_implementation_task"} {
		if auditor[name] {
			t.Errorf("auditor exposes %s", name)
		}
		if !ordinary[name] {
			t.Errorf("ordinary tasks lost %s", name)
		}
	}
	delete(ordinary, "view_task_thread")
	if !TaskToolNames("")["view_task_thread"] {
		t.Fatal("policy maps share mutable state")
	}
}
