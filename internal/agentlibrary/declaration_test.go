package agentlibrary

import (
	"strings"
	"testing"
)

func TestParseDeclaration_FullExample(t *testing.T) {
	// Mirrors the runbook §Markdown Skills And Active Frontmatter example.
	content := `---
kind: openvibely.agent_skill
version: 1

agent:
  key: backend-engineer
  name: Backend Engineer
  description: Implements backend features.
  enabled: true
  primary_agent_enabled: true
  system_prompt: You are a backend engineer.

skill:
  key: add-goose-migration
  name: Add Goose Migration
  scope: project
  enabled: true
  description: Add Goose migrations in this repo.

routing:
  triggers:
    - database
    - migration
  priority: 50

tools:
  - shell
  - file_read

plugins:
  - github

mcp_servers:
  - local-repo

permissions:
  repo_read: true
  repo_write: true

lifecycle_hooks:
  after_complete:
    skill: observe_backend_work
    enabled: true
    blocking: false
    output_contract: learning_summary
---

# Add Goose Migration

Body here.
`
	decl, body, err := ParseDeclaration(content)
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	if decl.Agent.Key != "backend-engineer" || decl.Skill.Key != "add-goose-migration" {
		t.Fatalf("bad keys: %+v", decl)
	}
	if decl.Handle() != "add-goose-migration" {
		t.Fatalf("handle = %q", decl.Handle())
	}
	if decl.Skill.Scope != "project" {
		t.Fatalf("scope = %q", decl.Skill.Scope)
	}
	if len(decl.Routing.Triggers) != 2 || decl.Routing.Priority != 50 {
		t.Fatalf("bad routing: %+v", decl.Routing)
	}
	if len(decl.Tools) != 2 || decl.Tools[0] != "shell" {
		t.Fatalf("bad tools: %v", decl.Tools)
	}
	hook, ok := decl.LifecycleHooks["after_complete"]
	if !ok {
		t.Fatalf("missing after_complete hook")
	}
	if hook.Skill != "observe_backend_work" || hook.OutputContract != "learning_summary" {
		t.Fatalf("bad hook: %+v", hook)
	}
	if !strings.Contains(body, "# Add Goose Migration") {
		t.Fatalf("body should preserve markdown, got %q", body)
	}
}

func TestParseDeclaration_RejectsBadKind(t *testing.T) {
	content := `---
kind: notes
version: 1
agent: {key: a}
skill: {key: b}
---
body
`
	if _, _, err := ParseDeclaration(content); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

func TestParseDeclaration_AllowsAgentRootDeclarationWithoutSkillBlock(t *testing.T) {
	content := `---
kind: openvibely.agent_skill
version: 1
agent:
  key: skill_curator
  name: Skill Curator
---
body
`
	decl, _, err := ParseDeclaration(content)
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	if !decl.IsAgentRootDeclaration() {
		t.Fatalf("expected root declaration without skill key, got %q", decl.Skill.Key)
	}
	if decl.Handle() != "" {
		t.Fatalf("root declaration should not have a skill handle, got %q", decl.Handle())
	}
}

func TestParseDeclaration_AllowsStandaloneSkillWithoutAgentKey(t *testing.T) {
	content := `---
kind: openvibely.agent_skill
version: 1
skill:
  key: reusable_debugger
  scope: project
---
body
`
	decl, _, err := ParseDeclaration(content)
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	if decl.Handle() != "reusable_debugger" || decl.Agent.Key != "" {
		t.Fatalf("expected standalone skill declaration, got handle=%q agent=%q", decl.Handle(), decl.Agent.Key)
	}
}

func TestParseDeclaration_RejectsBadSlug(t *testing.T) {
	content := `---
kind: openvibely.agent_skill
version: 1
agent: {key: "../etc"}
skill: {key: x}
---
body
`
	if _, _, err := ParseDeclaration(content); err == nil {
		t.Fatalf("expected slug error")
	}
}

func TestParseDeclaration_RejectsBadHookSlot(t *testing.T) {
	content := `---
kind: openvibely.agent_skill
version: 1
agent: {key: a}
skill: {key: b}
lifecycle_hooks:
  somewhere_unknown:
    skill: x
---
`
	if _, _, err := ParseDeclaration(content); err == nil {
		t.Fatalf("expected unknown slot error")
	}
}

func TestParseDeclaration_RejectsBadOutputContract(t *testing.T) {
	content := `---
kind: openvibely.agent_skill
version: 1
agent: {key: a}
skill: {key: b}
lifecycle_hooks:
  after_complete:
    skill: x
    output_contract: not_a_contract
---
`
	if _, _, err := ParseDeclaration(content); err == nil {
		t.Fatalf("expected output_contract error")
	}
}

func TestSplitFrontmatter_NoBlock(t *testing.T) {
	_, body, ok := SplitFrontmatter("# just a body")
	if ok {
		t.Fatalf("ok should be false without frontmatter")
	}
	if body != "# just a body" {
		t.Fatalf("body should be returned untouched")
	}
}

func TestRenderSkillMarkdown_RoundTrips(t *testing.T) {
	decl := &SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Agent:   AgentDeclaration{Key: "backend-engineer"},
		Skill:   SkillBlock{Key: "verify-go-change", Scope: "project"},
	}
	out, err := RenderSkillMarkdown(decl, "# Verify\n")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.HasPrefix(out, "---\n") {
		t.Fatalf("missing frontmatter delimiter: %q", out)
	}
	parsed, body, err := ParseDeclaration(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if parsed.Handle() != "verify-go-change" {
		t.Fatalf("round-trip handle = %q", parsed.Handle())
	}
	for _, forbidden := range []string{"agent:", "permissions:", "lifecycle_hooks:", "model_defaults:", "tool_config:"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("standalone skill frontmatter should not render %s:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(body, "# Verify") {
		t.Fatalf("body lost: %q", body)
	}
}

// A hook's declared payload must survive parsing and reach the hook row, so an
// agent author controls its own context from its declaration file.
func TestHookPayloadDeclarationReachesHookRow(t *testing.T) {
	decl, _, err := ParseDeclaration(`---
kind: openvibely.agent_skill
version: 1
agent:
  key: custom_reviewer
  name: Custom Reviewer
lifecycle_hooks:
  after_complete:
    enabled: true
    skill: review_after
    output_contract: activity_summary
    payload:
      - conversation_transcript
      - assigned_agent
---

# Custom Reviewer
`)
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	hookDecl := decl.LifecycleHooks["after_complete"]
	if len(hookDecl.Payload) != 2 || hookDecl.Payload[0] != "conversation_transcript" || hookDecl.Payload[1] != "assigned_agent" {
		t.Fatalf("payload not parsed from declaration: %#v", hookDecl.Payload)
	}

	hooks := importHooks(decl)
	if len(hooks) != 1 {
		t.Fatalf("expected one imported hook, got %d", len(hooks))
	}
	if hooks[0].PayloadJSON != `{"blocks":["conversation_transcript","assigned_agent"]}` {
		t.Fatalf("payload did not reach the hook row: %q", hooks[0].PayloadJSON)
	}
}

// Declarations that omit payload keep an empty value, which the runtime reads
// as "send every context block".
func TestHookWithoutPayloadDeclarationStaysUnscoped(t *testing.T) {
	decl, _, err := ParseDeclaration(`---
kind: openvibely.agent_skill
version: 1
agent:
  key: custom_reviewer
  name: Custom Reviewer
lifecycle_hooks:
  after_complete:
    enabled: true
    skill: review_after
    output_contract: activity_summary
---

# Custom Reviewer
`)
	if err != nil {
		t.Fatalf("ParseDeclaration: %v", err)
	}
	hooks := importHooks(decl)
	if len(hooks) != 1 || hooks[0].PayloadJSON != "" {
		t.Fatalf("expected empty payload for undeclared hook, got %#v", hooks)
	}
}
