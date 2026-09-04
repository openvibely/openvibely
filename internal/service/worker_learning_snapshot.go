package service

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/lifecycle"
	"github.com/openvibely/openvibely/internal/models"
)

func (w *WorkerService) buildLearningSnapshot(ctx context.Context, task models.Task, taskRunID string, runErr error) lifecycle.LearningInputSnapshot {
	turn := lifecycleTurnFromContext(ctx)
	// UserRequestSummary is intentionally left empty: the hook prompt already
	// carries the request as HookInput.TaskPrompt and again as the user half of
	// extras.conversation_transcript. A third copy in the same JSON block is
	// paid for on every after-complete hook and read by none of them.
	snapshot := lifecycle.LearningInputSnapshot{
		TaskID:                 task.ID,
		TaskRunID:              taskRunID,
		ProjectID:              task.ProjectID,
		LoadedSkillHandles:     append([]string(nil), turn.SelectedSkillHandles...),
		SkillWritePolicy:       learningSkillWritePolicy(turn.AssignedAgent != nil),
		ExecutionStatus:        "completed",
		ExistingSkillIndex:     []string{},
		ExistingAgentIndex:     []string{},
		RecentRoutingDecisions: []string{},
	}
	if runErr != nil {
		snapshot.ExecutionStatus = "failed"
		snapshot.FailureAndRecoverySummary = runErr.Error()
	}
	if turn.AssignedAgent != nil {
		agent := turn.AssignedAgent
		snapshot.ActiveAgentID = agent.ID
		snapshot.ActiveAgentKey = agent.Key
		snapshot.AssignedAgent = &lifecycle.LearningAgentContext{
			ID:          agent.ID,
			Key:         agent.Key,
			Name:        agent.Name,
			Description: strings.TrimSpace(agent.Description),
			SystemKind:  agent.SystemKind,
			Scope:       string(agent.Scope),
			ProjectID:   agent.ProjectID,
			ToolGrants:  append([]string(nil), agent.Tools...),
			PurposeHint: firstNonEmptyLearningString(agent.Description, agent.SystemPrompt),
		}
	}
	for _, entry := range selectedLearningSkillEntries(turn.Catalog, turn.SelectedSkillHandles) {
		ctx := lifecycle.LearningSkillContext{Handle: entry.Handle, Name: entry.Skill, Owner: string(entry.Source)}
		if entry.Source == agentskills.SourceAgent || entry.AgentKey != "" {
			ctx.Owner = "assigned_agent"
			snapshot.SelectedAgentSkills = append(snapshot.SelectedAgentSkills, ctx)
			continue
		}
		snapshot.SelectedStandaloneSkills = append(snapshot.SelectedStandaloneSkills, ctx)
	}
	return snapshot
}

// assignedAgentIdentity derives the compact assigned-agent block from the
// learning snapshot, so a hook that only needs to recognize which agent ran the
// task can declare `assigned_agent` instead of the whole snapshot.
func assignedAgentIdentity(snapshot lifecycle.LearningInputSnapshot) lifecycle.AssignedAgentIdentity {
	identity := lifecycle.AssignedAgentIdentity{Key: snapshot.ActiveAgentKey}
	if snapshot.AssignedAgent != nil {
		if identity.Key == "" {
			identity.Key = snapshot.AssignedAgent.Key
		}
		identity.Name = snapshot.AssignedAgent.Name
		identity.SystemKind = snapshot.AssignedAgent.SystemKind
	}
	return identity
}

func selectedLearningSkillEntries(catalog *agentskills.Catalog, handles []string) []agentskills.Entry {
	if catalog == nil {
		return nil
	}
	out := make([]agentskills.Entry, 0, len(handles))
	seen := map[string]struct{}{}
	for _, handle := range handles {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		entry, ok := catalog.Lookup(handle)
		if !ok {
			continue
		}
		seen[handle] = struct{}{}
		out = append(out, entry)
	}
	return out
}

func learningSkillWritePolicy(hasAssignedAgent bool) []string {
	policy := []string{
		"Use skill_manage for reusable standalone/project/global skill learning that would help many agents or future no-agent tasks.",
		"If unsure whether learning is agent-specific, prefer skill_manage for standalone skills or no change.",
		"Do not copy standalone skills into an agent automatically.",
	}
	if hasAssignedAgent {
		policy = append([]string{
			"Use agent_skill_manage only for changes specific to the assigned agent's role, workflow, or selected agent-owned skills.",
			"agent_skill_manage is server-scoped to the assigned agent; pass only skill keys, never agent/skill paths.",
		}, policy...)
	}
	return policy
}

func firstNonEmptyLearningString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
