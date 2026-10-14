package service

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/toolschema"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// These required fields are the unconditional handler contracts. Alternatives
// (ID/title, source_path/content, action-dependent inputs) stay handler-validated.
// Listing every tool explicitly makes new tools require a contract review.
func TestEveryToolOptionalArgumentContract(t *testing.T) {
	required := map[string]string{
		"request_user_input": "questions", "create_task": "title prompt", "create_swarm_task": "title prompt", "edit_task": "id",
		"execute_tasks": "", "cancel_task": "", "list_tasks": "", "view_swarm": "", "view_task_thread": "", "send_to_task": "message", "merge_task": "",
		"set_task_goal": "goal", "clear_task_goal": "", "get_task_goal": "", "pause_task_goal": "", "resume_task_goal": "",
		"mark_task_goal_achieved": "goal_id reason", "report_task_goal_blocked": "goal_id blocker_key reason",
		"send_message": "", "github_create_issue": "title", "github_get_issue": "issue_number", "github_get_project_inbox": "",
		"github_is_actor_authorized": "github_login", "github_list_my_assigned_issues": "", "github_list_existing_automation_issues": "",
		"github_list_assigned_issues": "assignee", "github_list_assigned_issues_with_prs": "assignee", "github_comment_on_issue": "issue_number body",
		"github_add_issue_labels": "issue_number labels", "github_close_issue": "issue_number", "github_open_pull_request": "",
		"github_replace_pull_request_branch": "confirm_history_rewrite expected_head_sha", "github_forward_pr_feedback_to_tasks": "",
		"list_schedules": "", "view_pulse": "", "schedule_task": "time", "delete_schedule": "", "modify_schedule": "",
		"list_alerts": "", "get_alert": "alert_id", "list_existing_automation_notifications": "", "create_alert": "title", "create_notification": "title type",
		"decide_alert": "alert_id decision", "claim_alert": "alert_id", "create_alert_implementation_task": "alert_id title prompt",
		"link_alert_implementation_task": "alert_id task_id", "complete_alert_processing": "alert_id", "fail_alert_processing": "alert_id", "release_alert_claim": "alert_id",
		"delete_alert": "alert_id", "toggle_alert": "alert_id", "list_personalities": "", "get_personality": "", "set_personality": "personality",
		"save_custom_personality": "mode name system_prompt", "list_models": "", "get_model": "", "view_usage_analytics": "", "list_agents": "",
		"create_agent": "name system_prompt", "update_agent": "", "list_projects": "", "project_info": "", "get_current_project": "",
		"create_project": "name repo_url", "update_project_settings": "", "switch_project": "project", "view_settings": "", "list_channels": "", "view_system_update": "",
		"memory_view": "handle", "list_automations": "", "get_automation": "automation_id", "preview_automation_description": "description",
		"save_automation": "source", "update_automation_template": "", "run_automation_now": "", "pause_automation": "", "resume_automation": "", "delete_automation": "",
		"get_chat_mode": "", "set_chat_mode": "mode", "list_capabilities": "",
		"read_file": "file_path", "write_file": "file_path content", "edit_file": "file_path old_string new_string", "shell": "command", "interact_process": "session_id", "list_files": "", "grep_search": "pattern", "delete_file": "file_path",
		"skill_view": "handle", "skills_list": "", "agent_list": "", "agent_view": "key", "skill_manage": "action", "skill_import": "", "agent_skill_manage": "action",
		"register_automation_resources": "adapter_key stable_key resources", oversizedInputReaderTool: "offset limit",
	}
	defs := []llmcontracts.RuntimeToolDefinition{}
	for _, d := range chatcontrol.Registry() {
		defs = append(defs, llmcontracts.RuntimeToolDefinition{Name: d.Name, Schema: d.Schema})
	}
	for _, d := range agenttools.Definitions() {
		defs = append(defs, llmcontracts.RuntimeToolDefinition{Name: d.Name, Schema: d.Schema})
	}
	defs = append(defs, scopedFileDefinitions...)
	// Scheduled tasks intentionally omit project/read filters from list_alerts.
	for _, d := range (&LLMService{}).taskControlRuntimeTools(models.Task{ProjectID: "project", Category: models.CategoryScheduled}).Definitions {
		if d.Name == "list_alerts" {
			defs = append(defs, d)
		}
	}

	defs = append(defs, automationRegistrationDefinition)
	defs = append(defs, artifactReaderRuntime("unused").Definitions...)
	defs = append(defs, memory.SelectedMemoryRuntimeTools("unused", []memory.SelectedMemory{{File: "audit.md"}}).Definitions...)
	defs = append(defs, agentskills.SkillRuntimeTools(&agentskills.Catalog{}, "", "", contractAgentInspector{}).Definitions...)
	defs = append(defs, agentlibrary.SkillMutationTools(&agentlibrary.Importer{}, nil).Definitions...)
	defs = append(defs, agentlibrary.AgentSkillMutationTools(&agentlibrary.Importer{}, nil, "assigned", "project").Definitions...)
	libraryDefs := agentlibrary.LibraryAgentSkillMutationTools(&agentlibrary.Importer{}, nil).Definitions
	defs = append(defs, libraryDefs...)
	seen := map[string]bool{}
	properties := 0
	for _, d := range defs {
		t.Run(d.Name, func(t *testing.T) {
			want, ok := required[d.Name]
			if !ok {
				t.Fatalf("tool has no reviewed handler contract")
			}
			seen[d.Name] = true
			got := append([]string{}, d.Schema.Required...)
			expected := strings.Fields(want)
			if d.Name == "agent_skill_manage" && d.Schema == libraryDefs[0].Schema {
				expected = append(expected, "agent")
			}
			slices.Sort(got)
			slices.Sort(expected)
			if !slices.Equal(got, expected) {
				t.Fatalf("required=%v, handler contract=%v", got, expected)
			}
			properties += checkOptionalObjectContract(t, d.Schema)
		})
	}
	for n := range required {
		if !seen[n] {
			t.Errorf("reviewed tool %s was not exercised", n)
		}
	}
	t.Logf("checked %d definitions, %d distinct tools, %d declared property occurrences", len(defs), len(seen), properties)
}

type contractAgentInspector struct{}

func (contractAgentInspector) ListAgents(context.Context) ([]agentskills.AgentSummary, error) {
	return nil, nil
}
func (contractAgentInspector) InspectAgent(context.Context, string) (*agentskills.AgentDetails, error) {
	return nil, nil
}

// Exercise each nested object independently, including objects inside arrays.
// The source validator is the oracle for whether explicit zero/empty values are
// legal; normalization must preserve every legal value, not just truthy ones.
func checkOptionalObjectContract(t *testing.T, s *toolschema.Schema) int {
	t.Helper()
	count := 0
	if s.Items != nil {
		count += checkOptionalObjectContract(t, s.Items)
	}
	if s.Type != "object" {
		return count
	}
	compiled, err := jsonschema.CompileString("https://tools.invalid/contract", string(toolschema.JSON(s, nil)))
	if err != nil {
		t.Fatal(err)
	}
	base := contractSample(s).(map[string]any)
	for name, child := range s.Properties {
		count++
		t.Run(name, func(t *testing.T) {
			count += checkOptionalObjectContract(t, child)
			input := contractSample(s).(map[string]any)
			delete(input, name)
			raw, _ := json.Marshal(input)
			omitted, err := toolschema.ValidateArguments(s, nil, raw)
			if slices.Contains(s.Required, name) {
				if err == nil {
					t.Fatal("missing required field accepted")
				}
				input[name] = nil
				raw, _ = json.Marshal(input)
				if _, err := toolschema.ValidateArguments(s, nil, raw); err == nil {
					t.Fatal("null required field accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("omission rejected: %v", err)
			}
			input[name] = nil
			raw, _ = json.Marshal(input)
			normalized, err := toolschema.ValidateArguments(s, nil, raw)
			if err != nil || string(normalized) != string(omitted) {
				t.Fatalf("null differs from omission: %s / %s: %v", normalized, omitted, err)
			}
			for _, value := range []any{base[name], false, 0, "", []any{}, map[string]any{}} {
				input[name] = value
				if compiled.Validate(input) != nil {
					continue
				}
				raw, _ = json.Marshal(input)
				normalized, err = toolschema.ValidateArguments(s, nil, raw)
				if err != nil || string(normalized) != string(raw) {
					t.Fatalf("explicit value changed: %s -> %s (%v)", raw, normalized, err)
				}
			}
		})
	}
	if s.AdditionalProperties == nil || *s.AdditionalProperties {
		base["arbitrary_null"] = nil
		raw, _ := json.Marshal(base)
		got, err := toolschema.ValidateArguments(s, nil, raw)
		if err != nil || string(got) != string(raw) {
			t.Fatalf("arbitrary data changed: %s (%v)", got, err)
		}
	}
	return count
}

func contractSample(s *toolschema.Schema) any {
	if s.Const != nil {
		var v any
		_ = json.Unmarshal(s.Const, &v)
		return v
	}
	if len(s.Enum) > 0 {
		return s.Enum[0]
	}
	switch s.Type {
	case "object":
		m := map[string]any{}
		for k, v := range s.Properties {
			m[k] = contractSample(v)
		}
		return m
	case "array":
		n := 1
		if s.MinItems != nil {
			n = max(n, *s.MinItems)
		}
		a := []any{}
		for i := 0; i < n; i++ {
			a = append(a, contractSample(s.Items))
		}
		return a
	case "boolean":
		return false
	case "integer", "number":
		if s.Minimum != nil {
			v, _ := s.Minimum.Float64()
			return v
		}
		return 0
	case "string":
		if s.Pattern != "" {
			if strings.Contains(s.Pattern, "40") {
				return strings.Repeat("a", 40)
			}
			return "12:00"
		}
		if s.MinLength != nil {
			return strings.Repeat("a", *s.MinLength)
		}
		return "value"
	}
	return nil
}

// Decode into the real mutation/filter request structs, so pointer presence and
// custom unmarshalling remain correct after removing null placeholders.
func TestToolOptionalArgumentsPreserveHandlerPresence(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		target      any
	}{
		{"edit_task", `{"id":"task","priority":null,"auto_merge":null,"merge_target_branch":null}`, &TaskEditRequest{}},
		{"modify_schedule", `{"schedule_id":"schedule","interval":null,"enabled":null,"clear_context_on_start":null}`, &ModifyScheduleRequest{}},
		{"create_agent", `{"name":"agent","system_prompt":"prompt","enabled":null,"selectable_as_primary":null}`, &CreateAgentRuntimeInput{}},
		{"update_agent", `{"agent_id":"agent","tools":null,"scoped_files":null,"enabled":null,"description":null}`, &UpdateAgentRuntimeInput{}},
		{"update_project_settings", `{"max_workers":null,"description":null,"new_name":null}`, &UpdateProjectSettingsRuntimeInput{}},
		{"create_swarm_task", `{"title":"swarm","prompt":"prompt","reviewer_enabled":null,"merger_enabled":null}`, &SwarmTaskRuntimeInput{}},
		{"list_schedules", `{"enabled":null}`, &ListSchedulesRequest{}},
		{"view_usage_analytics", `{"top_limit":null,"recent_bucket_limit":null}`, &usageAnalyticsActionInput{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := toolschema.ValidateArguments(chatcontrol.Get(tc.name).Schema, nil, json.RawMessage(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, tc.target); err != nil {
				t.Fatal(err)
			}
			var omitted map[string]any
			_ = json.Unmarshal([]byte(tc.input), &omitted)
			for k, v := range omitted {
				if v == nil {
					delete(omitted, k)
				}
			}
			want := reflect.New(reflect.TypeOf(tc.target).Elem()).Interface()
			b, _ := json.Marshal(omitted)
			_ = json.Unmarshal(b, want)
			if !reflect.DeepEqual(tc.target, want) {
				t.Fatalf("handler presence changed: %#v vs %#v", tc.target, want)
			}
		})
	}
	var edit TaskEditRequest
	raw, err := toolschema.ValidateArguments(chatcontrol.Get("edit_task").Schema, nil, json.RawMessage(`{"id":"task","auto_merge":false,"merge_target_branch":""}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &edit)
	if edit.AutoMerge == nil || *edit.AutoMerge || edit.MergeTargetBranch == nil || *edit.MergeTargetBranch != "" || edit.PrioritySet {
		t.Fatalf("explicit false/empty or omitted priority lost: %+v", edit)
	}
}

func TestToolSchemasMatchDecodedRequestFields(t *testing.T) {
	inputs := map[string]any{
		"create_task": TaskCreationRequest{}, "create_swarm_task": SwarmTaskRuntimeInput{}, "edit_task": TaskEditRequest{}, "execute_tasks": TaskExecutionRequest{},
		"cancel_task": cancelTaskRuntimeInput{}, "list_tasks": ListTasksRequest{}, "view_swarm": ViewSwarmRequest{}, "view_task_thread": ViewThreadRequest{},
		"send_message": SendMessageRequest{}, "github_create_issue": githubCreateIssueRuntimeInput{},
		"schedule_task": ScheduleTaskRequest{}, "modify_schedule": ModifyScheduleRequest{}, "delete_schedule": DeleteScheduleRequest{}, "list_schedules": ListSchedulesRequest{},
		"create_alert": CreateAlertRequest{}, "delete_alert": DeleteAlertRequest{}, "toggle_alert": ToggleAlertRequest{}, "list_personalities": personalityListRequest{},
		"set_personality": SetPersonalityRequest{}, "save_custom_personality": CustomPersonalitySaveInput{}, "view_usage_analytics": usageAnalyticsActionInput{},
		"create_agent": CreateAgentRuntimeInput{}, "update_agent": UpdateAgentRuntimeInput{}, "create_project": CreateGitHubProjectRuntimeInput{},
		"update_project_settings": UpdateProjectSettingsRuntimeInput{}, "switch_project": SwitchProjectRequest{}, "list_automations": channelListAutomationsInput{},
		"get_automation": channelGetAutomationInput{}, "update_automation_template": AutomationTemplateUpdateRuntimeInput{},
	}
	for _, name := range []string{"set_task_goal", "clear_task_goal", "get_task_goal", "pause_task_goal", "resume_task_goal", "mark_task_goal_achieved", "report_task_goal_blocked"} {
		inputs[name] = TaskGoalRuntimeToolInput{}
	}
	for _, name := range []string{"github_get_issue", "github_is_actor_authorized", "github_list_my_assigned_issues", "github_list_existing_automation_issues", "github_list_assigned_issues", "github_list_assigned_issues_with_prs", "github_comment_on_issue", "github_add_issue_labels", "github_close_issue", "github_open_pull_request", "github_replace_pull_request_branch", "github_forward_pr_feedback_to_tasks"} {
		inputs[name] = GitHubIssueActionRequest{}
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) { checkDecodedSchemaFields(t, chatcontrol.Get(name).Schema, reflect.TypeOf(input)) })
	}
	checkDecodedSchemaFields(t, automationRegistrationDefinition.Schema, reflect.TypeOf(AutomationRegistrationRequest{}))
}

func checkDecodedSchemaFields(t *testing.T, s *toolschema.Schema, typ reflect.Type) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if s.Type == "array" {
		if typ.Kind() != reflect.Slice {
			t.Fatalf("array schema with handler type %s", typ)
		}
		checkDecodedSchemaFields(t, s.Items, typ.Elem())
		return
	}
	if s.Type == "object" && typ.Kind() == reflect.Struct {
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			fields[tag] = f.Type
		}
		for name, child := range s.Properties {
			ft, ok := fields[name]
			if !ok {
				t.Errorf("%s.%s has no handler field", typ, name)
				continue
			}
			checkDecodedSchemaFields(t, child, ft)
		}
	}
	// Real unmarshallers catch wrong scalar types and nested structs as well.
	raw, _ := json.Marshal(contractSample(s))
	dst := reflect.New(typ).Interface()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("schema value %s cannot decode into %s: %v", raw, typ, err)
	}
}

func TestPresenceSensitiveToolDefaultsAfterNullNormalization(t *testing.T) {
	for _, name := range []string{"list_personalities", "list_alerts", "list_existing_automation_notifications", "github_list_my_assigned_issues", "github_list_existing_automation_issues", "github_list_assigned_issues", "github_list_assigned_issues_with_prs"} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"limit": nil, "offset": nil}
			if strings.Contains(name, "list_assigned") {
				args["assignee"] = "alice"
			}
			input, _ := json.Marshal(args)
			raw, err := toolschema.ValidateArguments(chatcontrol.Get(name).Schema, nil, input)
			if err != nil {
				t.Fatal(err)
			}
			var req GitHubIssueActionRequest
			_ = json.Unmarshal(raw, &req)
			var limit, offset int
			switch name {
			case "list_personalities":
				limit, offset, err = PersonalityListPageArgs(raw)
			case "list_alerts", "list_existing_automation_notifications":
				limit, offset, err = alertRuntimeListPage(raw, 0, 0)
			case "github_list_existing_automation_issues":
				limit, offset, err = existingAutomationIssueListPageForInput(raw, req)
			default:
				limit, offset, err = assignedIssueListPageForInput(raw, req)
			}
			if err != nil || limit <= 0 || offset != 0 {
				t.Fatalf("null placeholders broke default pagination: %d %d %v", limit, offset, err)
			}
			args["limit"] = 0
			input, _ = json.Marshal(args)
			if _, err := toolschema.ValidateArguments(chatcontrol.Get(name).Schema, nil, input); err == nil {
				t.Fatal("explicit zero accepted where handler rejects it")
			}
		})
	}
}

func TestNestedToolRequiredFieldContracts(t *testing.T) {
	for _, tc := range []struct{ name, path, required string }{
		{"request_user_input", "questions[]", "id question options"},
		{"request_user_input", "questions[].options[]", "label description"},
		{"create_task", "chain", "enabled"}, {"edit_task", "chain", "enabled"},
		{"create_agent", "scoped_files[]", "directory"}, {"update_agent", "scoped_files[]", "directory"},
	} {
		t.Run(tc.name+"/"+tc.path, func(t *testing.T) {
			s := chatcontrol.Get(tc.name).Schema
			for _, part := range strings.Split(tc.path, ".") {
				s = s.Properties[strings.TrimSuffix(part, "[]")]
				if strings.HasSuffix(part, "[]") {
					s = s.Items
				}
			}
			got := append([]string{}, s.Required...)
			want := strings.Fields(tc.required)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("nested required=%v, handler contract=%v", got, want)
			}
		})
	}
}
