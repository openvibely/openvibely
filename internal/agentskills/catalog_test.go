package agentskills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"implementation-task goals for per-issue work records",
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
		"If no existing task is evident from available task/thread context, call `create_task` immediately; do not wait for an existing PR",
		"then call `set_task_goal` for the created task so it implements the issue",
		"For PAT setups, use `github_list_my_assigned_issues` to find open issues assigned to the authenticated PAT user",
		"For GitHub App setups, do not treat the installation owner or organization as an issue assignee",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("GitHub bootstrap skill missing %q", want)
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
		"implementation-task goals for per-issue work records",
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
		"If no existing task is evident from available task/thread context, call `create_task` immediately; do not wait for an existing PR",
		"then call `set_task_goal` for the created task so it implements the issue",
	} {
		if !strings.Contains(skillText, want) {
			t.Fatalf("GitHub bootstrap skill missing %q", want)
		}
	}

	for _, want := range []string{
		"There is no hidden GitHub poller daemon",
		"GitHub Runtime Settings",
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
		"Bug Finder, Optimization Finder, Redundancy Finder, and Loop Auditor tasks",
		"These finder tasks open GitHub issues only; Dev Inbox remains the path that turns assigned issues into implementation tasks",
		"Offering, Bug Finder, Optimization Finder, and Redundancy Finder tasks should open issues only",
		"Do not list, search, or inspect existing GitHub issues for duplicate detection",
		"the server prevents duplicate Automation-created issues using trusted local state",
		"First call `github_forward_pr_feedback_to_tasks` to fetch new pull request comments, review summaries, and review comments from GitHub Authorized Users",
		"forwards each new authorized feedback item to the linked implementation task thread and deduplicates previously forwarded feedback",
		"For each actionable issue, create or continue a distinct visible OpenVibely implementation task for that GitHub issue",
		"If no existing task is evident from available task/thread context, call `create_task` immediately; do not wait for an existing PR",
		"then call `set_task_goal` for the created task so it implements the issue",
	} {
		if !strings.Contains(guideText, want) {
			t.Fatalf("GitHub autonomous SDLC guide missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"Avoid duplicates by searching or inspecting existing visible work",
		"Avoid duplicates by searching/inspecting existing visible work",
	} {
		if strings.Contains(guideText, forbidden) {
			t.Fatalf("GitHub autonomous SDLC guide contains stale duplicate-search guidance %q", forbidden)
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
	if !strings.Contains(string(githubSetupBody), "[GitHub Autonomous SDLC User Guide](./github-autonomous-sdlc-user-guide.md)") {
		t.Fatalf("GitHub channel setup guide does not link GitHub autonomous SDLC guide")
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
