package contracts

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestComposeRuntimeToolFilterSharedPolicy(t *testing.T) {
	rt := &RuntimeTools{
		Definitions: []RuntimeToolDefinition{
			{Name: " read_runtime ", Access: RuntimeToolAccessRead},
			{Name: "write_runtime"},
		},
	}
	base := func(name string) bool { return name == "read_file" || name == "bash" }

	plan := ComposeRuntimeToolFilter(base, rt, RuntimeToolPolicyOptions{ChatMode: models.ChatModePlan})
	if !plan("read_runtime") {
		t.Fatal("plan mode should allow read-only runtime tools")
	}
	if plan("write_runtime") {
		t.Fatal("plan mode should block write runtime tools")
	}
	if !plan("read_file") {
		t.Fatal("plan mode should allow provider-local read-only tools through the base filter")
	}
	if plan("bash") {
		t.Fatal("plan mode should block provider-local write tools before the base filter")
	}

	orchestrate := ComposeRuntimeToolFilter(base, rt, RuntimeToolPolicyOptions{ChatMode: models.ChatModeOrchestrate})
	if !orchestrate("read_runtime") || !orchestrate("write_runtime") {
		t.Fatal("orchestrate mode should allow runtime tools")
	}
	if orchestrate("read_file") || orchestrate("bash") {
		t.Fatal("orchestrate mode should block provider-local default tools")
	}

	followup := ComposeRuntimeToolFilter(base, rt, RuntimeToolPolicyOptions{IsTaskFollowup: true, ChatMode: models.ChatModeOrchestrate})
	if !followup("read_runtime") || !followup("write_runtime") || !followup("bash") {
		t.Fatal("task follow-up should keep runtime tools and base provider-local policy")
	}
	if followup("unknown_default") {
		t.Fatal("task follow-up should preserve base default-tool denials")
	}
}

func TestComposeRuntimeToolFilterRuntimeFilterOverridesAfterModeGate(t *testing.T) {
	rt := &RuntimeTools{
		Definitions: []RuntimeToolDefinition{
			{Name: "read_runtime", Access: RuntimeToolAccessRead},
			{Name: "write_runtime", Access: RuntimeToolAccessWrite},
			{Name: "default_write_access"},
		},
		Filter: func(name string) (bool, bool) {
			switch name {
			case "read_runtime":
				return false, true
			case "write_runtime":
				return true, true
			case "default_write_access":
				return true, true
			default:
				return false, false
			}
		},
	}

	plan := ComposeRuntimeToolFilter(nil, rt, RuntimeToolPolicyOptions{ChatMode: models.ChatModePlan})
	if plan("read_runtime") {
		t.Fatal("runtime filter should be able to deny a read runtime tool after the plan-mode gate")
	}
	if plan("write_runtime") || plan("default_write_access") {
		t.Fatal("runtime filter should not override the plan-mode write-runtime gate")
	}

	orchestrate := ComposeRuntimeToolFilter(nil, rt, RuntimeToolPolicyOptions{ChatMode: models.ChatModeOrchestrate})
	if !orchestrate("write_runtime") || !orchestrate("default_write_access") {
		t.Fatal("runtime filter should be able to allow write runtime tools in orchestrate mode")
	}
	if orchestrate("read_runtime") {
		t.Fatal("runtime filter denial should apply in orchestrate mode")
	}
}

func TestComposeRuntimeToolFilterSkipDefaultTools(t *testing.T) {
	rt := &RuntimeTools{
		SkipDefaultTools: true,
		Definitions:      []RuntimeToolDefinition{{Name: "runtime_read", Access: RuntimeToolAccessRead}},
	}
	filter := ComposeRuntimeToolFilter(func(string) bool { return true }, rt, RuntimeToolPolicyOptions{IsTaskFollowup: true, ChatMode: models.ChatModeOrchestrate})
	if !filter("runtime_read") {
		t.Fatal("SkipDefaultTools should not block runtime definitions")
	}
	if filter("read_file") || filter("bash") {
		t.Fatal("SkipDefaultTools should suppress provider-local/default tools")
	}
}

func TestComposeRuntimeToolFilterProviderOptions(t *testing.T) {
	rt := &RuntimeTools{
		Definitions: []RuntimeToolDefinition{{Name: "skills_list", Access: RuntimeToolAccessRead}},
		Filter: func(name string) (bool, bool) {
			if name != "skills_list" {
				return false, true
			}
			return true, true
		},
	}
	canonical := func(name string) string {
		if strings.EqualFold(strings.TrimSpace(name), "skill_list") {
			return "skills_list"
		}
		return name
	}
	allowsReadOnly := func(name string) bool {
		if DefaultPlanModeAllowsReadOnlyTool(name) {
			return true
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "web_fetch", "web_fetch_20250910":
			return true
		default:
			return false
		}
	}

	filter := ComposeRuntimeToolFilter(func(string) bool { return true }, rt, RuntimeToolPolicyOptions{ChatMode: models.ChatModePlan, CanonicalName: canonical, AllowsReadOnlyTool: allowsReadOnly})
	if !filter("skill_list") {
		t.Fatal("provider canonical name option should resolve runtime wire aliases")
	}
	if !filter("web_fetch_20250910") {
		t.Fatal("provider read-only option should admit provider-native read-only tools")
	}
	if filter("write_file") {
		t.Fatal("provider read-only option should not admit write tools")
	}
}

func TestAllowsBuiltInToolSharedPolicy(t *testing.T) {
	mapTool := func(name string) string {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "read_file":
			return "Read"
		case "bash":
			return "Bash"
		default:
			return ""
		}
	}
	if !AllowsBuiltInTool("bash", BuiltInToolPolicyOptions{MapToolName: mapTool}) {
		t.Fatal("empty configured tool list should allow built-in tools")
	}
	if AllowsBuiltInTool("bash", BuiltInToolPolicyOptions{SkipDefaultTools: true, MapToolName: mapTool}) {
		t.Fatal("SkipDefaultTools should deny built-in tools")
	}
	if !AllowsBuiltInTool("read_file", BuiltInToolPolicyOptions{ConfiguredTools: []string{" read "}, MapToolName: mapTool}) {
		t.Fatal("explicit configured tool grant should allow mapped built-in tool")
	}
	if AllowsBuiltInTool("bash", BuiltInToolPolicyOptions{ConfiguredTools: []string{"Read"}, MapToolName: mapTool}) {
		t.Fatal("missing configured tool grant should deny mapped built-in tool")
	}
	if !AllowsBuiltInTool("custom_runtime_tool", BuiltInToolPolicyOptions{ConfiguredTools: []string{"Read"}, MapToolName: mapTool}) {
		t.Fatal("unmapped tools should pass through")
	}
}
