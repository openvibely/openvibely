package builtinskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
)

func TestSyncTo_SeedsDefaultIndexesAndSkillBodies(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	mustExist := []string{
		filepath.Join(root, "agents", "AGENTS.md"),
		filepath.Join(root, "agents", "skill_curator", "SKILLS.md"),
		filepath.Join(root, "agents", "skill_curator", "skills", "route_task", "SKILL.md"),
		filepath.Join(root, "agents", "skill_curator", "skills", "observe_task_for_learning", "SKILL.md"),
		filepath.Join(root, "agents", "skill_curator", "skills", "maintain_skill_library", "SKILL.md"),
		filepath.Join(root, "agents", "goal", "SKILLS.md"),
		filepath.Join(root, "agents", "goal", "skills", "evaluate_task_goal", "SKILL.md"),
		filepath.Join(root, "agents", "memory_curator", "SKILLS.md"),
		filepath.Join(root, "agents", "memory_curator", "skills", "recall_memory", "SKILL.md"),
		filepath.Join(root, "skills", "SKILLS.md"),
		filepath.Join(root, "skills", "openvibely_github_autonomous_sdlc_bootstrap", "SKILL.md"),
		filepath.Join(root, "skills", "openvibely_native_autonomous_sdlc_bootstrap", "SKILL.md"),
	}
	for _, p := range mustExist {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s after SyncTo: %v", p, err)
		}
	}

	agents, err := os.ReadFile(filepath.Join(root, "agents", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "## skill_curator") {
		t.Fatalf("AGENTS.md missing ## skill_curator header:\n%s", agents)
	}
	if !strings.Contains(string(agents), "## goal") {
		t.Fatalf("AGENTS.md missing ## goal header:\n%s", agents)
	}

	skills, err := os.ReadFile(filepath.Join(root, "agents", "skill_curator", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read SKILLS.md: %v", err)
	}
	for _, want := range []string{
		"blocking: false",
		"## skill_curator/route_task",
		"skills/route_task/SKILL.md",
		"## skill_curator/observe_task_for_learning",
		"skills/observe_task_for_learning/SKILL.md",
		"## skill_curator/maintain_skill_library",
		"skills/maintain_skill_library/SKILL.md",
	} {
		if !strings.Contains(string(skills), want) {
			t.Fatalf("SKILLS.md missing %q:\n%s", want, skills)
		}
	}

	observe, err := os.ReadFile(filepath.Join(root, "agents", "skill_curator", "skills", "observe_task_for_learning", "SKILL.md"))
	if err != nil {
		t.Fatalf("read observe_task_for_learning SKILL.md: %v", err)
	}
	for _, want := range []string{"Missing skill coverage is not a no-op reason", "What Belongs In A Skill", "consolidate immediately", "skill_manage(action=archive"} {
		if !strings.Contains(string(observe), want) {
			t.Fatalf("observe_task_for_learning missing %q:\n%s", want, observe)
		}
	}
}

func TestSyncTo_DisablesSDLCAutomationBootstrapSkills(t *testing.T) {
	root := t.TempDir()
	for _, handle := range []string{
		"openvibely_github_autonomous_sdlc_bootstrap",
		"openvibely_native_autonomous_sdlc_bootstrap",
	} {
		path := filepath.Join(root, "skills", handle, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create stale skill directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("---\nskill:\n    key: "+handle+"\n    enabled: true\n---\nstale"), 0o644); err != nil {
			t.Fatalf("write stale enabled skill: %v", err)
		}
	}

	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	for _, handle := range []string{
		"openvibely_github_autonomous_sdlc_bootstrap",
		"openvibely_native_autonomous_sdlc_bootstrap",
	} {
		path := filepath.Join(root, "skills", handle, "SKILL.md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read synced skill %q: %v", handle, err)
		}
		if !strings.Contains(string(body), "enabled: false") {
			t.Errorf("synced bootstrap skill %q must be disabled:\n%s", handle, body)
		}
	}

	runtimeCatalog, err := agentskills.BuildCatalog("runtime", root, "")
	if err != nil {
		t.Fatalf("build synced runtime catalog: %v", err)
	}
	for _, handle := range []string{
		"openvibely_github_autonomous_sdlc_bootstrap",
		"openvibely_native_autonomous_sdlc_bootstrap",
	} {
		if _, ok := runtimeCatalog.Lookup(handle); ok {
			t.Errorf("synced disabled bootstrap skill %q must not be routable", handle)
		}
	}
}

func TestSyncTo_GitHubBootstrapUsesProjectRepositoryAndExistingIssueDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "skills", "openvibely_github_autonomous_sdlc_bootstrap", "SKILL.md"))
	if err != nil {
		t.Fatalf("read GitHub bootstrap skill: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"Do not set `source_github_repo_url`",
		"the selected project's configured repository URL",
		"a GitHub remote in its local checkout when that URL is blank",
		"github_list_existing_automation_issues",
		"github_get_issue",
		"skip that candidate and keep searching",
		"at most one new GitHub issue",
		"idempotency_key",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("GitHub bootstrap skill missing repository/deduplication guidance %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{
		"set `source_github_repo_url` when polling a non-default repository",
		"Do not list, search, or inspect existing GitHub issues for duplicate detection",
		"Do not require a repository-wide issue or pull-request listing/search before publication",
		"Avoid duplicates by searching/inspecting existing visible work",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("GitHub bootstrap skill contains stale guidance %q:\n%s", forbidden, body)
		}
	}
}

func TestSyncTo_BundledSystemPromptsAvoidUnnecessaryInternalLabelInjection(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	paths := []string{
		filepath.Join(root, "agents", "AGENTS.md"),
		filepath.Join(root, "agents", "skill_curator", "SKILLS.md"),
		filepath.Join(root, "agents", "skill_curator", "skills", "maintain_skill_library", "SKILL.md"),
		filepath.Join(root, "agents", "skill_curator", "skills", "observe_task_for_learning", "SKILL.md"),
		filepath.Join(root, "agents", "memory_curator", "SKILLS.md"),
		filepath.Join(root, "agents", "memory_curator", "skills", "consolidate_memory", "SKILL.md"),
		filepath.Join(root, "agents", "memory_curator", "skills", "update_memory", "SKILL.md"),
		filepath.Join(root, "agents", "goal", "SKILLS.md"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		body := string(data)
		for _, forbidden := range []string{"ships with OpenVibely", "OpenVibely managed project memory", "OpenVibely skills", "Built-in system agent", "built-in system agent", "generated skill", "generated skills"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("bundled prompt %s contains unnecessary internal/product wording %q:\n%s", p, forbidden, body)
			}
		}
	}
}

func TestSyncTo_GoalAgentSkillUsesTranscriptEvidenceNotOnlyFinalClaims(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "agents", "goal", "skills", "evaluate_task_goal", "SKILL.md"))
	if err != nil {
		t.Fatalf("read evaluate_task_goal SKILL.md: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"not only the assistant's final completion claim",
		"explicit task-agent statements about actions taken, files changed, commands run, validation performed, or remaining issues",
		"If a goal requires that some action did not happen, assistant text that says the action happened is evidence that condition is not proven by that turn",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Goal Agent prompt missing evidence guidance %q:\n%s", want, body)
		}
	}
}

func TestSyncTo_PreservesUserManagedIndexesButRefreshesSystemDeclaration(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatalf("initial SyncTo: %v", err)
	}

	agentsPath := filepath.Join(root, "agents", "AGENTS.md")
	skillsPath := filepath.Join(root, "agents", "skill_curator", "SKILLS.md")
	custom := "# Custom Index\n\n## skill_curator\n\nEdited by the LLM.\n"
	if err := os.WriteFile(agentsPath, []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom AGENTS.md: %v", err)
	}
	customSkills := "# Custom Skills Index\n\n## skill_curator/route_task\n\nEdited.\n"
	if err := os.WriteFile(skillsPath, []byte(customSkills), 0o644); err != nil {
		t.Fatalf("write custom SKILLS.md: %v", err)
	}

	if err := SyncTo(root); err != nil {
		t.Fatalf("second SyncTo: %v", err)
	}

	got, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("re-read AGENTS.md: %v", err)
	}
	if string(got) != custom {
		t.Fatalf("AGENTS.md clobbered by SyncTo.\nwant:\n%s\ngot:\n%s", custom, got)
	}
	gotSkills, err := os.ReadFile(skillsPath)
	if err != nil {
		t.Fatalf("re-read SKILLS.md: %v", err)
	}
	if string(gotSkills) == customSkills {
		t.Fatalf("protected system skill_curator/SKILLS.md was not refreshed by SyncTo")
	}
	if !strings.Contains(string(gotSkills), "# Skill Curator") || !strings.Contains(string(gotSkills), "skill_manage") {
		t.Fatalf("refreshed Skill Curator SKILLS.md missing expected policy/tools:\n%s", gotSkills)
	}

	userAgentDir := filepath.Join(root, "agents", "backend")
	if err := os.MkdirAll(userAgentDir, 0o755); err != nil {
		t.Fatalf("mkdir backend: %v", err)
	}
	userSkillsPath := filepath.Join(userAgentDir, "SKILLS.md")
	userSkills := "# Backend Skills\n\n## backend/build\n\nEdited by user.\n"
	if err := os.WriteFile(userSkillsPath, []byte(userSkills), 0o644); err != nil {
		t.Fatalf("write backend SKILLS.md: %v", err)
	}
	if err := SyncTo(root); err != nil {
		t.Fatalf("third SyncTo: %v", err)
	}
	gotUserSkills, err := os.ReadFile(userSkillsPath)
	if err != nil {
		t.Fatalf("re-read backend SKILLS.md: %v", err)
	}
	if string(gotUserSkills) != userSkills {
		t.Fatalf("user-managed SKILLS.md clobbered by SyncTo.\nwant:\n%s\ngot:\n%s", userSkills, gotUserSkills)
	}
}

func TestSyncTo_MemoryRecallSkillUsesIndexSelectionContract(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	recall, err := os.ReadFile(filepath.Join(root, "agents", "memory_curator", "skills", "recall_memory", "SKILL.md"))
	if err != nil {
		t.Fatalf("read recall_memory SKILL.md: %v", err)
	}
	decl, err := os.ReadFile(filepath.Join(root, "agents", "memory_curator", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read memory_curator SKILLS.md: %v", err)
	}
	declBody := string(decl)
	for _, want := range []string{"route_task:", "skill: recall_memory", "blocking: false", "output_contract: selected_memories", "- ScopedFiles", "- memory_view"} {
		if !strings.Contains(declBody, want) {
			t.Fatalf("memory_curator declaration missing route selected-memory hook %q:\n%s", want, declBody)
		}
	}
	if strings.Contains(declBody, "output_contract: context_block") {
		t.Fatalf("memory_curator recall should not install as before_run context_block:\n%s", declBody)
	}

	body := string(recall)
	for _, want := range []string{
		"route_task",
		"selected_memories",
		"extras.available_memories",
		"compact project memory index from `MEMORIES.md`",
		"analogous to the Skill Curator's `available_skills` index",
		"If no listed memory is relevant",
		"Use root-relative memory filenames from the index as `file` handles",
		"Leave `content`, `summary`, and `snippet` empty",
		"memory_view",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("recall_memory skill missing index-selection instruction %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"read only the topic files", "bounded `read_file`", "read all memory files", "read every memory file", "dump all memory"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("recall_memory skill should not encourage broad memory dumps via %q:\n%s", forbidden, body)
		}
	}
}

func TestSyncTo_OverwritesSkillBodies(t *testing.T) {
	root := t.TempDir()
	if err := SyncTo(root); err != nil {
		t.Fatalf("initial SyncTo: %v", err)
	}

	skillPath := filepath.Join(root, "agents", "skill_curator", "skills", "route_task", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("stale content"), 0o644); err != nil {
		t.Fatalf("write stale SKILL.md: %v", err)
	}

	if err := SyncTo(root); err != nil {
		t.Fatalf("second SyncTo: %v", err)
	}

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("re-read SKILL.md: %v", err)
	}
	if string(got) == "stale content" {
		t.Fatalf("SKILL.md not refreshed by SyncTo; still contains stale content")
	}
}

func TestSyncTo_EmptyRootIsNoop(t *testing.T) {
	if err := SyncTo(""); err != nil {
		t.Fatalf("SyncTo(\"\"): %v", err)
	}
}

func TestSyncTo_InstallsDisabledGitHubBootstrapAsGlobalStandaloneSkill(t *testing.T) {
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()
	if err := SyncTo(globalRoot); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	runtimeCatalog, err := agentskills.BuildCatalog("test-turn", globalRoot, projectRoot)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	if _, ok := runtimeCatalog.Lookup("openvibely_github_autonomous_sdlc_bootstrap"); ok {
		t.Fatal("disabled bundled GitHub bootstrap skill must not be in runtime catalog")
	}
	managementCatalog, err := agentskills.BuildCatalogAll("test-turn", globalRoot, projectRoot)
	if err != nil {
		t.Fatalf("BuildCatalogAll: %v", err)
	}
	entry, ok := managementCatalog.Lookup("openvibely_github_autonomous_sdlc_bootstrap")
	if !ok {
		t.Fatalf("expected disabled bundled GitHub autonomous SDLC bootstrap skill in management catalog")
	}
	if entry.Source != agentskills.SourceGlobal {
		t.Fatalf("expected bundled bootstrap skill to be global, got %s", entry.Source)
	}
	if !strings.HasPrefix(entry.AbsolutePath, filepath.Join(globalRoot, "skills", "openvibely_github_autonomous_sdlc_bootstrap")) {
		t.Fatalf("expected bundled bootstrap under global root, got %s", entry.AbsolutePath)
	}

	body, err := os.ReadFile(entry.AbsolutePath)
	if err != nil {
		t.Fatalf("read global bootstrap skill: %v", err)
	}
	for _, want := range []string{"scope: global", "enabled: false", "github_open_pull_request", "visible scheduled OpenVibely tasks"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("global bootstrap skill missing %q:\n%s", want, string(body))
		}
	}
}

func TestSyncTo_MergesStandaloneSkillsIndexWithoutClobberingUserEntries(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, "skills", "SKILLS.md")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	custom := "---\nalways_use:\n    - existing_skill\n---\n\n# Custom Skills\n\n## existing_skill\n\n[Existing Skill](existing_skill/SKILL.md) — User-managed entry.\n"
	if err := os.WriteFile(indexPath, []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom SKILLS.md: %v", err)
	}

	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	gotBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read merged SKILLS.md: %v", err)
	}
	got := string(gotBytes)
	for _, want := range []string{
		"always_use:\n    - existing_skill",
		"## existing_skill",
		"User-managed entry.",
		"## openvibely_github_autonomous_sdlc_bootstrap",
		"OpenVibely GitHub Autonomous SDLC Bootstrap",
		"## openvibely_native_autonomous_sdlc_bootstrap",
		"OpenVibely Native Autonomous SDLC Bootstrap",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged SKILLS.md missing %q:\n%s", want, got)
		}
	}

	if err := SyncTo(root); err != nil {
		t.Fatalf("second SyncTo: %v", err)
	}
	gotAgainBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read merged SKILLS.md after second sync: %v", err)
	}
	if strings.Count(string(gotAgainBytes), "## openvibely_github_autonomous_sdlc_bootstrap") != 1 || strings.Count(string(gotAgainBytes), "## openvibely_native_autonomous_sdlc_bootstrap") != 1 {
		t.Fatalf("built-in bootstrap entries should be merged idempotently:\n%s", string(gotAgainBytes))
	}
}

// TestSyncTo_InstallsSystemHookSkillsButKeepsDisabledStandaloneSkillsOutOfRoutableCatalog
// verifies that built-in system hook skills remain agent-owned implementation
// details and disabled built-in standalone skills remain management-visible only.
func TestSyncTo_InstallsSystemHookSkillsButKeepsDisabledStandaloneSkillsOutOfRoutableCatalog(t *testing.T) {
	root := t.TempDir()
	if err := agentskills.EnsureAgentsRoot(root); err != nil {
		t.Fatalf("EnsureAgentsRoot: %v", err)
	}
	if err := SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	cat, err := agentskills.BuildCatalog("test-turn", root, "")
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	if entries := cat.Entries(); len(entries) != 0 {
		t.Fatalf("expected no routable standalone bootstrap skills, got: %+v", entries)
	}
	managementCatalog, err := agentskills.BuildCatalogAll("test-turn", root, "")
	if err != nil {
		t.Fatalf("BuildCatalogAll: %v", err)
	}
	seen := map[string]bool{}
	for _, entry := range managementCatalog.Entries() {
		if entry.Source != agentskills.SourceGlobal {
			t.Fatalf("bootstrap skill must be global: %+v", entry)
		}
		seen[entry.Handle] = true
	}
	if !seen["openvibely_github_autonomous_sdlc_bootstrap"] || !seen["openvibely_native_autonomous_sdlc_bootstrap"] {
		t.Fatalf("expected disabled GitHub and native bootstrap skills in management catalog, got: %+v", managementCatalog.Entries())
	}

	block := agentskills.RenderAvailableSkillsMarkdown(root, "")
	if strings.Contains(block, "openvibely_github_autonomous_sdlc_bootstrap") || strings.Contains(block, "openvibely_native_autonomous_sdlc_bootstrap") || strings.Contains(block, "skill_curator/route_task") {
		t.Fatalf("RenderAvailableSkillsMarkdown must omit disabled standalone and lifecycle hook skills:\n%s", block)
	}

	// Sanity: agent-owned SKILL.md bodies are present so lifecycle hooks can read them.
	for _, p := range []string{
		filepath.Join(root, "agents", "skill_curator", "skills", "route_task", "SKILL.md"),
		filepath.Join(root, "agents", "skill_curator", "skills", "observe_task_for_learning", "SKILL.md"),
		filepath.Join(root, "agents", "skill_curator", "skills", "maintain_skill_library", "SKILL.md"),
		filepath.Join(root, "agents", "goal", "skills", "evaluate_task_goal", "SKILL.md"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing SKILL.md %s: %v", p, err)
		}
		if info.Size() == 0 {
			t.Fatalf("SKILL.md %s is empty", p)
		}
	}
}
