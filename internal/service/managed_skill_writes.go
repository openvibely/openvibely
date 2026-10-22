package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

// Resolve authorization and the exact root on every operation, including revocation
// during a tool loop. A single-root importer prevents archive from touching overrides.
func (w *WorkerService) mainSkillWriters(task models.Task) *llmcontracts.RuntimeTools {
	prototype := agentlibrary.NewImporter(agentlibrary.SkillRoots{}, nil)
	standalone := agentlibrary.SkillMutationTools(prototype, nil)
	owned := agentlibrary.LibraryAgentSkillMutationTools(prototype, nil)
	return &llmcontracts.RuntimeTools{
		Definitions: append(standalone.Definitions[:1:1], owned.Definitions...),
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name != "skill_manage" && name != "agent_skill_manage" {
				return "", false, false, nil
			}
			deny := func(err error) (string, bool, bool, error) { return err.Error(), true, true, nil }
			scope, err := w.authorizeSkillWrite(ctx, task, name, input)
			if err != nil {
				return deny(err)
			}
			roots := agentlibrary.SkillRoots{}
			if scope == "global" {
				roots.Global = w.globalSkillRoot
			} else {
				roots.Project = projectSkillRoot(ctx, w.projectRepo, task.ProjectID)
			}
			var recorder agentlibrary.MutationRecorder
			if w.mutationRecorder != nil {
				recorder = w.mutationRecorder(task)
			}
			importer := agentlibrary.NewImporter(roots, nil)
			// Normalize the selected scope for all actions, including support files/archive.
			var args map[string]json.RawMessage
			if err := json.Unmarshal(input, &args); err != nil {
				return deny(err)
			}
			if args == nil {
				return deny(fmt.Errorf("skill arguments must be an object"))
			}
			args["scope"], _ = json.Marshal(scope)
			input, _ = json.Marshal(args)
			if name == "agent_skill_manage" {
				return agentlibrary.LibraryAgentSkillMutationTools(importer, recorder).Executor(ctx, name, input)
			}
			return agentlibrary.SkillMutationTools(importer, recorder).Executor(ctx, name, input)
		},
	}
}

func (w *WorkerService) authorizeSkillWrite(ctx context.Context, task models.Task, name string, input json.RawMessage) (string, error) {
	var p struct {
		Scope       string `json:"scope"`
		Agent       string `json:"agent"`
		Declaration string `json:"declaration"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return "", err
	}
	if p.Scope == "" && p.Declaration != "" {
		decl, _, err := agentlibrary.ParseDeclaration(p.Declaration)
		if err != nil {
			return "", err
		}
		p.Scope = decl.Skill.Scope
	}
	if p.Scope == "" {
		p.Scope = "project"
	}
	if p.Scope != "project" && p.Scope != "global" {
		return "", fmt.Errorf("scope must be project or global")
	}
	permissions := projectManagedPermissions(ctx, w.projectRepo, task.ProjectID)
	actor := w.taskAgentDefinition(ctx, task)
	if !permissions.WriteSkills || task.AgentDefinitionID != nil && (actor == nil || !actor.Enabled || actor.ArchivedAt != nil || actor.ProjectID != "" && actor.ProjectID != task.ProjectID || !agentExplicitlyAllowsTool(actor, name)) {
		return "", fmt.Errorf("skill writing is not permitted")
	}
	if p.Scope == "global" && !permissions.AllowGlobalSkillWrites {
		return "", fmt.Errorf("global skill writes require Allow global skill writes")
	}
	if name == "agent_skill_manage" {
		if w.agentRepo == nil {
			return "", fmt.Errorf("agent library is unavailable")
		}
		target, err := w.agentRepo.GetByKey(ctx, p.Agent)
		if err != nil || target == nil || !target.Enabled || target.ArchivedAt != nil || target.SystemKind != "" || target.GeneratedStatus == models.AgentStatusProtected || target.GeneratedStatus == models.AgentStatusArchived || target.ProjectID != "" && target.ProjectID != task.ProjectID || p.Scope == "global" && target.Scope != models.AgentScopeGlobal {
			return "", fmt.Errorf("target agent is unavailable or outside the requested scope")
		}
		maintenance := actor != nil && actor.SystemKind == models.AgentSystemKindSkillCurator && task.Category == models.CategoryScheduled && !lifecycleTurnFromContext(ctx).TaskThreadTurn
		if actor != nil && actor.ID != target.ID && !maintenance {
			return "", fmt.Errorf("assigned agents may maintain only their own skills")
		}
	}

	return p.Scope, nil
}
