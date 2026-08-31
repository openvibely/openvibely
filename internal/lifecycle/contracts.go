// Package lifecycle implements the task lifecycle runner described in the
// agent lifecycle runbook.
//
// The runner owns timing for `route_task`, `before_run`, `task_mode`, and
// `after_complete` slots. Each slot invokes configured lifecycle hooks. Hook
// invocations always produce a recorded lifecycle execution; hook executions
// never start new `when` values themselves, which prevents recursion.
//
// Memory rewiring is intentionally out of scope; this package exposes the
// generic runner, contract validators, and hook plumbing so the existing
// MemoryService can be migrated behind it in a follow-up.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SelectedMode is the legacy validated payload for the `selected_mode` contract.
// Skill Curator no longer uses this contract for route_task; routes now select
// relevant skills while the task's assigned/default agent remains unchanged.
type SelectedMode struct {
	Mode               string  `json:"mode"`
	Action             string  `json:"action"`
	Confidence         float64 `json:"confidence"`
	Reason             string  `json:"reason"`
	NeedsClarification bool    `json:"needs_clarification"`
	ClarifyingQuestion string  `json:"clarifying_question,omitempty"`
}

// SelectedSkills is the validated payload for the `selected_skills` route_task
// contract. It selects skill handles from the current available_skills index for
// the next task turn; it never changes the task's assigned agent.
type SelectedSkills struct {
	Skills             []string `json:"skills"`
	Confidence         float64  `json:"confidence"`
	Reason             string   `json:"reason"`
	NeedsClarification bool     `json:"needs_clarification"`
	ClarifyingQuestion string   `json:"clarifying_question,omitempty"`
}

// SelectedMemories is the validated payload for the `selected_memories` route_task
// contract. It selects memory handles from the current available_memories index
// for the next task turn; full bodies are load-on-demand through memory_view.
type SelectedMemories struct {
	Memories           []SelectedMemory `json:"memories"`
	Content            string           `json:"content,omitempty"` // optional route/debug note; task prompt uses selected handles plus memory_view
	Confidence         float64          `json:"confidence"`
	Reason             string           `json:"reason"`
	NeedsClarification bool             `json:"needs_clarification"`
	ClarifyingQuestion string           `json:"clarifying_question,omitempty"`
}

// ContextBlock is the validated payload for the `context_block` contract used
// by before_run hooks.
type ContextBlock struct {
	Content          string           `json:"content"`
	Sources          []string         `json:"sources,omitempty"`
	SelectedMemories []SelectedMemory `json:"selected_memories,omitempty"`
	Confidence       float64          `json:"confidence,omitempty"`
}

// SelectedMemory is optional debug metadata for context_block outputs from
// recall_memory. It identifies the memory topic/file used without requiring the
// UI to expose the full memory file contents.
type SelectedMemory struct {
	File    string `json:"file,omitempty"`
	Topic   string `json:"topic,omitempty"`
	Summary string `json:"summary,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// ActivitySummary is the validated payload for the `activity_summary` contract
// used by hooks that touch durable storage (memory create, consolidation).
type ActivitySummary struct {
	Summary      string   `json:"summary"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	Created      []string `json:"created,omitempty"`
	Updated      []string `json:"updated,omitempty"`
	Skipped      bool     `json:"skipped,omitempty"`
	SkipReason   string   `json:"skip_reason,omitempty"`
}

// LearningSummary is the validated payload for `observe_task_for_learning`.
type LearningSummary struct {
	Summary             string        `json:"summary"`
	NothingToSave       bool          `json:"nothing_to_save,omitempty"`
	CreatedAgents       []string      `json:"created_agents,omitempty"`
	UpdatedAgents       []string      `json:"updated_agents,omitempty"`
	ArchivedAgents      []string      `json:"archived_agents,omitempty"`
	CreatedSkills       []string      `json:"created_skills,omitempty"`
	UpdatedSkills       []string      `json:"updated_skills,omitempty"`
	ArchivedSkills      []string      `json:"archived_skills,omitempty"`
	SupportFilesWritten []string      `json:"support_files_written,omitempty"`
	HooksUpdated        []string      `json:"hooks_updated,omitempty"`
	BlockedChanges      []string      `json:"blocked_changes,omitempty"`
	EvidenceRefs        []EvidenceRef `json:"evidence_refs,omitempty"`
}

// EvidenceRef points at the source task/run a learning artifact came from.
type EvidenceRef struct {
	TaskID    string `json:"task_id,omitempty"`
	TaskRunID string `json:"task_run_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// LearningInputSnapshot is the structured payload the runner hands to
// observe_task_for_learning and maintain_skill_library hooks.
//
// The snapshot is stored with the learning execution so future debugging can
// answer why a skill changed. It also tells observe_task_for_learning whether
// learning should update reusable standalone skills or skills owned by the
// assigned task agent. Callers attach this to HookInput.Extras under the
// "learning_snapshot" key.
type LearningInputSnapshot struct {
	TaskID                      string                 `json:"task_id,omitempty"`
	TaskRunID                   string                 `json:"task_run_id,omitempty"`
	ProjectID                   string                 `json:"project_id,omitempty"`
	ActiveAgentID               string                 `json:"active_agent_id,omitempty"`
	ActiveAgentKey              string                 `json:"active_agent_key,omitempty"`
	SelectedAgentReason         string                 `json:"selected_agent_reason,omitempty"`
	RoutingCandidatesConsidered []string               `json:"routing_candidates_considered,omitempty"`
	UserRequestSummary          string                 `json:"user_request_summary,omitempty"`
	FinalResponseSummary        string                 `json:"final_response_summary,omitempty"`
	ExecutionStatus             string                 `json:"execution_status,omitempty"`
	LoadedSkillHandles          []string               `json:"loaded_skill_handles,omitempty"`
	ViewedSkillHandles          []string               `json:"viewed_skill_handles,omitempty"`
	ToolCallSummary             []string               `json:"tool_call_summary,omitempty"`
	FailureAndRecoverySummary   string                 `json:"failure_and_recovery_summary,omitempty"`
	ChangedFileSummary          []string               `json:"changed_file_summary,omitempty"`
	UserCorrections             []string               `json:"user_corrections,omitempty"`
	ExistingAgentIndex          []string               `json:"existing_agent_index,omitempty"`
	ExistingSkillIndex          []string               `json:"existing_skill_index,omitempty"`
	RecentRoutingDecisions      []string               `json:"recent_routing_decisions,omitempty"`
	RelevantMemorySummary       string                 `json:"relevant_memory_summary,omitempty"`
	ProtectedAgentAndSkillFlags map[string]any         `json:"protected_agent_and_skill_flags,omitempty"`
	AssignedAgent               *LearningAgentContext  `json:"assigned_agent,omitempty"`
	SelectedAgentSkills         []LearningSkillContext `json:"selected_agent_skills,omitempty"`
	SelectedStandaloneSkills    []LearningSkillContext `json:"selected_standalone_skills,omitempty"`
	SkillWritePolicy            []string               `json:"skill_write_policy,omitempty"`
}

// LearningAgentContext describes the task's assigned agent so learning hooks can
// decide whether a change is agent-specific or reusable standalone guidance.
type LearningAgentContext struct {
	ID          string   `json:"id,omitempty"`
	Key         string   `json:"key,omitempty"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	SystemKind  string   `json:"system_kind,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	ToolGrants  []string `json:"tool_grants,omitempty"`
	PurposeHint string   `json:"purpose_hint,omitempty"`
}

// LearningSkillContext labels a selected skill by ownership scope. Agent-owned
// handles are skill keys scoped to AssignedAgent, not agent/skill paths.
type LearningSkillContext struct {
	Handle      string `json:"handle,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner,omitempty"`
}

// LearningSnapshotKey is the conventional HookInput.Extras key for embedding
// a LearningInputSnapshot in a hook invocation.
const LearningSnapshotKey = "learning_snapshot"

// ConversationTranscriptKey is the conventional HookInput.Extras key for the
// current completed user/assistant turn handed to after_complete hooks. It does
// not include prior chat history, task metadata, execution artifacts, diffs,
// statuses, or lifecycle internals.
const ConversationTranscriptKey = "conversation_transcript"

// AssignedAgentKey is the HookInput.Extras key for the compact identity of the
// task's assigned agent. Hooks that only need to know which agent ran the task
// can declare this block instead of the full LearningSnapshotKey.
const AssignedAgentKey = "assigned_agent"

// TaskGoalKey is the HookInput.Extras key for the task's active goal.
const TaskGoalKey = "task_goal"

// ExecutionErrorKey is the HookInput.Extras key carrying the error from a task
// execution that failed. It is absent when the execution succeeded.
const ExecutionErrorKey = "execution_error"

// alwaysDeliveredBlocks are context blocks every hook receives regardless of
// the payload it declared. They describe the outcome of the turn itself rather
// than optional context, so filtering them out would let a hook evaluate a
// failed run as though it had succeeded.
var alwaysDeliveredBlocks = map[string]struct{}{
	ExecutionErrorKey: {},
}

// AlwaysDelivered reports whether a context block is delivered to every hook
// regardless of its declared payload.
func AlwaysDelivered(block string) bool {
	_, ok := alwaysDeliveredBlocks[strings.TrimSpace(block)]
	return ok
}

// AssignedAgentIdentity is the compact identity of the agent that ran the task,
// for hooks that only need to recognize which agent produced the turn.
type AssignedAgentIdentity struct {
	Key        string `json:"key,omitempty"`
	Name       string `json:"name,omitempty"`
	SystemKind string `json:"system_kind,omitempty"`
}

// HookPayload is the parsed form of AgentLifecycleHook.PayloadJSON. It names
// the lifecycle context blocks an agent's hook skill reads. An empty Blocks
// list means "every block the slot produced", which is the default for hooks
// that declare no payload.
type HookPayload struct {
	Blocks []string `json:"blocks,omitempty"`
}

// ParseHookPayload reads a hook's declared payload. Unset, empty, or malformed
// JSON yields a zero HookPayload, which callers treat as "send everything" so a
// bad declaration never silently starves a hook of context.
func ParseHookPayload(raw string) HookPayload {
	var payload HookPayload
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return payload
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return HookPayload{}
	}
	cleaned := make([]string, 0, len(payload.Blocks))
	seen := map[string]struct{}{}
	for _, block := range payload.Blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if _, ok := seen[block]; ok {
			continue
		}
		seen[block] = struct{}{}
		cleaned = append(cleaned, block)
	}
	payload.Blocks = cleaned
	return payload
}

// SelectsAllBlocks reports whether the hook declared no payload and should
// therefore receive every context block the slot produced.
func (p HookPayload) SelectsAllBlocks() bool { return len(p.Blocks) == 0 }

// Allows reports whether the hook receives the supplied context block: either
// it declared the block, it declared no payload at all, or the block is
// delivered to every hook regardless of declaration.
func (p HookPayload) Allows(block string) bool {
	if p.SelectsAllBlocks() || AlwaysDelivered(block) {
		return true
	}
	for _, declared := range p.Blocks {
		if declared == block {
			return true
		}
	}
	return false
}

// LibraryUpdateSummary is the validated payload for scheduled/explicit
// library maintenance runs.
type LibraryUpdateSummary struct {
	Summary             string          `json:"summary"`
	CreatedAgents       []string        `json:"created_agents,omitempty"`
	UpdatedAgents       []string        `json:"updated_agents,omitempty"`
	ArchivedAgents      []string        `json:"archived_agents,omitempty"`
	CreatedSkills       []string        `json:"created_skills,omitempty"`
	UpdatedSkills       []string        `json:"updated_skills,omitempty"`
	ArchivedSkills      []string        `json:"archived_skills,omitempty"`
	AgentConsolidations []Consolidation `json:"agent_consolidations,omitempty"`
	SkillConsolidations []Consolidation `json:"skill_consolidations,omitempty"`
	AgentPrunings       []Pruning       `json:"agent_prunings,omitempty"`
	SkillPrunings       []Pruning       `json:"skill_prunings,omitempty"`
	BlockedChanges      []string        `json:"blocked_changes,omitempty"`
}

// Consolidation describes an "absorbed into" archive action.
type Consolidation struct {
	From   string `json:"from"`
	Into   string `json:"into"`
	Reason string `json:"reason,omitempty"`
}

// Pruning describes an archive action with no replacement target.
type Pruning struct {
	Key    string `json:"key,omitempty"`
	Handle string `json:"handle,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ValidateSelectedMode parses and validates `selected_mode` output. It does
// not check that the mode is enabled or allowed for the project; the runner
// performs those checks against persisted config.
func ValidateSelectedMode(raw []byte) (SelectedMode, error) {
	var out SelectedMode
	if len(raw) == 0 {
		return out, errors.New("selected_mode: empty payload")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("selected_mode: invalid JSON: %w", err)
	}
	out.Mode = strings.TrimSpace(out.Mode)
	out.Action = strings.TrimSpace(strings.ToLower(out.Action))
	if out.NeedsClarification {
		if strings.TrimSpace(out.ClarifyingQuestion) == "" {
			return out, errors.New("selected_mode: clarifying_question required when needs_clarification=true")
		}
		return out, nil
	}
	if out.Mode == "" {
		return out, errors.New("selected_mode: mode is required")
	}
	switch out.Action {
	case "continue", "switch":
	case "":
		return out, errors.New("selected_mode: action must be continue or switch")
	default:
		return out, fmt.Errorf("selected_mode: action must be continue or switch, got %q", out.Action)
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		return out, fmt.Errorf("selected_mode: confidence %v out of range [0,1]", out.Confidence)
	}
	return out, nil
}

// ValidateSelectedSkills parses and validates a `selected_skills` payload.
func ValidateSelectedSkills(raw []byte) (SelectedSkills, error) {
	var out SelectedSkills
	if len(raw) == 0 {
		return out, errors.New("selected_skills: empty payload")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("selected_skills: invalid JSON: %w", err)
	}
	if out.NeedsClarification {
		if strings.TrimSpace(out.ClarifyingQuestion) == "" {
			return out, errors.New("selected_skills: clarifying_question required when needs_clarification=true")
		}
		return out, nil
	}
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(out.Skills))
	for _, handle := range out.Skills {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		if strings.Contains(handle, "/") {
			return out, fmt.Errorf("selected_skills: skill handle %q must be a skill key from the current available_skills index, not agent/skill", handle)
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
		cleaned = append(cleaned, handle)
	}
	out.Skills = cleaned
	if out.Confidence < 0 || out.Confidence > 1 {
		return out, fmt.Errorf("selected_skills: confidence %v out of range [0,1]", out.Confidence)
	}
	return out, nil
}

// ValidateSelectedMemories parses and validates a `selected_memories` payload.
func ValidateSelectedMemories(raw []byte) (SelectedMemories, error) {
	var out SelectedMemories
	if len(raw) == 0 {
		return out, errors.New("selected_memories: empty payload")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("selected_memories: invalid JSON: %w", err)
	}
	if out.NeedsClarification {
		if strings.TrimSpace(out.ClarifyingQuestion) == "" {
			return out, errors.New("selected_memories: clarifying_question required when needs_clarification=true")
		}
		return out, nil
	}
	seen := map[string]struct{}{}
	cleaned := make([]SelectedMemory, 0, len(out.Memories))
	for _, memory := range out.Memories {
		memory.File = strings.TrimSpace(strings.ReplaceAll(memory.File, "\\", "/"))
		memory.Topic = strings.TrimSpace(memory.Topic)
		memory.Summary = strings.TrimSpace(memory.Summary)
		memory.Snippet = strings.TrimSpace(memory.Snippet)
		if memory.File == "" {
			continue
		}
		if !isSafeRelativeMemoryIdentifier(memory.File) {
			return out, fmt.Errorf("selected_memories: unsafe memory file %q", memory.File)
		}
		key := memory.File
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, memory)
	}
	out.Memories = cleaned
	out.Content = strings.TrimSpace(out.Content)
	out.Reason = strings.TrimSpace(out.Reason)
	if out.Confidence < 0 || out.Confidence > 1 {
		return out, fmt.Errorf("selected_memories: confidence %v out of range [0,1]", out.Confidence)
	}
	return out, nil
}

func isSafeRelativeMemoryIdentifier(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "./") || strings.Contains(value, ":") {
		return false
	}
	cleaned := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	cleaned = strings.TrimPrefix(cleaned, "./")
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") || strings.HasSuffix(cleaned, "/..") {
		return false
	}
	return true
}

// ValidateContextBlock parses and validates a `context_block` payload.
func ValidateContextBlock(raw []byte) (ContextBlock, error) {
	var out ContextBlock
	if len(raw) == 0 {
		return out, errors.New("context_block: empty payload")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("context_block: invalid JSON: %w", err)
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		return out, fmt.Errorf("context_block: confidence %v out of range [0,1]", out.Confidence)
	}
	return out, nil
}

// ValidateActivitySummary parses and validates an `activity_summary` payload.
func ValidateActivitySummary(raw []byte) (ActivitySummary, error) {
	var out ActivitySummary
	if len(raw) == 0 {
		return out, errors.New("activity_summary: empty payload")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("activity_summary: invalid JSON: %w", err)
	}
	if out.Skipped && strings.TrimSpace(out.SkipReason) == "" {
		return out, errors.New("activity_summary: skip_reason required when skipped=true")
	}
	if !out.Skipped && strings.TrimSpace(out.Summary) == "" {
		return out, errors.New("activity_summary: summary required unless skipped=true")
	}
	return out, nil
}

// ValidateLearningSummary parses and validates a `learning_summary` payload.
// Per the runbook, a real run must return a concise summary; a no-op run must
// set nothing_to_save=true (the model's `Nothing to save.` response).
func ValidateLearningSummary(raw []byte) (LearningSummary, error) {
	var out LearningSummary
	if len(raw) == 0 {
		return out, errors.New("learning_summary: empty payload")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("learning_summary: invalid JSON: %w", err)
	}
	if !out.NothingToSave && strings.TrimSpace(out.Summary) == "" {
		return out, errors.New("learning_summary: summary required unless nothing_to_save=true")
	}
	if len(out.CreatedAgents) > 0 || len(out.UpdatedAgents) > 0 || len(out.ArchivedAgents) > 0 {
		return out, errors.New("learning_summary: agent changes are not allowed; agents are user-managed")
	}
	return out, nil
}

// ValidateLibraryUpdateSummary parses and validates a `library_update_summary`.
// Archived artifacts must appear in exactly one of the consolidation or
// pruning lists, matching the runbook's structured-summary requirement.
func ValidateLibraryUpdateSummary(raw []byte) (LibraryUpdateSummary, error) {
	var out LibraryUpdateSummary
	if len(raw) == 0 {
		return out, errors.New("library_update_summary: empty payload")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("library_update_summary: invalid JSON: %w", err)
	}
	if strings.TrimSpace(out.Summary) == "" {
		return out, errors.New("library_update_summary: summary required")
	}
	if len(out.CreatedAgents) > 0 || len(out.UpdatedAgents) > 0 || len(out.ArchivedAgents) > 0 || len(out.AgentConsolidations) > 0 || len(out.AgentPrunings) > 0 {
		return out, errors.New("library_update_summary: agent changes are not allowed; agents are user-managed")
	}
	if err := validateArchivedAttribution("skill", out.ArchivedSkills, skillArchiveTargets(out)); err != nil {
		return out, err
	}
	return out, nil
}

func skillArchiveTargets(s LibraryUpdateSummary) map[string]int {
	out := make(map[string]int, len(s.ArchivedSkills))
	for _, c := range s.SkillConsolidations {
		out[c.From]++
	}
	for _, p := range s.SkillPrunings {
		out[p.Handle]++
	}
	return out
}

func validateArchivedAttribution(kind string, archived []string, attributions map[string]int) error {
	for _, a := range archived {
		count := attributions[a]
		if count == 0 {
			return fmt.Errorf("library_update_summary: archived %s %q missing from consolidations and prunings", kind, a)
		}
		if count > 1 {
			return fmt.Errorf("library_update_summary: archived %s %q appears in multiple lists", kind, a)
		}
	}
	return nil
}
