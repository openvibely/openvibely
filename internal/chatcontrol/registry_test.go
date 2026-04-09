package chatcontrol

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

// ---- Capability matrix tests by surface, mode, transport ----

func TestRegistry_AllActionsHaveValidJSON(t *testing.T) {
	for _, a := range Registry() {
		var m map[string]interface{}
		if err := json.Unmarshal(a.Parameters, &m); err != nil {
			t.Errorf("action %q has invalid Parameters JSON: %v", a.Name, err)
		}
	}
}

func TestRegistry_NoDuplicateNames(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Registry() {
		if seen[a.Name] {
			t.Errorf("duplicate action name: %q", a.Name)
		}
		seen[a.Name] = true
	}
}

func TestRegistry_AllNamesLowercase(t *testing.T) {
	for _, a := range Registry() {
		if a.Name != strings.ToLower(a.Name) {
			t.Errorf("action name %q is not lowercase", a.Name)
		}
	}
}

func TestRegistry_AllActionsHaveDescription(t *testing.T) {
	for _, a := range Registry() {
		if a.Description == "" {
			t.Errorf("action %q has empty description", a.Name)
		}
	}
}

func TestRegistry_AllActionsHaveDomain(t *testing.T) {
	validDomains := map[Domain]bool{
		DomainTasks: true, DomainSchedules: true, DomainAlerts: true,
		DomainPersonality: true, DomainModels: true, DomainAgents: true,
		DomainProjects: true, DomainSettings: true, DomainChat: true,
	}
	for _, a := range Registry() {
		if !validDomains[a.Domain] {
			t.Errorf("action %q has invalid domain %q", a.Name, a.Domain)
		}
	}
}

func TestRegistry_AllActionsHaveSurfaces(t *testing.T) {
	for _, a := range Registry() {
		if len(a.Surfaces) == 0 {
			t.Errorf("action %q has no surfaces", a.Name)
		}
	}
}

func TestRegistry_AllActionsHaveAllowedModes(t *testing.T) {
	for _, a := range Registry() {
		if len(a.AllowedModes) == 0 {
			t.Errorf("action %q has no allowed modes", a.Name)
		}
	}
}

func TestRegistry_PlanModeOnlyAllowsReadActions(t *testing.T) {
	for _, a := range Registry() {
		for _, m := range a.AllowedModes {
			if m == models.ChatModePlan && a.Access != AccessRead {
				t.Errorf("action %q has AccessLevel=%q but is allowed in plan mode (plan must be read-only)", a.Name, a.Access)
			}
		}
	}
}

func TestRegistry_WriteActionsRequireOrchestrateMode(t *testing.T) {
	for _, a := range Registry() {
		if a.Access == AccessWrite {
			hasOrchestrate := false
			for _, m := range a.AllowedModes {
				if m == models.ChatModeOrchestrate {
					hasOrchestrate = true
				}
			}
			if !hasOrchestrate {
				t.Errorf("write action %q does not include orchestrate mode", a.Name)
			}
		}
	}
}

func TestRegistry_DestructiveActionsNeedConfirmation(t *testing.T) {
	for _, a := range Registry() {
		if a.Sensitivity == SensitivityDestructive && !a.NeedsConfirmation {
			t.Errorf("destructive action %q should have NeedsConfirmation=true", a.Name)
		}
	}
}

// ---- Surface-specific capability tests ----

func TestToolDefsForContext_OrchestrateWeb(t *testing.T) {
	defs := ToolDefsForContext(models.ChatModeOrchestrate, SurfaceWeb, true)
	names := toolDefNames(defs)

	// Must have core write actions
	mustContain(t, names, "create_task", "edit_task", "execute_tasks", "send_to_task")
	// Must have new actions
	mustContain(t, names, "switch_project", "get_chat_mode", "set_chat_mode", "list_capabilities")
	// Must have new read actions
	mustContain(t, names, "get_alert", "get_model", "get_personality", "get_current_project")
	// Must have thread tools when requested
	mustContain(t, names, "view_task_thread", "send_to_task")
}

func TestToolDefsForContext_OrchestrateWebNoThread(t *testing.T) {
	defs := ToolDefsForContext(models.ChatModeOrchestrate, SurfaceWeb, false)
	names := toolDefNames(defs)

	// Must NOT have thread-only tools
	mustNotContain(t, names, "view_task_thread", "send_to_task")
	// Must still have core actions
	mustContain(t, names, "create_task", "edit_task", "execute_tasks")
}

func TestToolDefsForContext_PlanWeb(t *testing.T) {
	defs := ToolDefsForContext(models.ChatModePlan, SurfaceWeb, true)
	names := toolDefNames(defs)

	// Must NOT have write actions
	mustNotContain(t, names, "create_task", "edit_task", "execute_tasks",
		"set_personality", "schedule_task", "delete_schedule", "modify_schedule",
		"create_alert", "delete_alert", "toggle_alert", "switch_project",
		"set_chat_mode", "send_to_task")

	// Must have read actions
	mustContain(t, names, "list_projects", "list_models", "list_alerts",
		"list_personalities", "view_settings", "project_info",
		"get_chat_mode", "list_capabilities", "get_alert", "get_model",
		"get_personality", "get_current_project", "view_task_thread")
}

func TestToolDefsForContext_Telegram(t *testing.T) {
	defs := ToolDefsForContext(models.ChatModeOrchestrate, SurfaceTelegram, true)
	names := toolDefNames(defs)
	mustContain(t, names, "create_task", "switch_project", "list_projects",
		"view_task_thread", "send_to_task", "get_chat_mode", "list_capabilities")
}

func TestToolDefsForContext_Slack(t *testing.T) {
	defs := ToolDefsForContext(models.ChatModeOrchestrate, SurfaceSlack, false)
	names := toolDefNames(defs)
	mustContain(t, names, "create_task", "switch_project", "list_projects",
		"get_chat_mode", "list_capabilities")
	// Slack without thread tools
	mustNotContain(t, names, "view_task_thread", "send_to_task")
}

func TestToolDefsForContext_API(t *testing.T) {
	defs := ToolDefsForContext(models.ChatModeOrchestrate, SurfaceAPI, true)
	names := toolDefNames(defs)
	mustContain(t, names, "create_task", "switch_project", "get_chat_mode", "set_chat_mode")
}

// ---- IsAllowed tests ----

func TestIsAllowed_UnknownAction(t *testing.T) {
	err := IsAllowed("nonexistent_action", models.ChatModeOrchestrate, SurfaceWeb)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if err.Code != "unknown_action" {
		t.Errorf("expected code=unknown_action, got %q", err.Code)
	}
}

func TestIsAllowed_ModeBlocked(t *testing.T) {
	err := IsAllowed("create_task", models.ChatModePlan, SurfaceWeb)
	if err == nil {
		t.Fatal("expected error for plan mode write action")
	}
	if err.Code != "mode_blocked" {
		t.Errorf("expected code=mode_blocked, got %q", err.Code)
	}
}

func TestIsAllowed_ReadInPlan(t *testing.T) {
	err := IsAllowed("list_models", models.ChatModePlan, SurfaceWeb)
	if err != nil {
		t.Errorf("expected list_models allowed in plan mode, got error: %v", err)
	}
}

func TestIsAllowed_WriteInOrchestrate(t *testing.T) {
	err := IsAllowed("create_task", models.ChatModeOrchestrate, SurfaceWeb)
	if err != nil {
		t.Errorf("expected create_task allowed in orchestrate mode, got error: %v", err)
	}
}

// ---- ListForContext tests ----

func TestListForContext_Plan(t *testing.T) {
	summaries := ListForContext(models.ChatModePlan, SurfaceWeb)
	for _, s := range summaries {
		if s.Access != "read" {
			t.Errorf("plan mode listed non-read action %q (access=%s)", s.Name, s.Access)
		}
	}
	if len(summaries) == 0 {
		t.Error("expected at least some actions in plan mode")
	}
}

func TestListForContext_Orchestrate(t *testing.T) {
	summaries := ListForContext(models.ChatModeOrchestrate, SurfaceWeb)
	hasRead := false
	hasWrite := false
	for _, s := range summaries {
		if s.Access == "read" {
			hasRead = true
		}
		if s.Access == "write" {
			hasWrite = true
		}
	}
	if !hasRead || !hasWrite {
		t.Error("expected both read and write actions in orchestrate mode")
	}
}

// ---- Route-to-capability coverage test ----

func TestRegistry_CoversCoreActions(t *testing.T) {
	// These are the actions that must be in the registry for parity with
	// the legacy marker set and the existing runtime tool sets.
	required := []string{
		"create_task", "edit_task", "execute_tasks",
		"view_task_thread", "send_to_task",
		"schedule_task", "delete_schedule", "modify_schedule",
		"list_personalities", "set_personality",
		"list_models", "list_agents",
		"view_settings", "project_info",
		"list_projects", "switch_project",
		"list_alerts", "create_alert", "delete_alert", "toggle_alert",
		// new actions
		"get_chat_mode", "set_chat_mode", "list_capabilities",
		"get_alert", "get_model", "get_personality", "get_current_project",
	}
	names := map[string]bool{}
	for _, a := range Registry() {
		names[a.Name] = true
	}
	for _, r := range required {
		if !names[r] {
			t.Errorf("required action %q missing from registry", r)
		}
	}
}

// ---- Error contract tests ----

func TestActionError_StructuredFields(t *testing.T) {
	err := IsAllowed("create_task", models.ChatModePlan, SurfaceWeb)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Action != "create_task" {
		t.Errorf("Action=%q want create_task", err.Action)
	}
	if err.Code == "" {
		t.Error("Code should not be empty")
	}
	if err.Message == "" {
		t.Error("Message should not be empty")
	}
	if err.Error() != err.Message {
		t.Errorf("Error()=%q should equal Message=%q", err.Error(), err.Message)
	}
}

// ---- Regression: key user flows ----

func TestRegistry_AlertFlowActions(t *testing.T) {
	// list alerts, get specific alert
	for _, name := range []string{"list_alerts", "get_alert"} {
		def := Get(name)
		if def == nil {
			t.Fatalf("missing action %q", name)
		}
		if def.Access != AccessRead {
			t.Errorf("%s should be read, got %s", name, def.Access)
		}
	}
}

func TestRegistry_ModelFlowActions(t *testing.T) {
	for _, name := range []string{"list_models", "get_model"} {
		def := Get(name)
		if def == nil {
			t.Fatalf("missing action %q", name)
		}
		if def.Access != AccessRead {
			t.Errorf("%s should be read, got %s", name, def.Access)
		}
	}
}

func TestRegistry_PersonalityFlowActions(t *testing.T) {
	setPers := Get("set_personality")
	if setPers == nil {
		t.Fatal("missing set_personality")
	}
	if setPers.Access != AccessWrite {
		t.Errorf("set_personality should be write, got %s", setPers.Access)
	}
	getPers := Get("get_personality")
	if getPers == nil {
		t.Fatal("missing get_personality")
	}
	if getPers.Access != AccessRead {
		t.Errorf("get_personality should be read, got %s", getPers.Access)
	}
}

func TestRegistry_SwitchProjectAllSurfaces(t *testing.T) {
	def := Get("switch_project")
	if def == nil {
		t.Fatal("missing switch_project")
	}
	for _, s := range AllSurfaces {
		if !surfaceAllowed(*def, s) {
			t.Errorf("switch_project should be available on %s", s)
		}
	}
}

func TestRegistry_ChatModeActions(t *testing.T) {
	getMode := Get("get_chat_mode")
	if getMode == nil {
		t.Fatal("missing get_chat_mode")
	}
	if getMode.Access != AccessRead {
		t.Error("get_chat_mode should be read")
	}
	setMode := Get("set_chat_mode")
	if setMode == nil {
		t.Fatal("missing set_chat_mode")
	}
	if setMode.Access != AccessWrite {
		t.Error("set_chat_mode should be write")
	}
}

// ---- Anti-drift: all registry actions should have matching tool defs ----

func TestToolDefsForContext_FullOrchestrateCoversAllActions(t *testing.T) {
	// In full orchestrate mode with thread tools, every action in the registry
	// should be represented as a tool definition.
	defs := ToolDefsForContext(models.ChatModeOrchestrate, SurfaceWeb, true)
	defNames := map[string]bool{}
	for _, d := range defs {
		defNames[d.Name] = true
	}
	for _, a := range Registry() {
		// All actions that support orchestrate + web should be present
		if modeAllowed(a, models.ChatModeOrchestrate) && surfaceAllowed(a, SurfaceWeb) {
			if !a.IncludeThreadTools || true { // thread tools included
				if !defNames[a.Name] {
					t.Errorf("action %q is in registry but missing from orchestrate/web tool defs", a.Name)
				}
			}
		}
	}
}

// ---- Chain schema tests ----

func TestRegistry_CreateTaskChainSchemaHasProperties(t *testing.T) {
	def := Get("create_task")
	if def == nil {
		t.Fatal("missing create_task")
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(def.Parameters, &schema); err != nil {
		t.Fatalf("invalid Parameters JSON: %v", err)
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("missing properties in create_task schema")
	}

	chainObj, ok := props["chain"].(map[string]interface{})
	if !ok {
		t.Fatal("missing chain in create_task properties")
	}

	chainProps, ok := chainObj["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("chain schema has no properties — LLM cannot configure chaining in a single call")
	}

	// Verify all ChainConfiguration fields are present
	requiredFields := []string{"enabled", "trigger", "child_title", "child_prompt_prefix", "child_category", "child_agent_id", "child_chain_config"}
	for _, field := range requiredFields {
		if _, exists := chainProps[field]; !exists {
			t.Errorf("chain schema missing field %q", field)
		}
	}

	// Verify 'enabled' is listed as required
	chainRequired, _ := chainObj["required"].([]interface{})
	hasEnabled := false
	for _, r := range chainRequired {
		if r == "enabled" {
			hasEnabled = true
		}
	}
	if !hasEnabled {
		t.Error("chain schema should have 'enabled' as required field")
	}
}

func TestRegistry_EditTaskChainSchemaHasProperties(t *testing.T) {
	def := Get("edit_task")
	if def == nil {
		t.Fatal("missing edit_task")
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(def.Parameters, &schema); err != nil {
		t.Fatalf("invalid Parameters JSON: %v", err)
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("missing properties in edit_task schema")
	}

	chainObj, ok := props["chain"].(map[string]interface{})
	if !ok {
		t.Fatal("missing chain in edit_task properties")
	}

	chainProps, ok := chainObj["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("edit_task chain schema has no properties")
	}

	// Verify enabled field exists
	if _, exists := chainProps["enabled"]; !exists {
		t.Error("edit_task chain schema missing 'enabled' field")
	}
	if _, exists := chainProps["child_title"]; !exists {
		t.Error("edit_task chain schema missing 'child_title' field")
	}
}

func TestRegistry_CreateTaskDescriptionMentionsChaining(t *testing.T) {
	def := Get("create_task")
	if def == nil {
		t.Fatal("missing create_task")
	}
	if !strings.Contains(def.Description, "sequential") && !strings.Contains(def.Description, "chain") {
		t.Error("create_task description should mention sequential/chain workflows")
	}
}

// ---- Helpers ----

func toolDefNames(defs []llmcontracts.RuntimeToolDefinition) map[string]bool {
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	return names
}

func mustContain(t *testing.T, names map[string]bool, required ...string) {
	t.Helper()
	for _, r := range required {
		if !names[r] {
			t.Errorf("expected tool %q in definitions, got: %v", r, sortedKeys(names))
		}
	}
}

func mustNotContain(t *testing.T, names map[string]bool, forbidden ...string) {
	t.Helper()
	for _, f := range forbidden {
		if names[f] {
			t.Errorf("tool %q should NOT be in definitions for this context", f)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	_ = fmt.Sprint(keys) // suppress unused import
	return keys
}
