package agentskills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeSkill(t *testing.T, root, skill, body string) string {
	t.Helper()
	dir := filepath.Join(root, SkillsDir, skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, SkillFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	appendHeader(t, SkillsIndexPath(root), skill)
	return path
}

func writeAgentSkill(t *testing.T, root, agent, skill, body string) string {
	t.Helper()
	dir := filepath.Join(root, AgentRootsDir, agent, SkillsDir, skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, SkillFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	appendHeader(t, AgentSkillsIndexPath(root, agent), agent+"/"+skill)
	return path
}

func appendHeader(t *testing.T, path, header string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing, _ := os.ReadFile(path)
	if strings.Contains(string(existing), "\n## "+header+"\n") || strings.HasPrefix(string(existing), "## "+header+"\n") {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("## " + header + "\n\n"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCatalog_EnumeratesStandaloneHandlesFromIndexFiles(t *testing.T) {
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()

	writeSkill(t, globalRoot, "implement_change", "---\n---\n\nbody")
	writeSkill(t, globalRoot, "review_auth", "---\n---\n\nbody")
	writeSkill(t, projectRoot, "debug_go_tests", "---\n---\n\nbody")

	cat, err := BuildCatalog("turn-1", globalRoot, projectRoot)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	if cat.TurnID() != "turn-1" {
		t.Fatalf("want turn-1, got %q", cat.TurnID())
	}
	got := cat.Entries()
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d (%+v)", len(got), got)
	}
	dbg, ok := cat.Lookup("debug_go_tests")
	if !ok {
		t.Fatalf("expected debug_go_tests entry")
	}
	if dbg.Source != SourceProject {
		t.Fatalf("expected project source, got %s", dbg.Source)
	}
	if _, err := os.Stat(dbg.AbsolutePath); err != nil {
		t.Fatalf("AbsolutePath should exist on disk: %v", err)
	}
	if !strings.HasPrefix(dbg.AbsolutePath, projectRoot) {
		t.Fatalf("project entry should resolve under projectRoot, got %s", dbg.AbsolutePath)
	}
	raw, _ := json.Marshal(got)
	_ = raw
}

func TestBuildAgentCatalog_EnumeratesAssignedAgentSkills(t *testing.T) {
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()

	globalPath := writeAgentSkill(t, globalRoot, "task_agent", "debug", "global")
	projectPath := writeAgentSkill(t, projectRoot, "task_agent", "debug", "project")
	writeAgentSkill(t, projectRoot, "other_agent", "other", "other")
	writeSkill(t, projectRoot, "standalone", "standalone")

	cat, err := BuildAgentCatalog("turn-agent", globalRoot, projectRoot, "task_agent")
	if err != nil {
		t.Fatalf("build agent catalog: %v", err)
	}
	if !cat.IsAgentOwned() {
		t.Fatal("expected agent-owned catalog")
	}
	entry, ok := cat.Lookup("debug")
	if !ok {
		t.Fatal("expected debug agent skill")
	}
	if entry.Source != SourceAgent || entry.AgentKey != "task_agent" || entry.AbsolutePath != projectPath {
		t.Fatalf("bad agent entry: %+v projectPath=%s", entry, projectPath)
	}
	if entry.AbsolutePath == globalPath {
		t.Fatal("project agent skill should override global agent skill")
	}
	if _, ok := cat.Lookup("standalone"); ok {
		t.Fatal("standalone skill must not be in assigned-agent catalog")
	}
	if _, ok := cat.Lookup("other"); ok {
		t.Fatal("other agent skill must not be in assigned-agent catalog")
	}
}

func TestBuildAgentCatalog_EmptyCatalogStillReportsAgentOwned(t *testing.T) {
	cat, err := BuildAgentCatalog("turn-agent-empty", "", t.TempDir(), "task_agent")
	if err != nil {
		t.Fatalf("build empty agent catalog: %v", err)
	}
	if !cat.IsAgentOwned() {
		t.Fatal("empty assigned-agent catalog should still be scoped as agent-owned")
	}
	filtered := cat.Filter("turn-agent-empty:selected", []string{"missing"})
	if !filtered.IsAgentOwned() {
		t.Fatal("filtered empty assigned-agent catalog should preserve agent-owned scope")
	}
}

func TestCatalogEntriesForHandlesPreservesOrderAndEntryMetadata(t *testing.T) {
	catalog := NewCatalog("turn", []Entry{
		{Handle: "one", Skill: "one", Source: SourceProject},
		{Handle: "two", Skill: "two", Source: SourceGlobal},
		{Handle: "review", Skill: "review", Source: SourceAgent, AgentKey: "reviewer"},
	})

	got := catalog.EntriesForHandles([]string{" one ", "missing", "one", "", "review", "two"})
	if len(got) != 3 {
		t.Fatalf("expected three authorized entries, got %#v", got)
	}
	want := []Entry{
		{Handle: "one", Skill: "one", Source: SourceProject},
		{Handle: "review", Skill: "review", Source: SourceAgent, AgentKey: "reviewer"},
		{Handle: "two", Skill: "two", Source: SourceGlobal},
	}
	for i := range want {
		if got[i].Handle != want[i].Handle || got[i].Skill != want[i].Skill || got[i].Source != want[i].Source || got[i].AgentKey != want[i].AgentKey {
			t.Fatalf("entry %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if entries := (*Catalog)(nil).EntriesForHandles([]string{"one"}); entries != nil {
		t.Fatalf("nil catalog should produce no entries, got %#v", entries)
	}
	if entries := catalog.EntriesForHandles(nil); entries != nil {
		t.Fatalf("empty selection should produce no entries, got %#v", entries)
	}
}

func TestCatalogFilterPreservesTurnAndAgentOwnedScope(t *testing.T) {
	catalog := newCatalog("original", []Entry{{Handle: "review", Skill: "review", Source: SourceAgent, AgentKey: "reviewer"}}, true)
	filtered := catalog.Filter("filtered", []string{" review ", "review", "missing"})
	if filtered == nil {
		t.Fatal("expected filtered catalog")
	}
	if filtered.TurnID() != "filtered" {
		t.Fatalf("filtered turn id = %q, want filtered", filtered.TurnID())
	}
	if !filtered.IsAgentOwned() {
		t.Fatal("filtered catalog should preserve assigned-agent scope")
	}
	entries := filtered.Entries()
	if len(entries) != 1 || entries[0].Handle != "review" || entries[0].AgentKey != "reviewer" {
		t.Fatalf("filtered entries = %#v", entries)
	}
}

func TestBuildCatalog_ProjectOverridesGlobalForSameHandle(t *testing.T) {
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()

	globalPath := writeSkill(t, globalRoot, "debug_go_tests", "---\n---\n\nglobal")
	projectPath := writeSkill(t, projectRoot, "debug_go_tests", "---\n---\n\nproject")

	cat, err := BuildCatalog("t", globalRoot, projectRoot)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	entry, ok := cat.Lookup("debug_go_tests")
	if !ok {
		t.Fatalf("expected entry")
	}
	if entry.Source != SourceProject {
		t.Fatalf("project should win, got %s", entry.Source)
	}
	if entry.AbsolutePath != projectPath {
		t.Fatalf("expected project path %s, got %s", projectPath, entry.AbsolutePath)
	}
	if entry.AbsolutePath == globalPath {
		t.Fatalf("should not have resolved to global path")
	}
}

func TestBuildCatalog_LoadsTrackedOpenVibelyProjectGuidance(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))

	projectRoot := filepath.Join(repoRoot, ".openvibely")
	cat, err := BuildCatalog("t", "", projectRoot)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	entry, ok := cat.Lookup("openvibely_project_guidance")
	if !ok {
		t.Fatalf("expected tracked openvibely_project_guidance skill in project catalog")
	}
	if entry.Source != SourceProject {
		t.Fatalf("expected project-scoped guidance skill, got %s", entry.Source)
	}
	if !strings.HasPrefix(entry.AbsolutePath, filepath.Join(projectRoot, SkillsDir)) {
		t.Fatalf("expected guidance skill under project .openvibely/skills, got %s", entry.AbsolutePath)
	}
	body, err := os.ReadFile(entry.AbsolutePath)
	if err != nil {
		t.Fatalf("read guidance skill: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"Never delete, truncate, or overwrite `openvibely.db`",
		"After main Go app code changes, run the required validation chain",
		"./start.sh              # Start server",
		"| Entry point | `cmd/server/main.go` |",
		"`models`: plain structs and domain rules",
		"Chat bubbles and input containers should not use visible borders",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("guidance skill missing migrated guidance %q", want)
		}
	}
}

func TestBuiltInSDLCAutomationBootstrapSkillsAreDisabledForRuntimeRouting(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	builtinRoot := filepath.Clean(filepath.Join(wd, "..", "builtinskills", "builtin"))

	runtimeCatalog, err := BuildCatalog("runtime", builtinRoot, "")
	if err != nil {
		t.Fatalf("build runtime catalog: %v", err)
	}
	managementCatalog, err := BuildCatalogAll("management", builtinRoot, "")
	if err != nil {
		t.Fatalf("build management catalog: %v", err)
	}
	for _, handle := range []string{
		"openvibely_github_autonomous_sdlc_bootstrap",
		"openvibely_native_autonomous_sdlc_bootstrap",
	} {
		if _, ok := runtimeCatalog.Lookup(handle); ok {
			t.Errorf("disabled bootstrap skill %q must not be available to lifecycle routing", handle)
		}
		if _, ok := managementCatalog.Lookup(handle); !ok {
			t.Errorf("disabled bootstrap skill %q must remain visible to management", handle)
		}
	}
}

func TestBuiltInGitHubAutonomousSDLCBootstrapSkillContent(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	skillPath := filepath.Join(repoRoot, "internal", "builtinskills", "builtin", SkillsDir, "openvibely_github_autonomous_sdlc_bootstrap", SkillFile)
	body, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read built-in GitHub bootstrap skill: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"visible scheduled OpenVibely tasks",
		"generic GitHub runtime tools",
		"Do not create hidden daemon or poller services",
		"github_list_my_assigned_issues",
		"github_list_assigned_issues",
		"github_list_assigned_issues_with_prs",
		"github_open_pull_request",
		"github_forward_pr_feedback_to_tasks",
		"Never use labels beginning with `openvibely:`",
		"Assignment to the configured OpenVibely GitHub inbox identity is the default human approval signal to start work",
		"Assigned issues do not need an existing PR before automation may create OpenVibely implementation tasks",
		"Create one visible OpenVibely task per loop role; do not create separate one-off setup/runner tasks in addition to the scheduled loop tasks",
		"Do not call `set_task_goal` for recurring loop tasks during bootstrap",
		"Create `GitHub Offering Manager: Vision Suggestions` first and make it run immediately before creating downstream implementation schedules",
		"do not set a persisted goal on this recurring loop task",
		"attach their recurring schedules without setting persisted task goals",
		"Use `set_task_goal` only for implementation tasks that Dev Inbox creates from assigned GitHub issues",
		"do not add persisted goals to recurring loop tasks",
		"prompt memory alone",
		"github_list_existing_automation_issues",
		"github_get_issue",
		"skip covered candidates",
		"Do not start Dev Inbox or scanner/finder tasks as extra one-off setup work unless the user explicitly asks for an immediate poll/scan pass",
		"GitHub Bug Finder`",
		"GitHub Optimization Finder`",
		"GitHub Redundancy Finder`",
		"Offering/finder/scanner tasks open GitHub issues only",
		"Do not modify code, do not create OpenVibely implementation tasks, and do not open PRs",
		"The Dev Inbox is the default implementation gateway",
		"First call `github_forward_pr_feedback_to_tasks` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users",
		"forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback",
		"For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue",
		"If no existing task is evident from available task/thread context, call `create_task` immediately with `category=active`; do not wait for an existing PR",
		"Set `source_github_issue_number` to the exact issue number returned by this inbox execution",
		"Do not set `source_github_repo_url`",
		"then call `set_task_goal` for the created or reconciled task so it implements the issue",
		"Do not call `execute_tasks` for a newly created Active task",
		"For a reconciled existing task, call `execute_tasks` only when `list_tasks` shows category Backlog or status failed/cancelled",
		"Never call `execute_tasks` for an Active pending, queued, running, or completed task",
		"Always read assignee candidates with `github_get_project_inbox` and call `github_list_assigned_issues` for every returned Authorized User",
		"also use `github_list_my_assigned_issues` to include issues assigned only to the authenticated PAT user",
		"Deduplicate issues by repository plus issue number",
		"For GitHub App setups, do not treat the installation owner or organization as an issue assignee",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("GitHub bootstrap skill missing %q", want)
		}
	}
}

func TestNativeAutonomousSDLCDocsAlignWithBootstrapSkill(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))

	skillPath := filepath.Join(repoRoot, "internal", "builtinskills", "builtin", SkillsDir, "openvibely_native_autonomous_sdlc_bootstrap", SkillFile)
	skillBody, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read built-in Native bootstrap skill: %v", err)
	}
	skillText := string(skillBody)

	guidePath := filepath.Join(repoRoot, "docs", "openvibely-native-autonomous-sdlc-user-guide.md")
	guideBody, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read Native autonomous SDLC guide: %v", err)
	}
	guideText := string(guideBody)

	for _, want := range []string{
		"Vision Suggestions, Bug Finder, Optimization Finder, Redundancy Finder, and Notification Inbox",
		"Do not create separate runner tasks",
		"usually daily",
		"commonly hourly",
		"must not create implementation tasks or modify code",
		"list_existing_automation_notifications",
		"next_offset",
		"get_alert",
		"skip covered candidates",
		"at most one new notification",
		"Call `list_alerts` without `project_id`",
		"pass the `read` filter",
		"both read and unread approved notifications",
		"complete paginated snapshot",
		"call `execute_tasks` with that exact task ID",
		"Only after `execute_tasks` succeeds",
		"Never reuse a project ID from prior messages, examples, memory, or tool output",
		"create_alert_implementation_task",
		"The created task is the implementation task",
		"must not create or look for another implementation task",
		"run notification intake",
		"destructive remediation",
		"register_automation_resources",
		"`vision_suggestions`, `bug_finder`, `optimization_finder`, `redundancy_finder`, and `inbox`",
	} {
		if !strings.Contains(skillText, want) {
			t.Fatalf("Native bootstrap skill missing %q", want)
		}
		if !strings.Contains(guideText, want) {
			t.Fatalf("Native autonomous SDLC guide missing %q", want)
		}
	}

	for _, want := range []string{
		"Give each finder its own prompt",
		"one shared three-role menu",
		"bug_suggestion",
		"performance_suggestion",
		"maintenance_suggestion",
	} {
		if !strings.Contains(skillText, want) {
			t.Fatalf("Native bootstrap skill missing role-specific finder guidance %q", want)
		}
		if !strings.Contains(guideText, want) {
			t.Fatalf("Native autonomous SDLC guide missing role-specific finder guidance %q", want)
		}
	}

	for _, forbidden := range []string{
		"Native notification idempotency is the duplicate-prevention boundary",
		"Create a scheduled suggestion producer and a project-scoped approved-notification inbox",
		"A typical setup schedules the suggestion producer daily and the approved-notification inbox hourly",
	} {
		if strings.Contains(guideText, forbidden) {
			t.Fatalf("Native autonomous SDLC guide contains stale two-task guidance %q", forbidden)
		}
	}
}

func TestGitHubAutonomousSDLCDocsAlignWithBootstrapSkill(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))

	skillPath := filepath.Join(repoRoot, "internal", "builtinskills", "builtin", SkillsDir, "openvibely_github_autonomous_sdlc_bootstrap", SkillFile)
	skillBody, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read built-in GitHub bootstrap skill: %v", err)
	}
	skillText := string(skillBody)

	guidePath := filepath.Join(repoRoot, "docs", "github-autonomous-sdlc-user-guide.md")
	guideBody, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read GitHub autonomous SDLC guide: %v", err)
	}
	guideText := string(guideBody)

	for _, want := range []string{
		"visible scheduled OpenVibely tasks",
		"generic GitHub runtime tools",
		"Do not create hidden daemon or poller services",
		"For this Automation bootstrap, do not pass `repo_url` overrides",
		"Automation-bound GitHub tools resolve only the selected project's configured GitHub repository URL",
		"falling back to a GitHub remote in that project's local checkout",
		"github_list_my_assigned_issues",
		"github_list_assigned_issues",
		"github_list_assigned_issues_with_prs",
		"github_open_pull_request",
		"github_forward_pr_feedback_to_tasks",
		"Never use labels beginning with `openvibely:`",
		"Assignment to the configured OpenVibely GitHub inbox identity is the default human approval signal to start work",
		"Assigned issues do not need an existing PR before automation may create OpenVibely implementation tasks",
		"Create one visible OpenVibely task per loop role; do not create separate one-off setup/runner tasks in addition to the scheduled loop tasks",
		"Do not call `set_task_goal` for recurring loop tasks during bootstrap",
		"Create `GitHub Offering Manager: Vision Suggestions` first and make it run immediately before creating downstream implementation schedules",
		"do not set a persisted goal on this recurring loop task",
		"attach their recurring schedules without setting persisted task goals",
		"Use `set_task_goal` only for implementation tasks that Dev Inbox creates from assigned GitHub issues",
		"do not add persisted goals to recurring loop tasks",
		"prompt memory alone",
		"github_list_existing_automation_issues",
		"github_get_issue",
		"skip covered candidates",
		"Do not start Dev Inbox or scanner/finder tasks as extra one-off setup work unless the user explicitly asks for an immediate poll/scan pass",
		"GitHub Bug Finder`",
		"GitHub Optimization Finder`",
		"GitHub Redundancy Finder`",
		"Offering/finder/scanner tasks open GitHub issues only",
		"Do not modify code, do not create OpenVibely implementation tasks, and do not open PRs",
		"The Dev Inbox is the default implementation gateway",
		"First call `github_forward_pr_feedback_to_tasks` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users",
		"forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback",
		"For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue",
		"If no existing task is evident from available task/thread context, call `create_task` immediately with `category=active`; do not wait for an existing PR",
		"Set `source_github_issue_number` to the exact issue number returned by this inbox execution",
		"Do not set `source_github_repo_url`",
		"then call `set_task_goal` for the created or reconciled task so it implements the issue",
		"Do not call `execute_tasks` for a newly created Active task",
		"For a reconciled existing task, call `execute_tasks` only when `list_tasks` shows category Backlog or status failed/cancelled",
		"Never call `execute_tasks` for an Active pending, queued, running, or completed task",
	} {
		if !strings.Contains(skillText, want) {
			t.Fatalf("GitHub bootstrap skill missing %q", want)
		}
	}

	for _, want := range []string{
		"There is no hidden GitHub poller daemon",
		"GitHub Runtime Settings",
		"For this Automation loop, do not pass `repo_url` overrides",
		"Automation-bound GitHub tools use only the selected project's configured GitHub repository URL",
		"fall back to a GitHub remote in that project's local checkout",
		"github_list_my_assigned_issues",
		"github_list_assigned_issues",
		"github_list_assigned_issues_with_prs",
		"A PAT identifies a real GitHub user",
		"A GitHub App installation may be installed on an organization",
		"github_open_pull_request",
		"github_forward_pr_feedback_to_tasks",
		"Never use labels beginning with `openvibely:`",
		"Assignment to the PAT owner or configured Authorized User is the default approval signal",
		"assigned issues do not need an existing PR first",
		"Setup should create one visible task per loop role and schedule that same task",
		"Do not set persisted goals on recurring loop tasks; schedules drive the loop",
		"Create `GitHub Offering Manager: Vision Suggestions` first and run that same task immediately",
		"attach their recurring schedules without setting persisted task goals",
		"Do not create separate standalone one-off runner tasks in addition to the scheduled loop tasks",
		"Use `set_task_goal` only for implementation tasks that Dev Inbox creates from assigned GitHub issues",
		"Do not set a persisted goal on the Dev Inbox scheduled task itself",
		"Do not immediately start Dev Inbox or scanner/finder tasks during bootstrap unless the user explicitly asks for an immediate poll/scan pass",
		"`GitHub Offering Manager: Vision Suggestions` | Daily",
		"`GitHub Dev Inbox` | Hourly",
		"`GitHub Bug Finder` | Daily",
		"`GitHub Optimization Finder` | Daily",
		"`GitHub Redundancy Finder` | Daily",
		"These finder tasks open GitHub issues only; Dev Inbox remains the path that turns assigned issues into implementation tasks",
		"Offering, Bug Finder, Optimization Finder, and Redundancy Finder tasks should open issues only",
		"register_automation_resources",
		"`github_sdlc`",
		"`github-sdlc/default`",
		"`vision_suggestions`, `bug_finder`, `optimization_finder`, `redundancy_finder`, and `dev_inbox`",
		"github_list_existing_automation_issues",
		"github_get_issue",
		"skip that candidate and keep searching",
		"at most one new GitHub issue",
		"First call `github_forward_pr_feedback_to_tasks` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users",
		"forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback",
		"For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue",
		"If no existing task is evident from available task/thread context, call `create_task` immediately with `category=active`; do not wait for an existing PR",
		"Set `source_github_issue_number` to the exact issue number returned by this inbox execution",
		"Do not set `source_github_repo_url`",
		"then call `set_task_goal` for the created or reconciled task so it implements the issue",
		"Do not call `execute_tasks` for a newly created Active task",
		"For a reconciled existing task, call `execute_tasks` only when `list_tasks` shows category Backlog or status failed/cancelled",
		"Never call `execute_tasks` for an Active pending, queued, running, or completed task",
	} {
		if !strings.Contains(guideText, want) {
			t.Fatalf("GitHub autonomous SDLC guide missing %q", want)
		}
	}

	for _, text := range []struct {
		name string
		body string
	}{{name: "GitHub bootstrap skill", body: skillText}, {name: "GitHub autonomous SDLC guide", body: guideText}} {
		for _, want := range []string{
			"Never give one finder a menu of all three roles",
			"You are the Bug Finder.",
			"You are the Optimization Finder.",
			"You are the Redundancy Finder.",
			"`bug` label",
			"`performance` label",
			"`duplication` label",
		} {
			if !strings.Contains(text.body, want) {
				t.Fatalf("%s missing role-specific finder guidance %q", text.name, want)
			}
		}
	}

	for _, text := range []struct {
		name string
		body string
	}{{name: "GitHub bootstrap skill", body: skillText}, {name: "GitHub autonomous SDLC guide", body: guideText}} {
		for _, want := range []string{
			"Before creating any issue, call `github_list_existing_automation_issues`",
			"call `github_get_issue` for that issue and read the body",
			"skip that candidate and keep searching",
			"Try to create at most one new GitHub issue this run",
		} {
			if !strings.Contains(text.body, want) {
				t.Fatalf("%s must include existing-issue discovery guidance %q", text.name, want)
			}
		}
	}

	for _, forbidden := range []string{
		"idempotency_key",
		"Do not list, search, or inspect existing GitHub issues for duplicate detection",
		"Do not require a repository-wide issue or pull-request listing/search before publication",
		"Do not block publication because such a listing/search is unavailable, unauthenticated, incomplete, or unpaginated",
		"Avoid duplicates by searching or inspecting existing visible work",
		"Avoid duplicates by searching/inspecting existing visible work",
		"Start with two scheduled tasks before adding more scanner/finder loops",
		"You can later add Bug Finder, Optimization Finder, Redundancy Finder, and Loop Auditor tasks",
		"Loop Auditor",
		"optional Loop Auditor",
		"When a prompt names a specific GitHub repository URL, pass `repo_url` to issue create/read/list/comment/label tools",
		"prompts may pass `repo_url` when they name a specific GitHub repository URL",
	} {
		if strings.Contains(skillText, forbidden) || strings.Contains(guideText, forbidden) {
			t.Fatalf("GitHub bootstrap guidance contains stale or contradictory guidance %q", forbidden)
		}
	}

	indexBody, err := os.ReadFile(filepath.Join(repoRoot, "docs", "user-guides.md"))
	if err != nil {
		t.Fatalf("read user guide index: %v", err)
	}
	if !strings.Contains(string(indexBody), "[GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md)") {
		t.Fatalf("user guide index does not link GitHub autonomous SDLC guide")
	}

	githubSetupBody, err := os.ReadFile(filepath.Join(repoRoot, "docs", "github-channels-setup.md"))
	if err != nil {
		t.Fatalf("read GitHub channel setup guide: %v", err)
	}
	githubSetupText := string(githubSetupBody)
	if !strings.Contains(githubSetupText, "[GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md)") {
		t.Fatalf("GitHub channel setup guide does not link GitHub autonomous SDLC guide")
	}
	for _, want := range []string{
		"Ordinary non-Automation GitHub issue tools may accept `repo_url`",
		"Automation-bound GitHub tools ignore model-supplied `repo_url` overrides",
		"selected project's configured GitHub repository URL",
		"GitHub remote in that project's local checkout",
	} {
		if !strings.Contains(githubSetupText, want) {
			t.Fatalf("GitHub channel setup guide missing repository-boundary guidance %q", want)
		}
	}
	if strings.Contains(githubSetupText, "GitHub issue API tools default to the current project repository, but can accept `repo_url` when a prompt names a specific GitHub repository URL") {
		t.Fatalf("GitHub channel setup guide contains stale repository-override guidance")
	}
}

func TestBuildCatalog_IgnoresMissingRoots(t *testing.T) {
	cat, err := BuildCatalog("t", "/nonexistent/global", "/nonexistent/project")
	if err != nil {
		t.Fatalf("missing roots should be tolerated, got %v", err)
	}
	if len(cat.Entries()) != 0 {
		t.Fatalf("expected empty catalog, got %d entries", len(cat.Entries()))
	}
}

func TestBuildCatalog_ErrorsOnUnreadableSkillsIndex(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "ok", "---\n---\n\nbody")

	skillsPath := SkillsIndexPath(root)
	if err := os.Remove(skillsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skillsPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := BuildCatalog("t", root, ""); err == nil {
		t.Fatalf("expected read error for invalid SKILLS.md path")
	}
}

func TestBuildCatalog_RejectsHiddenTraversalAndAgentOwnedNames(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "ok", "---\n---\n\nbody")

	hidden := filepath.Join(root, SkillsDir, ".hidden")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, SkillFile), []byte("---\n---\n\nx"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendHeader(t, SkillsIndexPath(root), ".hidden")
	appendHeader(t, SkillsIndexPath(root), "agent/owned")

	cat, err := BuildCatalog("t", root, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := cat.Lookup(".hidden"); ok {
		t.Fatalf("hidden skill should be rejected")
	}
	if _, ok := cat.Lookup("agent/owned"); ok {
		t.Fatalf("agent-owned handle should not be routed as standalone")
	}
	if _, ok := cat.Lookup("ok"); !ok {
		t.Fatalf("valid handle should be present")
	}
}

func TestBuildCatalog_RequiresSkillBody(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "real", "---\n---\n\nbody")
	data, _ := os.ReadFile(SkillsIndexPath(root))
	tampered := string(data) + "\n## forged\n\n"
	if err := os.WriteFile(SkillsIndexPath(root), []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}

	cat, err := BuildCatalog("t", root, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := cat.Lookup("forged"); ok {
		t.Fatalf("forged handle without on-disk SKILL.md must not be authorized")
	}
	if _, ok := cat.Lookup("real"); !ok {
		t.Fatalf("real handle should still be authorized")
	}
}

func TestBuildCatalog_ExcludesDisabledSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "enabled_skill", "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: enabled_skill\n    enabled: true\n---\nbody")
	writeSkill(t, root, "disabled_skill", "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: disabled_skill\n    enabled: false\n---\nbody")
	writeSkill(t, root, "nil_enabled_skill", "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: nil_enabled_skill\n---\nbody")

	cat, err := BuildCatalog("t", root, "")
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	if _, ok := cat.Lookup("enabled_skill"); !ok {
		t.Fatalf("enabled skill must be in catalog")
	}
	if _, ok := cat.Lookup("nil_enabled_skill"); !ok {
		t.Fatalf("skill without enabled field must be in catalog")
	}
	if _, ok := cat.Lookup("disabled_skill"); ok {
		t.Fatalf("disabled skill must NOT be in runtime catalog")
	}
}

func TestBuildCatalog_FrontmatterFallbacksDefaultToEnabled(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "no_frontmatter", "# No frontmatter\nbody")
	writeSkill(t, root, "missing_enabled", "---\nskill:\n  key: missing_enabled\n---\nbody")
	writeSkill(t, root, "malformed_frontmatter", "---\nskill:\n  enabled: [\n---\nbody")

	cat, err := BuildCatalog("t", root, "")
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	for _, handle := range []string{"no_frontmatter", "missing_enabled", "malformed_frontmatter"} {
		if _, ok := cat.Lookup(handle); !ok {
			t.Fatalf("%s should default to enabled", handle)
		}
	}
}

func TestBuildCatalog_StandaloneProjectOverridePreservesDisabledFiltering(t *testing.T) {
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeDisabledSkill(t, globalRoot, "override_skill")
	projectPath := writeSkill(t, projectRoot, "override_skill", "---\nskill:\n  enabled: true\n---\nproject")

	cat, err := BuildCatalog("t", globalRoot, projectRoot)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	entry, ok := cat.Lookup("override_skill")
	if !ok {
		t.Fatal("project override should remain enabled")
	}
	if entry.Source != SourceProject || entry.AbsolutePath != projectPath {
		t.Fatalf("expected project override entry, got %+v", entry)
	}
}

func TestRenderAndSkillsList_SkipMissingSkillBody(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "real_skill", "body")
	appendHeader(t, SkillsIndexPath(root), "missing_skill")

	available := RenderAvailableSkillsMarkdown(root, "")
	if !strings.Contains(available, "## real_skill") {
		t.Fatalf("available skills missing real skill:\n%s", available)
	}
	if strings.Contains(available, "missing_skill") {
		t.Fatalf("available skills must skip missing body:\n%s", available)
	}

	cat, err := BuildCatalog("turn", root, "")
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	rt := SkillRuntimeTools(cat, root, "", nil)
	out, handled, isErr, execErr := rt.Executor(t.Context(), "skills_list", nil)
	if !handled || isErr || execErr != nil {
		t.Fatalf("skills_list failed handled=%v isErr=%v err=%v out=%q", handled, isErr, execErr, out)
	}
	if strings.Contains(out, "missing_skill") {
		t.Fatalf("skills_list must skip missing body:\n%s", out)
	}
	if !strings.Contains(out, "standalone:real_skill") {
		t.Fatalf("skills_list missing real skill view handle:\n%s", out)
	}
}

func TestBuildCatalogAll_IncludesDisabledSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "enabled_skill", "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: enabled_skill\n    enabled: true\n---\nbody")
	writeSkill(t, root, "disabled_skill", "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: disabled_skill\n    enabled: false\n---\nbody")

	cat, err := BuildCatalogAll("t", root, "")
	if err != nil {
		t.Fatalf("build catalog all: %v", err)
	}
	if _, ok := cat.Lookup("enabled_skill"); !ok {
		t.Fatalf("enabled skill must be in catalog")
	}
	if _, ok := cat.Lookup("disabled_skill"); !ok {
		t.Fatalf("disabled skill must appear in BuildCatalogAll for management UI")
	}
	if len(cat.Entries()) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(cat.Entries()))
	}
}

func TestBuildAgentCatalog_ExcludesDisabledAgentSkills(t *testing.T) {
	root := t.TempDir()
	writeAgentSkill(t, root, "task_agent", "enabled_skill", "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: enabled_skill\n    enabled: true\n---\nbody")
	writeAgentSkill(t, root, "task_agent", "disabled_skill", "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: disabled_skill\n    enabled: false\n---\nbody")

	cat, err := BuildAgentCatalog("t", root, "", "task_agent")
	if err != nil {
		t.Fatalf("build agent catalog: %v", err)
	}
	if _, ok := cat.Lookup("enabled_skill"); !ok {
		t.Fatalf("enabled agent skill must be in catalog")
	}
	if _, ok := cat.Lookup("disabled_skill"); ok {
		t.Fatalf("disabled agent skill must NOT be in runtime catalog")
	}
}

// writeDisabledSkill writes a SKILL.md with skill.enabled: false frontmatter
// and registers the handle in the appropriate SKILLS.md index.
func writeDisabledSkill(t *testing.T, root, skill string) string {
	t.Helper()
	dir := filepath.Join(root, SkillsDir, skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, SkillFile)
	body := "---\nskill:\n  enabled: false\n---\n# " + skill + "\nDisabled skill body.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	appendHeader(t, SkillsIndexPath(root), skill)
	return path
}

// writeDisabledAgentSkill writes a disabled agent-owned SKILL.md.
func writeDisabledAgentSkill(t *testing.T, root, agent, skill string) string {
	t.Helper()
	dir := filepath.Join(root, AgentRootsDir, agent, SkillsDir, skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, SkillFile)
	body := "---\nskill:\n  enabled: false\n---\n# " + skill + "\nDisabled agent skill body.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	appendHeader(t, AgentSkillsIndexPath(root, agent), agent+"/"+skill)
	return path
}

func TestRenderAvailableSkillsMarkdown_ReturnsRawIndexContent(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeSkill(t, global, "implement_change", "body")
	writeSkill(t, project, "debug_go_tests", "body")

	out := RenderAvailableSkillsMarkdown(global, project)
	if !strings.Contains(out, "<available_skills>") || !strings.Contains(out, "## implement_change") || !strings.Contains(out, "## debug_go_tests") {
		t.Fatalf("rendered block missing index content:\n%s", out)
	}
	if strings.Contains(out, filepath.Join(global, SkillsDir)) || strings.Contains(out, filepath.Join(project, SkillsDir)) {
		t.Fatalf("rendered block leaked filesystem path:\n%s", out)
	}
}

func TestRenderAvailableSkillsMarkdown_ExcludesDisabledHandles(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()

	// enabled skill in global scope
	writeSkill(t, global, "enabled_global", "body")
	// disabled skill in global scope — must NOT appear in rendered output
	writeDisabledSkill(t, global, "disabled_global")
	// enabled skill in project scope
	writeSkill(t, project, "enabled_project", "body")
	// disabled skill in project scope — must NOT appear in rendered output
	writeDisabledSkill(t, project, "disabled_project")

	out := RenderAvailableSkillsMarkdown(global, project)

	if !strings.Contains(out, "## enabled_global") {
		t.Errorf("expected enabled_global to be present:\n%s", out)
	}
	if !strings.Contains(out, "## enabled_project") {
		t.Errorf("expected enabled_project to be present:\n%s", out)
	}
	if strings.Contains(out, "## disabled_global") {
		t.Errorf("disabled_global must NOT appear in available_skills block:\n%s", out)
	}
	if strings.Contains(out, "## disabled_project") {
		t.Errorf("disabled_project must NOT appear in available_skills block:\n%s", out)
	}
}

func TestRenderAvailableSkillsMarkdown_AllDisabledProducesEmptyFallback(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	writeDisabledSkill(t, global, "disabled_only")
	writeDisabledSkill(t, project, "also_disabled")

	out := RenderAvailableSkillsMarkdown(global, project)

	if strings.Contains(out, "## disabled_only") || strings.Contains(out, "## also_disabled") {
		t.Errorf("disabled skills must NOT appear:\n%s", out)
	}
	if !strings.Contains(out, "_No standalone skills indexed in this turn._") {
		t.Errorf("expected fallback message when all skills are disabled:\n%s", out)
	}
}

func TestRenderAvailableAgentSkillsMarkdown_ExcludesDisabledHandles(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	const agent = "myagent"

	// enabled agent-owned skill
	writeAgentSkill(t, global, agent, "enabled_skill", "body")
	// disabled agent-owned skill — must NOT appear in rendered output
	writeDisabledAgentSkill(t, global, agent, "disabled_skill")
	// another enabled skill in project scope
	writeAgentSkill(t, project, agent, "project_skill", "body")

	out := RenderAvailableAgentSkillsMarkdown(global, project, agent)

	if !strings.Contains(out, "## "+agent+"/enabled_skill") {
		t.Errorf("expected enabled_skill present:\n%s", out)
	}
	if !strings.Contains(out, "## "+agent+"/project_skill") {
		t.Errorf("expected project_skill present:\n%s", out)
	}
	if strings.Contains(out, "## "+agent+"/disabled_skill") {
		t.Errorf("disabled_skill must NOT appear in available_skills block:\n%s", out)
	}
}

// TestRenderAvailableSkillsMarkdown_DoesNotLeakAlwaysUseFrontmatter verifies
// that a SKILLS.md file containing an always_use frontmatter block does not
// emit that block into the model-visible available_skills rendering. The
// frontmatter is catalog policy metadata, not model instructions.
func TestRenderAvailableSkillsMarkdown_DoesNotLeakAlwaysUseFrontmatter(t *testing.T) {
	root := t.TempDir()

	// Write a skill so the index has an H2 entry.
	writeSkill(t, root, "guidance_skill", "skill body here")

	// Prepend always_use frontmatter to the SKILLS.md that writeSkill created.
	indexPath := SkillsIndexPath(root)
	existing, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read SKILLS.md: %v", err)
	}
	withFrontmatter := "---\nalways_use:\n  - guidance_skill\n---\n\n" + string(existing)
	if writeErr := os.WriteFile(indexPath, []byte(withFrontmatter), 0o644); writeErr != nil {
		t.Fatalf("write SKILLS.md with frontmatter: %v", writeErr)
	}

	out := RenderAvailableSkillsMarkdown("", root)

	// The skill H2 entry must be visible.
	if !strings.Contains(out, "## guidance_skill") {
		t.Errorf("expected guidance_skill H2 to appear in output:\n%s", out)
	}
	// The frontmatter YAML must NOT appear in the model-visible block.
	if strings.Contains(out, "always_use") {
		t.Errorf("always_use frontmatter must NOT be leaked into model-visible available_skills:\n%s", out)
	}
	if strings.Contains(out, "---") {
		t.Errorf("frontmatter delimiters must NOT be present in model-visible available_skills:\n%s", out)
	}
}

const (
	benchmarkSkillCount    = 200
	benchmarkSkillBodySize = 128 * 1024
)

func BenchmarkBuildCatalogProductionShape(b *testing.B) {
	root := writeBenchmarkSkillRoot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cat, err := BuildCatalog("bench", root, "")
		if err != nil {
			b.Fatalf("BuildCatalog: %v", err)
		}
		if got := len(cat.Entries()); got != benchmarkSkillCount {
			b.Fatalf("entries = %d, want %d", got, benchmarkSkillCount)
		}
	}
}

func BenchmarkBuildCatalogLegacyFullBodyDisabledCheckProductionShape(b *testing.B) {
	root := writeBenchmarkSkillRoot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cat, err := legacyBuildCatalogFullBodyDisabledCheck("bench", root)
		if err != nil {
			b.Fatalf("legacy build catalog: %v", err)
		}
		if got := len(cat.Entries()); got != benchmarkSkillCount {
			b.Fatalf("entries = %d, want %d", got, benchmarkSkillCount)
		}
	}
}

func BenchmarkLifecycleCatalogAndAvailableIndexProductionShape(b *testing.B) {
	root := writeBenchmarkSkillRoot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cat, err := BuildCatalog("bench", root, "")
		if err != nil {
			b.Fatalf("BuildCatalog: %v", err)
		}
		if got := len(cat.Entries()); got != benchmarkSkillCount {
			b.Fatalf("entries = %d, want %d", got, benchmarkSkillCount)
		}
		out := RenderAvailableSkillsMarkdown(root, "")
		if !strings.Contains(out, "## skill_000") || !strings.Contains(out, "## skill_199") {
			b.Fatalf("rendered index missing expected handles")
		}
	}
}

func BenchmarkLifecycleCatalogAndAvailableIndexLegacyFullBodyDisabledCheckProductionShape(b *testing.B) {
	root := writeBenchmarkSkillRoot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cat, err := legacyBuildCatalogFullBodyDisabledCheck("bench", root)
		if err != nil {
			b.Fatalf("legacy build catalog: %v", err)
		}
		if got := len(cat.Entries()); got != benchmarkSkillCount {
			b.Fatalf("entries = %d, want %d", got, benchmarkSkillCount)
		}
		out := legacyRenderAvailableSkillsMarkdownFullBodyDisabledCheck(root)
		if !strings.Contains(out, "## skill_000") || !strings.Contains(out, "## skill_199") {
			b.Fatalf("rendered index missing expected handles")
		}
	}
}

func writeBenchmarkSkillRoot(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	bodyPadding := strings.Repeat("x", benchmarkSkillBodySize)
	for i := 0; i < benchmarkSkillCount; i++ {
		handle := fmt.Sprintf("skill_%03d", i)
		dir := filepath.Join(root, SkillsDir, handle)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("mkdir %s: %v", dir, err)
		}
		body := fmt.Sprintf("---\nskill:\n  key: %s\n  enabled: true\n---\n%s", handle, bodyPadding)
		if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(body), 0o644); err != nil {
			b.Fatalf("write skill %s: %v", handle, err)
		}
		appendBenchmarkHeader(b, SkillsIndexPath(root), handle)
	}
	return root
}

func appendBenchmarkHeader(b *testing.B, path, header string) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("## " + header + "\n\n"); err != nil {
		b.Fatal(err)
	}
}

func legacyBuildCatalogFullBodyDisabledCheck(turnID, root string) (*Catalog, error) {
	entries, err := legacyLoadSkillIndexEntriesFullBodyDisabledCheck(SkillsIndexPath(root), filepath.Join(root, SkillsDir))
	if err != nil {
		return nil, err
	}
	return NewCatalog(turnID, entries), nil
}

func legacyLoadSkillIndexEntriesFullBodyDisabledCheck(indexPath, skillsDir string) ([]Entry, error) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, benchmarkSkillCount)
	for _, skill := range extractH2Headers(string(data)) {
		if strings.Contains(skill, "/") || !isValidSlug(skill) {
			continue
		}
		absPath := filepath.Join(skillsDir, skill, SkillFile)
		if _, err := os.Stat(absPath); err != nil {
			continue
		}
		if legacySkillDisabledOnDiskFullBody(absPath) {
			continue
		}
		out = append(out, Entry{Handle: skill, Skill: skill, Source: SourceGlobal, AbsolutePath: absPath})
	}
	return out, nil
}

func legacyRenderAvailableSkillsMarkdownFullBodyDisabledCheck(root string) string {
	body, ok := legacyFilteredIndexBodyFullBodyDisabledCheck(SkillsIndexPath(root), filepath.Join(root, SkillsDir))
	if !ok {
		return ""
	}
	return body
}

func legacyFilteredIndexBodyFullBodyDisabledCheck(indexPath, skillsDir string) (string, bool) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return "", false
	}
	content := string(data)
	type section struct {
		body  string
		skill string
	}
	headerLocs := h2HeaderRegexp.FindAllStringIndex(content, -1)
	headerMatches := h2HeaderRegexp.FindAllStringSubmatch(content, -1)
	sections := make([]section, 0, len(headerLocs))
	for i, loc := range headerLocs {
		skill := strings.TrimSpace(headerMatches[i][1])
		if strings.Contains(skill, "/") || !isValidSlug(skill) {
			continue
		}
		end := len(content)
		if i+1 < len(headerLocs) {
			end = headerLocs[i+1][0]
		}
		sections = append(sections, section{body: content[loc[0]:end], skill: skill})
	}
	var sb strings.Builder
	for _, sec := range sections {
		absPath := filepath.Join(skillsDir, sec.skill, SkillFile)
		if legacySkillDisabledOnDiskFullBody(absPath) {
			continue
		}
		sb.WriteString(sec.body)
	}
	out := sb.String()
	return out, out != ""
}

func legacySkillDisabledOnDiskFullBody(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return false
	}
	rest := strings.TrimPrefix(content, "---")
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false
	}
	front := rest[:end]
	var parsed struct {
		Skill struct {
			Enabled *bool `yaml:"enabled"`
		} `yaml:"skill"`
	}
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		return false
	}
	return parsed.Skill.Enabled != nil && !*parsed.Skill.Enabled
}
