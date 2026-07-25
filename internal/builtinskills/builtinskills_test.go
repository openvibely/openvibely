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
		filepath.Join(root, "skills", "openvibely_github_autonomous_sdlc_bootstrap", "templates", "github-loop-prompts.md"),
		filepath.Join(root, "skills", "openvibely_github_autonomous_sdlc_bootstrap", "references", "dev-inbox-execution-invariants.md"),
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
	promptPath := filepath.Join(root, "skills", "openvibely_github_autonomous_sdlc_bootstrap", "templates", "github-loop-prompts.md")
	if err := os.WriteFile(skillPath, []byte("stale content"), 0o644); err != nil {
		t.Fatalf("write stale SKILL.md: %v", err)
	}
	if err := os.WriteFile(promptPath, []byte("stale prompt"), 0o644); err != nil {
		t.Fatalf("write stale prompt template: %v", err)
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
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("re-read prompt template: %v", err)
	}
	if string(prompt) == "stale prompt" {
		t.Fatalf("prompt template not refreshed by SyncTo; still contains stale content")
	}
}

func TestSyncTo_EmptyRootIsNoop(t *testing.T) {
	if err := SyncTo(""); err != nil {
		t.Fatalf("SyncTo(\"\"): %v", err)
	}
}

func TestSyncTo_InstallsReusableGitHubBootstrapAsGlobalStandaloneSkill(t *testing.T) {
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()
	if err := SyncTo(globalRoot); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	cat, err := agentskills.BuildCatalog("test-turn", globalRoot, projectRoot)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	entry, ok := cat.Lookup("openvibely_github_autonomous_sdlc_bootstrap")
	if !ok {
		t.Fatalf("expected bundled GitHub autonomous SDLC bootstrap skill in global catalog")
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
	for _, want := range []string{"scope: global", "github_open_pull_request", "visible scheduled OpenVibely tasks"} {
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

// TestSyncTo_InstallsSystemHookSkillsButKeepsLifecycleHookSkillsOutOfRoutableCatalog
// verifies that built-in system hook skills are agent-owned implementation
// details, while reusable built-in standalone skills remain routable globally.
func TestSyncTo_InstallsSystemHookSkillsButKeepsLifecycleHookSkillsOutOfRoutableCatalog(t *testing.T) {
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
	entries := cat.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected two reusable global bootstrap skills, got: %+v", entries)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Source != agentskills.SourceGlobal {
			t.Fatalf("bootstrap skill must be global: %+v", entry)
		}
		seen[entry.Handle] = true
	}
	if !seen["openvibely_github_autonomous_sdlc_bootstrap"] || !seen["openvibely_native_autonomous_sdlc_bootstrap"] {
		t.Fatalf("expected GitHub and native bootstrap skills, got: %+v", entries)
	}

	block := agentskills.RenderAvailableSkillsMarkdown(root, "")
	if !strings.Contains(block, "openvibely_github_autonomous_sdlc_bootstrap") || !strings.Contains(block, "openvibely_native_autonomous_sdlc_bootstrap") || strings.Contains(block, "skill_curator/route_task") {
		t.Fatalf("RenderAvailableSkillsMarkdown should show reusable standalone skill but not lifecycle hook skills:\n%s", block)
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
