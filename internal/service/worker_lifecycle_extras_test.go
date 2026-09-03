package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/lifecycle"
	"github.com/openvibely/openvibely/internal/models"
)

func afterCompleteExtrasFixture() map[string]any {
	return map[string]any{
		lifecycle.ConversationTranscriptKey: "transcript",
		lifecycle.LearningSnapshotKey:       lifecycle.LearningInputSnapshot{TaskID: "task-1", SkillWritePolicy: []string{"policy"}},
		lifecycle.AssignedAgentKey:          lifecycle.AssignedAgentIdentity{Key: "backend_reviewer"},
		lifecycle.TaskGoalKey:               &models.TaskGoal{TaskID: "task-1", Objective: "ship it"},
		lifecycle.ExecutionErrorKey:         "worktree build failed: exit status 1",
	}
}

func hookWithPayload(payloadJSON string) models.AgentLifecycleHook {
	return models.AgentLifecycleHook{
		AgentID: "any-agent", When: models.LifecycleAfterComplete, Enabled: true, PayloadJSON: payloadJSON,
	}
}

func extrasKeys(input lifecycle.HookInput) map[string]bool {
	out := map[string]bool{}
	for key := range input.Extras {
		out[key] = true
	}
	return out
}

// A hook receives exactly the context blocks its declaration asked for. The
// decision comes from the hook row, not from the identity of the owning agent.
func TestAfterCompleteExtrasFollowDeclaredPayload(t *testing.T) {
	w := &WorkerService{}
	cases := []struct {
		name    string
		payload string
		want    []string
		absent  []string
	}{
		{
			name:    "transcript and goal",
			payload: `{"blocks":["conversation_transcript","task_goal"]}`,
			want:    []string{lifecycle.ConversationTranscriptKey, lifecycle.TaskGoalKey},
			absent:  []string{lifecycle.LearningSnapshotKey, lifecycle.AssignedAgentKey},
		},
		{
			name:    "transcript and learning snapshot",
			payload: `{"blocks":["conversation_transcript","learning_snapshot"]}`,
			want:    []string{lifecycle.ConversationTranscriptKey, lifecycle.LearningSnapshotKey},
			absent:  []string{lifecycle.TaskGoalKey, lifecycle.AssignedAgentKey},
		},
		{
			name:    "transcript and assigned agent identity",
			payload: `{"blocks":["conversation_transcript","assigned_agent"]}`,
			want:    []string{lifecycle.ConversationTranscriptKey, lifecycle.AssignedAgentKey},
			absent:  []string{lifecycle.LearningSnapshotKey, lifecycle.TaskGoalKey},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := lifecycle.HookInput{TaskID: "task-1", Extras: afterCompleteExtrasFixture()}
			got := extrasKeys(w.lifecycleHookInput(context.Background(), hookWithPayload(tc.payload), input))
			for _, key := range tc.want {
				if !got[key] {
					t.Fatalf("declared block %q missing, got %v", key, got)
				}
			}
			for _, key := range tc.absent {
				if got[key] {
					t.Fatalf("undeclared block %q was sent, got %v", key, got)
				}
			}
			if !got[lifecycle.ExecutionErrorKey] {
				t.Fatalf("failed-execution details must reach every hook regardless of payload, got %v", got)
			}
		})
	}
}

// Declaring a payload must not mutate the shared slot payload other hooks in
// the same slot still read.
func TestAfterCompleteExtrasDoNotMutateSharedPayload(t *testing.T) {
	w := &WorkerService{}
	shared := afterCompleteExtrasFixture()
	before := len(shared)
	input := lifecycle.HookInput{TaskID: "task-1", Extras: shared}
	w.lifecycleHookInput(context.Background(), hookWithPayload(`{"blocks":["conversation_transcript"]}`), input)
	if len(shared) != before {
		t.Fatalf("scoping one hook mutated the shared slot payload: %v", shared)
	}
}

// Hooks that declare nothing keep every block, so existing declarations and
// user-created agents behave exactly as before.
func TestAfterCompleteExtrasDefaultToEverything(t *testing.T) {
	w := &WorkerService{}
	for _, payload := range []string{"", "{}", `{"blocks":[]}`, `{"blocks":`} {
		input := lifecycle.HookInput{TaskID: "task-1", Extras: afterCompleteExtrasFixture()}
		got := extrasKeys(w.lifecycleHookInput(context.Background(), hookWithPayload(payload), input))
		for _, key := range []string{
			lifecycle.ConversationTranscriptKey, lifecycle.LearningSnapshotKey,
			lifecycle.AssignedAgentKey, lifecycle.TaskGoalKey,
		} {
			if !got[key] {
				t.Fatalf("payload %q must fall back to every block, %q missing from %v", payload, key, got)
			}
		}
	}
}

// Route hooks keep their own index scoping and ignore payload selection.
func TestRouteHookInputStillScopesIndexes(t *testing.T) {
	w := &WorkerService{}
	input := lifecycle.HookInput{
		TaskID: "task-1",
		Extras: map[string]any{"available_skills": "index", "available_memories": "index"},
	}
	hook := models.AgentLifecycleHook{
		When: models.LifecycleRouteTask, OutputContract: models.OutputContractSelectedSkills, Enabled: true,
	}
	got := extrasKeys(w.lifecycleHookInput(context.Background(), hook, input))
	if got["available_memories"] {
		t.Fatalf("skill route hook must not receive the memory index, got %v", got)
	}
}

func TestParseHookPayload(t *testing.T) {
	if !lifecycle.ParseHookPayload(`{"blocks":["a","a","","b"]}`).Allows("b") {
		t.Fatal("expected declared block to be allowed")
	}
	if got := lifecycle.ParseHookPayload(`{"blocks":["a","a","","b"]}`).Blocks; len(got) != 2 {
		t.Fatalf("expected blank and duplicate blocks dropped, got %v", got)
	}
	if !lifecycle.ParseHookPayload("not json").SelectsAllBlocks() {
		t.Fatal("malformed payload must fall back to every block rather than starve the hook")
	}
	if lifecycle.ParseHookPayload(`{"blocks":["a"]}`).Allows("b") {
		t.Fatal("undeclared block must not be allowed")
	}
}

// The request text already reaches after-complete hooks as TaskPrompt and as
// the user half of the transcript; the snapshot must not add a third copy.
func TestLearningSnapshotOmitsDuplicateRequestText(t *testing.T) {
	w := &WorkerService{}
	task := models.Task{ID: "task-1", ProjectID: "proj-1", Prompt: "SENTINEL_REQUEST_TEXT"}
	snapshot := w.buildLearningSnapshot(context.Background(), task, "task-1:run", nil)
	if snapshot.UserRequestSummary != "" {
		t.Fatalf("learning snapshot must not repeat the request text, got %q", snapshot.UserRequestSummary)
	}
}

func TestSelectedLearningSkillEntriesPreserveOrderedAuthorizedEntries(t *testing.T) {
	catalog := agentskills.NewCatalog("turn", []agentskills.Entry{
		{Handle: "standalone", Skill: "standalone", Source: agentskills.SourceProject},
		{Handle: "assigned", Skill: "assigned", Source: agentskills.SourceAgent, AgentKey: "reviewer"},
	})

	got := selectedLearningSkillEntries(catalog, []string{" assigned ", "missing", "assigned", "", "standalone"})
	if len(got) != 2 {
		t.Fatalf("expected two selected learning entries, got %#v", got)
	}
	if got[0].Handle != "assigned" || got[0].Source != agentskills.SourceAgent || got[0].AgentKey != "reviewer" {
		t.Fatalf("assigned learning entry = %#v", got[0])
	}
	if got[1].Handle != "standalone" || got[1].Source != agentskills.SourceProject {
		t.Fatalf("standalone learning entry = %#v", got[1])
	}
}

// The assigned_agent block carries the identity fields update_memory checks.
func TestAssignedAgentIdentityCarriesAgentRecognitionFields(t *testing.T) {
	identity := assignedAgentIdentity(lifecycle.LearningInputSnapshot{
		ActiveAgentKey: "memory_curator",
		AssignedAgent: &lifecycle.LearningAgentContext{
			Key: "memory_curator", Name: "System: Memory Curator",
			SystemKind:  models.AgentSystemKindMemoryCurator,
			Description: "long description", PurposeHint: "long purpose hint",
			ToolGrants: []string{"ScopedFiles"},
		},
	})
	if identity.Key != "memory_curator" || identity.SystemKind != models.AgentSystemKindMemoryCurator {
		t.Fatalf("assigned_agent lost the fields update_memory checks: %#v", identity)
	}
}

// A failed execution must reach every after-complete hook even when its
// declared payload omits the block. Without this the Goal Agent evaluates a
// crashed run as an ordinary turn and queues continuation work off bad
// evidence, and Memory Curator records a failure as though it succeeded.
func TestFailedExecutionDetailsSurvivePayloadFiltering(t *testing.T) {
	w := &WorkerService{}
	for _, payload := range []string{
		`{"blocks":["conversation_transcript","task_goal"]}`,
		`{"blocks":["conversation_transcript","assigned_agent"]}`,
		`{"blocks":["conversation_transcript","learning_snapshot"]}`,
		`{"blocks":["conversation_transcript"]}`,
	} {
		input := lifecycle.HookInput{TaskID: "task-1", Extras: afterCompleteExtrasFixture()}
		got := w.lifecycleHookInput(context.Background(), hookWithPayload(payload), input)
		detail, ok := got.Extras[lifecycle.ExecutionErrorKey].(string)
		if !ok || detail == "" {
			t.Fatalf("payload %q dropped the execution failure: %v", payload, got.Extras)
		}
	}
}

// A successful run carries no failure block, so absence stays meaningful.
func TestSuccessfulExecutionCarriesNoFailureBlock(t *testing.T) {
	w := &WorkerService{}
	extras := afterCompleteExtrasFixture()
	delete(extras, lifecycle.ExecutionErrorKey)
	got := w.lifecycleHookInput(context.Background(),
		hookWithPayload(`{"blocks":["conversation_transcript","task_goal"]}`),
		lifecycle.HookInput{TaskID: "task-1", Extras: extras})
	if _, present := got.Extras[lifecycle.ExecutionErrorKey]; present {
		t.Fatalf("successful run must not carry a failure block: %v", got.Extras)
	}
}

func TestAlwaysDeliveredBlocks(t *testing.T) {
	if !lifecycle.AlwaysDelivered(lifecycle.ExecutionErrorKey) {
		t.Fatal("execution failure details must be always-delivered")
	}
	for _, optional := range []string{
		lifecycle.ConversationTranscriptKey, lifecycle.LearningSnapshotKey,
		lifecycle.AssignedAgentKey, lifecycle.TaskGoalKey,
	} {
		if lifecycle.AlwaysDelivered(optional) {
			t.Fatalf("%q must stay declarable, not always-delivered", optional)
		}
	}
}
