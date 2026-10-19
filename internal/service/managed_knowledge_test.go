package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

func TestMainModelKnowledgeChatAndTaskPermissions(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	require.NoError(t, os.WriteFile(filepath.Join(project.RepoPath, ".openvibely/memories/chat_memory.md"), []byte("durable fact"), 0644))
	root := filepath.Join(project.RepoPath, ".openvibely")
	writeLifecycleStandaloneSkill(t, root, "documents", "Document editing instructions")
	worker := NewWorkerService(nil, 0, stores.Projects)
	task := models.Task{ID: "test-turn", ProjectID: project.ID, Prompt: "Summarize the attached document"}
	for _, mode := range []struct {
		name           string
		chat, followup bool
	}{{"ordinary chat", true, false}, {"task execution", false, false}, {"task followup", false, true}} {
		t.Run(mode.name, func(t *testing.T) {
			chat := mode.chat
			prepare := func(ctx context.Context) LifecycleTurn {
				if chat {
					return worker.PrepareRecallOnlyLifecycleTurn(ctx, task)
				}
				if mode.followup {
					ctx = WithTaskThreadLifecycleTurnPrompt(ctx, "Edit that document")
				}
				return worker.PrepareLifecycleTurn(ctx, task)
			}
			turn := prepare(ctx)
			rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
			require.Contains(t, managedcontext.Text(turn.Ctx), "documents")
			require.Contains(t, managedcontext.Text(turn.Ctx), "chat_memory.md")
			require.NotContains(t, additionalProjectInstructionsFromContext(turn.Ctx), "available_skills")
			for _, tool := range []string{"skill_view", "memory_view"} {
				require.True(t, rt.HasDefinition(tool), tool)
			}
			call := func(name, input string, denied bool) string {
				out, handled, isErr, err := rt.Executor(ctx, name, json.RawMessage(input))
				require.NoError(t, err)
				require.True(t, handled)
				require.Equal(t, denied, isErr, out)
				return out
			}
			require.Contains(t, call("skill_view", `{"handle":"documents"}`, false), "Document editing instructions")
			require.Contains(t, call("memory_view", `{"handle":"chat_memory.md"}`, false), "durable fact")
			for _, name := range []string{"skill_import"} {
				require.False(t, rt.HasDefinition(name))
				call(name, `{}`, true)
			}
			require.Equal(t, !chat, rt.HasDefinition("skill_manage"))
			if chat {
				for _, name := range []string{"skill_manage", "agent_skill_manage", "agent_import"} {
					require.False(t, rt.HasDefinition(name))
					call(name, `{}`, true)
				}
				require.NotContains(t, managedcontext.Text(turn.Ctx), "Use skill_manage")
			} else {
				for _, body := range []string{"First instructions", "Corrected instructions"} {
					input, marshalErr := json.Marshal(map[string]any{"action": "create", "declaration": "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: reusable\n  scope: project\n---\n" + body})
					require.NoError(t, marshalErr)
					call("skill_manage", string(input), false)
					require.Contains(t, call("skill_view", `{"handle":"reusable"}`, false), body)
				}
				call("skill_manage", `{"action":"archive","handle":"reusable"}`, false)
				call("skill_view", `{"handle":"reusable"}`, true)
			}
			input, marshalErr := json.Marshal(map[string]any{"action": "create", "scope": "global", "declaration": "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: escaped\n---\nForbidden"})
			require.NoError(t, marshalErr)
			call("skill_manage", string(input), true)
			require.Equal(t, !chat, rt.HasDefinition("memory_save"))
			call("memory_save", `{"handle":"preference.md","content":"Use concise output","summary":"Output preference"}`, chat)
			if !chat {
				require.Contains(t, call("memory_view", `{"handle":"preference.md"}`, false), "concise")
				call("memory_save", `{"handle":"preference.md","content":"Use detailed output","summary":"Output preference"}`, false)
				require.Contains(t, call("memory_view", `{"handle":"preference.md"}`, false), "detailed")
				index, err := os.ReadFile(filepath.Join(project.RepoPath, ".openvibely/memories/MEMORIES.md"))
				require.NoError(t, err)
				require.Equal(t, 1, strings.Count(string(index), "preference.md"))
				call("memory_delete", `{"handle":"preference.md"}`, false)
				call("memory_view", `{"handle":"preference.md"}`, true)
			} else {
				call("memory_delete", `{"handle":"chat_memory.md"}`, true)
			}
			call("memory_view", `{"handle":"../other/MEMORIES.md"}`, true)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, _, _, err := rt.Executor(cancelled, "memory_view", json.RawMessage(`{"handle":"chat_memory.md"}`))
			require.ErrorIs(t, err, context.Canceled)
		})
	}
	// Recheck live permissions at execution time, even for a previously exposed tool.
	turn := worker.PrepareLifecycleTurn(ctx, task)
	old := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	project.SkillMemoryPermissions = &models.SkillMemoryPermissions{}
	require.NoError(t, stores.Projects.Update(ctx, project))
	for _, name := range []string{"memory_view", "memory_save", "memory_delete", "skill_view", "skill_manage"} {
		_, handled, isErr, err := old.Executor(ctx, name, json.RawMessage(`{}`))
		require.NoError(t, err)
		require.True(t, handled)
		require.True(t, isErr)
	}
	turn = worker.PrepareLifecycleTurn(ctx, task)
	require.NotContains(t, managedcontext.Text(turn.Ctx), "documents")
	require.NotContains(t, managedcontext.Text(turn.Ctx), "chat_memory.md")
	for _, name := range llmcontracts.RuntimeToolsFromContext(turn.Ctx).DefinitionNames() {
		require.NotContains(t, []string{"skill_view", "memory_view", "memory_save", "memory_delete"}, name)
	}
}

func TestManagedKnowledgeProjectAndAgentBoundary(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	one := createChatMemoryProject(t, ctx, stores.Projects)
	two := createChatMemoryProject(t, ctx, stores.Projects)
	require.NoError(t, os.WriteFile(filepath.Join(one.RepoPath, ".openvibely/memories/chat_memory.md"), []byte("one-only"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(two.RepoPath, ".openvibely/memories/chat_memory.md"), []byte("two-only"), 0644))
	worker := NewWorkerService(nil, 0, stores.Projects)
	for _, project := range []*models.Project{one, two} {
		turn := worker.PrepareRecallOnlyLifecycleTurn(ctx, models.Task{ProjectID: project.ID})
		out, _, isErr, err := llmcontracts.RuntimeToolsFromContext(turn.Ctx).Executor(ctx, "memory_view", json.RawMessage(`{"handle":"chat_memory.md"}`))
		require.NoError(t, err)
		require.False(t, isErr)
		if project == one {
			require.Contains(t, out, "one-only")
			require.NotContains(t, out, "two-only")
		} else {
			require.Contains(t, out, "two-only")
			require.NotContains(t, out, "one-only")
		}
	}
}

func TestManagedKnowledgeIndependentControlsAndMaintenance(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	root := filepath.Join(project.RepoPath, ".openvibely")
	writeLifecycleStandaloneSkill(t, root, "documents", "Document instructions")
	worker := NewWorkerService(nil, 0, stores.Projects)
	task := models.Task{ID: "controls", ProjectID: project.ID}
	priorIndex := managedcontext.Text(worker.prepareMainModelKnowledge(ctx, task, false))
	for mask := 0; mask < 16; mask++ {
		p := models.SkillMemoryPermissions{ReadSkills: mask&1 != 0, WriteSkills: mask&2 != 0, ReadMemories: mask&4 != 0, WriteMemories: mask&8 != 0}
		project.SkillMemoryPermissions = &p
		require.NoError(t, stores.Projects.Update(ctx, project))
		for _, mode := range []string{"chat", "task", "followup"} {
			chat := mode == "chat"
			base := ctx
			if mode == "followup" {
				base = WithTaskThreadLifecycleTurn(base)
			}
			turnCtx := worker.prepareMainModelKnowledge(base, task, chat)
			rt := llmcontracts.RuntimeToolsFromContext(turnCtx)
			require.Equal(t, p.ReadSkills, strings.Contains(managedcontext.Text(turnCtx), "documents"))
			require.Equal(t, p.ReadMemories, strings.Contains(managedcontext.Text(turnCtx), "chat_memory.md"))
			history := []string{"Summarize this document", priorIndex, "Summary", "Change the title"}
			updated := managedcontext.Ensure(turnCtx, history, func(s string) (string, string) { return "user", s }, func(s string) string { return s })
			text := managedcontext.Text(turnCtx)
			anyAllowed := p.ReadSkills || p.ReadMemories || !chat && (p.WriteSkills || p.WriteMemories)
			require.Equal(t, anyAllowed, text != "", "mask=%d mode=%s", mask, mode)
			require.Equal(t, history[:3], updated[:3])
			require.Equal(t, history[3], updated[len(updated)-1])
			if text != "" {
				require.Contains(t, updated, text)
			} else {
				require.Contains(t, updated[len(updated)-2], "No skills or memory index")
			}

			for _, name := range []string{"skill_view", "memory_view", "skill_manage", "agent_skill_manage", "memory_save", "memory_delete"} {
				require.Equal(t, rt.HasDefinition(name), strings.Contains(text, name), "mask=%d mode=%s tool=%s", mask, mode, name)
			}
			for name, allowed := range map[string]bool{"skill_view": p.ReadSkills, "memory_view": p.ReadMemories, "memory_save": !chat && p.WriteMemories, "memory_delete": !chat && p.WriteMemories, "skill_manage": !chat && p.WriteSkills} {
				require.Equal(t, allowed, rt.HasDefinition(name), "mask=%d chat=%t tool=%s", mask, chat, name)
				if !allowed {
					_, handled, isErr, err := rt.Executor(ctx, name, json.RawMessage(`{}`))
					require.NoError(t, err)
					require.True(t, handled)
					require.True(t, isErr)
				}
			}
			// General document tools retain their access, but cannot write managed libraries in chat.
			require.True(t, managedFileOperationAllowed(ctx, stores.Projects, task, chat, nil, "edit_file", json.RawMessage(`{"file_path":"report.docx"}`)))
			require.False(t, managedFileOperationAllowed(ctx, stores.Projects, task, chat, nil, "write_file", json.RawMessage(`{"file_path":".openvibely/memories/topic.md"}`)))
		}
		// Scheduled maintenance obeys project choices; followups require explicit tool grants.
		agent := &models.Agent{Enabled: true, SystemKind: models.AgentSystemKindSkillCurator}
		scheduled := task
		scheduled.Category = models.CategoryScheduled
		probe := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_manage"}, {Name: "skill_view"}, {Name: "agent_view"}}, Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
			return "executed", true, false, nil
		}}
		rt := guardManagedTools(ctx, probe, stores.Projects, scheduled, false, agent)
		require.Equal(t, p.WriteSkills, rt.HasDefinition("skill_manage"))
		require.Equal(t, p.ReadSkills, rt.HasDefinition("agent_view"))
		out, _, denied, err := rt.Executor(ctx, "skill_manage", json.RawMessage(`{}`))
		require.NoError(t, err)
		require.Equal(t, !p.WriteSkills, denied, out)
		followup := guardManagedTools(WithTaskThreadLifecycleTurn(ctx), probe, stores.Projects, scheduled, false, agent)
		require.False(t, followup.HasDefinition("skill_manage"))
		agent.SystemKind = models.AgentSystemKindMemoryCurator
		probe.Definitions = []llmcontracts.RuntimeToolDefinition{{Name: "read_file"}, {Name: "write_file"}, {Name: "delete_file"}}
		rt = guardManagedTools(ctx, probe, stores.Projects, scheduled, false, agent)
		require.Equal(t, p.ReadMemories, rt.HasDefinition("read_file"))
		require.Equal(t, p.WriteMemories, rt.HasDefinition("write_file"))
		require.Equal(t, p.WriteMemories, rt.HasDefinition("delete_file"))
	}
}

func TestManagedKnowledgeAssignedAgentGrantsAndIndexRefresh(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	agent := &models.Agent{ID: "knowledge-agent", Key: "knowledge_agent", Name: "Knowledge", Enabled: true, ProjectID: project.ID, Tools: []string{"memory_view", "memory_save"}}
	require.NoError(t, stores.Agents.Create(ctx, agent))
	worker := NewWorkerService(nil, 0, stores.Projects)
	worker.SetLifecycleAgentRepo(stores.Agents)
	task := models.Task{ID: "assigned", ProjectID: project.ID, AgentDefinitionID: &agent.ID}
	prepared := worker.prepareMainModelKnowledge(ctx, task, false)
	rt := llmcontracts.RuntimeToolsFromContext(prepared)
	require.True(t, rt.HasDefinition("memory_view"))
	require.True(t, rt.HasDefinition("memory_save"))
	require.False(t, rt.HasDefinition("memory_delete"))
	require.False(t, rt.HasDefinition("skill_manage"))
	agent.Tools = append(agent.Tools, "skill_manage")
	require.NoError(t, stores.Agents.Update(ctx, agent))
	require.True(t, rt.HasDefinition("skill_manage"))
	agent.Tools = []string{"memory_view", "memory_save"}
	require.NoError(t, stores.Agents.Update(ctx, agent))
	require.False(t, rt.HasDefinition("skill_manage"))
	_, skillHandled, skillDenied, skillErr := rt.Executor(ctx, "skill_manage", json.RawMessage(`{"action":"archive","handle":"documents"}`))
	require.NoError(t, skillErr)
	require.True(t, skillHandled)
	require.True(t, skillDenied)
	_, _, isErr, err := rt.Executor(ctx, "memory_save", json.RawMessage(`{"handle":"new.md","content":"durable fact","summary":"New topic"}`))
	require.NoError(t, err)
	require.False(t, isErr)
	refreshed := worker.prepareMainModelKnowledge(ctx, task, false)
	require.Contains(t, managedcontext.Text(refreshed), "new.md")
	require.Equal(t, 1, strings.Count(managedcontext.Text(refreshed), "new.md"))
	_, handled, isErr, err := rt.Executor(ctx, "memory_delete", json.RawMessage(`{"handle":"new.md"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isErr)
	// A project agent cannot be reused to obtain another project's skills or memories.
	task.ProjectID = "different-project"
	forbidden := worker.prepareMainModelKnowledge(ctx, task, false)
	require.NotContains(t, managedcontext.Text(forbidden), "new.md")
	for _, name := range []string{"skill_view", "memory_view", "memory_save", "memory_delete"} {
		require.False(t, llmcontracts.RuntimeToolsFromContext(forbidden).HasDefinition(name))
	}
}

func TestObsoletePersistedCuratorHooksNeverInvokeModels(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	worker := NewWorkerService(nil, 0, stores.Projects)
	worker.SetLifecycleAgentRepo(stores.Agents)
	hooks := &routeHookStore{}
	for _, kind := range []string{models.AgentSystemKindSkillCurator, models.AgentSystemKindMemoryCurator} {
		agent := &models.Agent{ID: kind, Key: "retired_" + kind, Name: kind, SystemKind: kind, Enabled: true}
		require.NoError(t, stores.Agents.Create(ctx, agent))
		skills := map[models.LifecycleWhen]string{models.LifecycleRouteTask: "route_task"}
		if kind == models.AgentSystemKindMemoryCurator {
			skills = map[models.LifecycleWhen]string{models.LifecycleRouteTask: "recall_memory", models.LifecycleBeforeRun: "recall_memory", models.LifecycleAfterComplete: "update_memory"}
		}
		for when, skill := range skills {
			hooks.hooks = append(hooks.hooks, models.AgentLifecycleHook{ID: kind + string(when), AgentID: agent.ID, When: when, SkillKey: skill, Enabled: true, Blocking: true})
		}
	}
	calls := 0
	worker.SetLifecycleRunner(lifecycle.NewRunner(hooks, routeHookInvokerFunc(func(context.Context, models.AgentLifecycleHook, lifecycle.HookInput) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}), nil))
	for _, when := range []models.LifecycleWhen{models.LifecycleRouteTask, models.LifecycleBeforeRun, models.LifecycleAfterComplete} {
		worker.runLifecycleSlot(ctx, when, models.Task{ProjectID: "default"}, "run", nil, llmcontracts.ChatContext{})
	}
	require.Zero(t, calls)
	require.Empty(t, hooks.created, "retired hooks must not even create lifecycle executions")
}

func TestKnowledgeChangesWithinSameTurn(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	worker := NewWorkerService(nil, 0, stores.Projects)
	task := models.Task{ID: "live-index", ProjectID: project.ID}
	turn := worker.prepareMainModelKnowledge(ctx, task, false)
	require.NotContains(t, managedcontext.Text(turn), "fresh.md")
	rt := llmcontracts.RuntimeToolsFromContext(turn)
	_, handled, failed, err := rt.Executor(turn, "memory_save", json.RawMessage(`{"handle":"fresh.md","content":"New fact","summary":"New memory"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, failed)
	require.Contains(t, managedcontext.Text(turn), "fresh.md")
	project.SkillMemoryPermissions = &models.SkillMemoryPermissions{ReadSkills: true, WriteMemories: true}
	require.NoError(t, stores.Projects.Update(ctx, project))
	require.NotContains(t, managedcontext.Text(turn), "fresh.md")
	_, _, failed, err = rt.Executor(turn, "memory_view", json.RawMessage(`{"handle":"fresh.md"}`))
	require.NoError(t, err)
	require.True(t, failed)
}

func TestManagedKnowledgePermissionsEnabledDuringTurn(t *testing.T) {
	for _, chat := range []bool{false, true} {
		ctx := context.Background()
		stores := teststores.New(t).Stores
		project := createChatMemoryProject(t, ctx, stores.Projects)
		writeLifecycleStandaloneSkill(t, filepath.Join(project.RepoPath, ".openvibely"), "documents", "Document instructions")
		project.SkillMemoryPermissions = &models.SkillMemoryPermissions{}
		require.NoError(t, stores.Projects.Update(ctx, project))
		worker := NewWorkerService(nil, 0, stores.Projects)
		task := models.Task{ID: "enable-in-turn", ProjectID: project.ID}
		turn := worker.prepareMainModelKnowledge(ctx, task, chat)
		rt := llmcontracts.RuntimeToolsFromContext(turn)
		for _, name := range []string{"skill_view", "memory_view", "memory_save", "memory_delete"} {
			require.False(t, rt.HasDefinition(name))
		}
		project.SkillMemoryPermissions = &models.SkillMemoryPermissions{ReadSkills: true, ReadMemories: true, WriteMemories: true}
		require.NoError(t, stores.Projects.Update(ctx, project))
		require.Contains(t, managedcontext.Text(turn), "documents")
		for _, name := range []string{"skill_view", "memory_view"} {
			require.True(t, rt.HasDefinition(name))
		}
		out, handled, failed, err := rt.Executor(turn, "skill_view", json.RawMessage(`{"handle":"documents"}`))
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, failed, out)
		require.Contains(t, out, "Document instructions")
		require.Equal(t, !chat, rt.HasDefinition("memory_save"))
		out, handled, failed, err = rt.Executor(turn, "memory_save", json.RawMessage(`{"handle":"new.md","content":"Durable fact","summary":"Fact"}`))
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, chat, failed, out)
		if !chat {
			out, _, failed, err = rt.Executor(turn, "memory_view", json.RawMessage(`{"handle":"new.md"}`))
			require.NoError(t, err)
			require.False(t, failed, out)
			require.Contains(t, out, "Durable fact")
			out, _, failed, err = rt.Executor(turn, "memory_delete", json.RawMessage(`{"handle":"new.md"}`))
			require.NoError(t, err)
			require.False(t, failed, out)
		}
	}
}

func TestManagedAgentPermissionsChangeWithinTurn(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	agent := &models.Agent{ID: "live-agent", Key: "live_agent", Name: "Live", Enabled: true, ProjectID: project.ID, Tools: []string{"memory_view"}}
	require.NoError(t, stores.Agents.Create(ctx, agent))
	worker := NewWorkerService(nil, 0, stores.Projects)
	worker.SetLifecycleAgentRepo(stores.Agents)
	turn := worker.prepareMainModelKnowledge(ctx, models.Task{ID: "live", ProjectID: project.ID, AgentDefinitionID: &agent.ID}, false)
	rt := llmcontracts.RuntimeToolsFromContext(turn)
	for _, enabled := range []bool{false, true, false, true} {
		agent.Tools = nil
		if enabled {
			agent.Tools = []string{"memory_view", "memory_save", "memory_delete"}
		}
		require.NoError(t, stores.Agents.Update(ctx, agent))
		for _, name := range []string{"memory_view", "memory_save", "memory_delete"} {
			require.Equal(t, enabled, rt.HasDefinition(name))
		}
		_, handled, failed, err := rt.Executor(turn, "memory_save", json.RawMessage(`{"handle":"live.md","content":"Durable fact","summary":"Fact"}`))
		require.NoError(t, err)
		require.True(t, handled)
		require.Equal(t, !enabled, failed)
	}
	agent.Enabled = false
	require.NoError(t, stores.Agents.Update(ctx, agent))
	require.False(t, rt.HasDefinition("memory_view"))
	agent.Enabled = true
	now := time.Now()
	agent.ArchivedAt = &now
	require.NoError(t, stores.Agents.Update(ctx, agent))
	require.False(t, rt.HasDefinition("memory_save"))
	_, _, failed, err := rt.Executor(turn, "memory_view", json.RawMessage(`{"handle":"live.md"}`))
	require.NoError(t, err)
	require.True(t, failed)
}

func TestManagedFileGuardResolvesSymlinkAncestors(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, ".openvibely", "memories")
	require.NoError(t, os.MkdirAll(managed, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(managed, "fact.md"), []byte("fact"), 0600))
	require.NoError(t, os.Symlink(managed, filepath.Join(root, "notes")))
	require.NoError(t, os.Symlink(filepath.Join(managed, "missing.md"), filepath.Join(root, "dangling.md")))
	ctx := context.WithValue(context.Background(), managedWorkDirKey{}, root)
	rt := guardManagedTools(ctx, &llmcontracts.RuntimeTools{Executor: func(c context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		out, err := agenttools.Execute(c, root, name, input)
		return out, true, err != nil, err
	}}, nil, models.Task{}, true, nil)
	_, handled, failed, err := rt.Executor(ctx, "write_file", json.RawMessage(`{"file_path":"notes/fact.md","content":"overwritten"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, failed)
	content, err := os.ReadFile(filepath.Join(managed, "fact.md"))
	require.NoError(t, err)
	require.Equal(t, "fact", string(content))

	for _, path := range []string{"notes/fact.md", "notes/new.md", "notes/new/nested.md", "dangling.md"} {
		input, _ := json.Marshal(map[string]string{"file_path": path})
		for _, tool := range []string{"read_file", "write_file", "edit_file"} {
			require.False(t, managedFileOperationAllowed(ctx, nil, models.Task{}, true, nil, tool, input), path)
		}
	}
	require.True(t, managedFileOperationAllowed(ctx, nil, models.Task{}, true, nil, "write_file", json.RawMessage(`{"file_path":"report.md"}`)))
}

func TestManagedSearchAuthorizesDiscoveredSymlinks(t *testing.T) {
	root := t.TempDir()
	for _, library := range []string{"memories", "skills", "agents"} {
		target := filepath.Join(root, ".openvibely", library, "private.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
		require.NoError(t, os.WriteFile(target, []byte("needle private"), 0600))
		require.NoError(t, os.Symlink(target, filepath.Join(root, library+"-alias.md")))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "public.md"), []byte("needle public"), 0600))
	require.NoError(t, os.Symlink(filepath.Join(root, "public.md"), filepath.Join(root, "public-alias.md")))
	input := json.RawMessage(`{"path":".","pattern":"needle"}`)
	for _, chat := range []bool{true, false} {
		ctx := context.WithValue(context.Background(), managedWorkDirKey{}, root)
		// Provider-local default tools execute with the provider request context.
		ctx = withManagedFileReadAuthorization(ctx, nil, models.Task{}, chat, nil)
		out, err := agenttools.Execute(ctx, root, "grep_search", input)
		require.NoError(t, err)
		require.NotContains(t, out, "private")
		require.Contains(t, out, "public.md")
		require.Contains(t, out, "public-alias.md")
		// Runtime-provided file tools receive the same policy from the executor guard.
		rt := guardManagedTools(ctx, &llmcontracts.RuntimeTools{Executor: func(c context.Context, name string, args json.RawMessage) (string, bool, bool, error) {
			out, err := agenttools.Execute(c, root, name, args)
			return out, true, err != nil, err
		}}, nil, models.Task{}, chat, nil)
		out, handled, failed, err := rt.Executor(context.Background(), "grep_search", input)
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, failed)
		require.NotContains(t, out, "private")
		require.Contains(t, out, "public-alias.md")
	}
}

func TestManagedScopedSearchChecksEachFile(t *testing.T) {
	root := t.TempDir()
	_, scoped, err := buildScopedFilesRuntimeTools(context.Background(), "p1", root, models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: ".openvibely", Permissions: []string{"read"}}}})
	require.NoError(t, err)
	for _, library := range []string{"memories", "skills", "agents"} {
		target := filepath.Join(root, ".openvibely", library, "private.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
		require.NoError(t, os.WriteFile(target, []byte("needle private"), 0600))
		require.NoError(t, os.Symlink(target, filepath.Join(root, ".openvibely", library+"-alias.md")))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, ".openvibely", "public.md"), []byte("needle public"), 0600))
	require.NoError(t, os.Symlink(filepath.Join(root, ".openvibely", "public.md"), filepath.Join(root, ".openvibely", "public-alias.md")))
	for _, chat := range []bool{true, false} {
		ctx := context.WithValue(context.Background(), managedWorkDirKey{}, root)
		rt := guardManagedTools(ctx, scoped, nil, models.Task{}, chat, nil)
		out, handled, failed, err := rt.Executor(context.Background(), "grep_search", json.RawMessage(`{"pattern":"needle"}`))
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, failed, out)
		require.NotContains(t, out, "private")
		require.Contains(t, out, "public.md")
		require.Contains(t, out, "public-alias.md")
	}
}

// Resolve the prompt on every request, including grants/revocations inside a turn.
func TestManagedKnowledgeInstructionsFollowIndividualAgentGrants(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	project.SkillMemoryPermissions = &models.SkillMemoryPermissions{WriteSkills: true, WriteMemories: true}
	require.NoError(t, stores.Projects.Update(ctx, project))
	agent := &models.Agent{ID: "prompt-grants", Key: "prompt_grants", Name: "Prompt grants", Enabled: true, ProjectID: project.ID}
	require.NoError(t, stores.Agents.Create(ctx, agent))
	worker := NewWorkerService(nil, 0, stores.Projects)
	worker.SetLifecycleAgentRepo(stores.Agents)
	task := models.Task{ID: "prompt-task", ProjectID: project.ID, AgentDefinitionID: &agent.ID}
	for _, mode := range []string{"chat", "task", "followup"} {
		t.Run(mode, func(t *testing.T) {
			base := ctx
			if mode == "followup" {
				base = WithTaskThreadLifecycleTurn(base)
			}
			turn := worker.prepareMainModelKnowledge(base, task, mode == "chat")
			for _, grant := range []string{"memory_save", "memory_delete", "skill_manage", "agent_skill_manage", ""} {
				agent.Tools = nil
				if grant != "" {
					agent.Tools = []string{grant}
				}
				require.NoError(t, stores.Agents.Update(ctx, agent))
				text := managedcontext.Text(turn)
				require.Equal(t, mode != "chat" && grant != "", text != "", "grant=%s", grant)
				for _, name := range []string{"skill_view", "memory_view", "memory_save", "memory_delete", "skill_manage", "agent_skill_manage"} {
					// Match tool words, since agent_skill_manage contains skill_manage.
					require.Equal(t, mode != "chat" && name == grant, strings.Contains(text, "Use "+name+" "), "grant=%s name=%s text=%s", grant, name, text)
				}
			}
			agent.Tools = []string{"memory_save"}
			agent.Enabled = false
			require.NoError(t, stores.Agents.Update(ctx, agent))
			require.Empty(t, managedcontext.Text(turn))
			agent.Enabled = true
			require.NoError(t, stores.Agents.Update(ctx, agent))
		})
	}
}

func TestManagedKnowledgeRevocationAndRestorationPreservesResults(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	worker := NewWorkerService(nil, 0, stores.Projects)
	turn := worker.prepareMainModelKnowledge(ctx, models.Task{ID: "refresh", ProjectID: project.ID}, false)
	type message struct{ role, text string }
	read := func(m message) (string, string) { return m.role, m.text }
	makeMessage := func(s string) message { return message{"user", s} }
	result := message{"tool", "Loaded skill instructions and memory result"}
	history := []message{makeMessage(managedcontext.Text(turn)), result, makeMessage(managedcontext.Text(turn)), {"user", "Follow up"}}
	project.SkillMemoryPermissions = &models.SkillMemoryPermissions{}
	require.NoError(t, stores.Projects.Update(ctx, project))
	require.Empty(t, managedcontext.Text(turn))
	history = managedcontext.Ensure(turn, history, read, makeMessage)
	require.Len(t, history, 5)
	require.Equal(t, result, history[1])
	require.Contains(t, history[3].text, "No skills or memory index")
	project.SkillMemoryPermissions = &models.SkillMemoryPermissions{ReadMemories: true}
	require.NoError(t, stores.Projects.Update(ctx, project))
	history = managedcontext.Ensure(turn, history, read, makeMessage)
	require.Len(t, history, 6)
	require.Equal(t, result, history[1])
	require.Equal(t, managedcontext.Text(turn), history[4].text)
	require.Equal(t, history, managedcontext.Ensure(turn, history, read, makeMessage))
	// Compaction dropped the index: restore it once on the next request.
	compacted := []message{{"user", "Conversation summary"}}
	restored := managedcontext.Ensure(turn, compacted, read, makeMessage)
	require.Len(t, restored, 2)
	require.Equal(t, restored, managedcontext.Ensure(turn, restored, read, makeMessage))
	// Later runtime composition can narrow access further than the project grants.
	rt := llmcontracts.RuntimeToolsFromContext(turn)
	filtered := *rt
	filtered.Filter = func(string) (bool, bool) { return false, true }
	require.Empty(t, managedcontext.Text(llmcontracts.WithRuntimeTools(turn, &filtered)))
}
