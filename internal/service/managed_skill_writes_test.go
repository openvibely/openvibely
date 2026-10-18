package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

func TestMainSkillWritesScopeAndOwnership(t *testing.T) {
	for _, mode := range []string{"chat", "task", "followup"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			stores := teststores.New(t).Stores
			project := createChatMemoryProject(t, ctx, stores.Projects)
			global := t.TempDir()
			worker := NewWorkerService(nil, 0, stores.Projects)
			worker.globalSkillRoot = global
			worker.SetLifecycleAgentRepo(stores.Agents)
			task := models.Task{ID: "scope-test", ProjectID: project.ID}
			if mode == "followup" {
				ctx = WithTaskThreadLifecycleTurn(ctx)
			}
			turn := worker.prepareMainModelKnowledge(ctx, task, mode == "chat")
			rt := llmcontracts.RuntimeToolsFromContext(turn)
			call := func(tool string, args map[string]any, denied bool) string {
				t.Helper()
				data, err := json.Marshal(args)
				require.NoError(t, err)
				out, handled, failed, err := rt.Executor(turn, tool, data)
				require.NoError(t, err)
				require.True(t, handled)
				require.Equal(t, denied, failed, out)
				return out
			}
			declaration := "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: reusable\n  name: Reusable\n---\nDurable instructions"
			args := map[string]any{"action": "create", "scope": "global", "declaration": declaration}
			call("skill_manage", args, true)
			p := project.ManagedPermissions()
			p.AllowGlobalSkillWrites = true
			project.SkillMemoryPermissions = &p
			require.NoError(t, stores.Projects.Update(ctx, project))
			for _, scope := range []string{"global", "project"} {
				args["scope"] = scope
				call("skill_manage", args, false)
				require.Contains(t, managedcontext.Text(turn), "reusable")
				owner := &models.Agent{ID: "owner-" + scope, Key: "owner_" + scope, Name: "Owner " + scope, Scope: models.AgentScope(scope), Enabled: true}
				if scope == "project" {
					owner.ProjectID = project.ID
				}
				require.NoError(t, stores.Agents.Create(ctx, owner))
				call("agent_skill_manage", map[string]any{"action": "create", "scope": scope, "agent": owner.Key, "declaration": declaration}, false)
				call("agent_skill_manage", map[string]any{"action": "archive", "scope": scope, "agent": owner.Key, "handle": "reusable"}, false)
			}
			// Global agents may also have skills overridden in the current project.
			call("agent_skill_manage", map[string]any{"action": "create", "scope": "project", "agent": "owner_global", "declaration": declaration}, false)
			call("skill_manage", map[string]any{"action": "archive", "scope": "project", "handle": "reusable"}, false)
			data, err := os.ReadFile(filepath.Join(global, "skills", "reusable", "SKILL.md"))
			require.NoError(t, err)
			require.NotContains(t, string(data), "archived: true")
			call("agent_skill_manage", map[string]any{"action": "create", "scope": "global", "agent": "owner_project", "declaration": declaration}, true)
			other := createChatMemoryProject(t, ctx, stores.Projects)
			otherTurn := worker.prepareMainModelKnowledge(ctx, models.Task{ID: "other-turn", ProjectID: other.ID}, true)
			require.Contains(t, managedcontext.Text(otherTurn), "reusable")
			foreign := &models.Agent{ID: "foreign", Key: "foreign", Name: "Foreign", Scope: models.AgentScopeProject, Enabled: true, ProjectID: other.ID}
			require.NoError(t, stores.Agents.Create(ctx, foreign))
			call("agent_skill_manage", map[string]any{"action": "create", "scope": "project", "agent": "foreign", "declaration": declaration}, true)
			// Assigned agents require an explicit grant and may target only themselves.
			owner, err := stores.Agents.GetByKey(ctx, "owner_project")
			require.NoError(t, err)
			task.AgentDefinitionID = &owner.ID
			turn = worker.prepareMainModelKnowledge(ctx, task, mode == "chat")
			rt = llmcontracts.RuntimeToolsFromContext(turn)
			ownArgs := map[string]any{"action": "create", "scope": "project", "agent": owner.Key, "declaration": declaration}
			call("agent_skill_manage", ownArgs, true)
			owner.Tools = []string{"agent_skill_manage"}
			require.NoError(t, stores.Agents.Update(ctx, owner))
			call("agent_skill_manage", ownArgs, false)
			ownArgs["agent"] = "owner_global"
			ownArgs["scope"] = "global"
			call("agent_skill_manage", ownArgs, true)
			owner.GeneratedStatus = models.AgentStatusProtected
			require.NoError(t, stores.Agents.Update(ctx, owner))
			ownArgs["agent"] = owner.Key
			ownArgs["scope"] = "project"
			call("agent_skill_manage", ownArgs, true)
			p.WriteSkills = false
			require.NoError(t, stores.Projects.Update(ctx, project))
			call("skill_manage", args, true)
		})
	}
}

func TestScheduledSkillWritersCannotBypassGlobalPermission(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	project := createChatMemoryProject(t, ctx, stores.Projects)
	worker := NewWorkerService(nil, 0, stores.Projects)
	worker.SetLifecycleAgentRepo(stores.Agents)
	actor := &models.Agent{ID: "curator", Key: "curator", Name: "Curator", Enabled: true, SystemKind: models.AgentSystemKindSkillCurator, Tools: []string{"skill_manage"}}
	require.NoError(t, stores.Agents.Create(ctx, actor))
	task := models.Task{ID: "maintenance", ProjectID: project.ID, Category: models.CategoryScheduled, AgentDefinitionID: &actor.ID}
	turn := worker.prepareMainModelKnowledge(ctx, task, false)
	// Emulate a maintenance writer prepended by a later composition stage.
	invoked := false
	rt := guardManagedTools(turn, &llmcontracts.RuntimeTools{Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
		invoked = true
		return "written", true, false, nil
	}}, stores.Projects, task, false, actor)
	_, _, failed, err := rt.Executor(turn, "skill_manage", json.RawMessage(`{"action":"archive","scope":"global","handle":"sample"}`))
	require.NoError(t, err)
	require.True(t, failed)
	require.False(t, invoked)
	p := project.ManagedPermissions()
	p.AllowGlobalSkillWrites = true
	project.SkillMemoryPermissions = &p
	require.NoError(t, stores.Projects.Update(ctx, project))
	_, _, failed, err = rt.Executor(turn, "skill_manage", json.RawMessage(`{"action":"archive","scope":"global","handle":"sample"}`))
	require.NoError(t, err)
	require.False(t, failed)
	require.True(t, invoked)
}
