package stream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func structuredTestWriter(t *testing.T) (*Writer, *teststores.Env, *models.Execution) {
	t.Helper()
	env := teststores.New(t, teststores.WithProductionTopology())
	task := &models.Task{ProjectID: "default", Title: "structured stream", Prompt: "test", Category: models.CategoryActive, Status: models.StatusPending}
	if err := env.Tasks.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	config, err := env.LLMConfigs.GetDefault(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, PromptSent: "test", Status: models.ExecRunning}
	if err := env.Executions.Create(t.Context(), exec); err != nil {
		t.Fatal(err)
	}
	w := NewStructuredWriterWithPublisher(exec.ID, task.ID, env.Executions, t.Context(), time.Hour, nil)
	t.Cleanup(w.Stop)
	if w.structured == nil {
		t.Fatal("structured storage not enabled")
	}
	return w, env, exec
}

func TestStructuredWriterDurablePartialAndCompletion(t *testing.T) {
	w, env, exec := structuredTestWriter(t)
	old := &Writer{}
	events := []Event{{Type: EventThinkingOpen}, {Type: EventThinkingText, Text: "part one café"}, {Type: EventThinkingText, Text: " part two"}, {Type: EventThinkingEnd}, {Type: EventTextDelta, Text: "<proposed_plan>keep this plan</proposed_plan>"}, {Type: EventToolUse, ToolName: "shell", Secondary: "a]b"}, {Type: EventToolResult, ToolName: "shell", Output: "nested [/Tool]\nresult"}, {Type: EventTextDelta, Text: "done"}}
	for i, e := range events {
		WriteEvent(w, e, false)
		WriteEvent(old, e, false)
		w.flushPeriodicOnce()
		saved, err := env.Executions.GetByID(t.Context(), exec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Output != old.String() {
			t.Fatalf("event %d got %q want %q", i, saved.Output, old.String())
		}
	}
	got, err := w.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got != old.String() {
		t.Fatal("final output differs")
	}
	before, err := env.Executions.ReadConversationChanges(t.Context(), models.ConversationOwner{ExecutionID: exec.ID}, models.ConversationDisplay, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Executions.Complete(t.Context(), exec.ID, models.ExecCompleted, got, "", 0, 0); err != nil {
		t.Fatal(err)
	}
	after, err := env.Executions.ReadConversationChanges(t.Context(), models.ConversationOwner{ExecutionID: exec.ID}, models.ConversationDisplay, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("completion replaced records")
	}
	for i := range before {
		if before[i].Sequence != after[i].Sequence {
			t.Fatal("completion reparsed records")
		}
	}
}

func TestStructuredWriterReleasesCompletedGroups(t *testing.T) {
	w, env, exec := structuredTestWriter(t)
	var expected strings.Builder
	for i := 0; i < 12; i++ {
		events := []Event{{Type: EventTextDelta, Text: fmt.Sprint(i)}, {Type: EventToolUse, ToolName: "shell"}, {Type: EventToolResult, ToolName: "shell", Output: strings.Repeat("result\n", 16384)}}
		oracle := &Writer{}
		for _, e := range events {
			WriteEvent(w, e, false)
			WriteEvent(oracle, e, false)
		}
		expected.WriteString(oracle.String())
		w.flushPeriodicOnce()
		if w.buf.Len() != 0 || len(w.structured.pending) != 0 || w.structured.active != nil || len(w.structured.inflight) != 0 {
			t.Fatal("completed display data retained")
		}
	}
	replay, err := env.Executions.ReadOutputAfter(t.Context(), exec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Output != expected.String() || replay.Total != expected.Len() {
		t.Fatal("durable replay mismatch")
	}
	resumed := NewStructuredWriterWithPublisher(exec.ID, exec.TaskID, env.Executions, t.Context(), time.Hour, nil)
	defer resumed.Stop()
	if resumed.structured == nil || resumed.buf.Len() != 0 {
		t.Fatal("resume retained full buffer")
	}
	WriteEvent(resumed, Event{Type: EventTextDelta, Text: "continued"}, false)
	got, err := resumed.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got != expected.String()+"continued" {
		t.Fatal("resume lost output")
	}
}

type structuredFaultStore struct {
	executionOutputRepo
	StructuredOutputStore
	failAfterCommit bool
	entered         chan struct{}
	release         chan struct{}
	once            sync.Once
}

func (s *structuredFaultStore) AppendStructuredOutput(ctx context.Context, id string, c []models.ConversationChange) ([]models.ConversationChange, error) {
	if s.entered != nil {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	result, err := s.StructuredOutputStore.AppendStructuredOutput(ctx, id, c)
	if err == nil && s.failAfterCommit {
		s.failAfterCommit = false
		return nil, errors.New("lost acknowledgement")
	}
	return result, err
}

func TestStructuredWriterUncertainCommitRetry(t *testing.T) {
	w, env, exec := structuredTestWriter(t)
	fault := &structuredFaultStore{executionOutputRepo: w.repo, StructuredOutputStore: w.structured.store, failAfterCommit: true}
	w.structured.store = fault
	WriteEvent(w, Event{Type: EventTextDelta, Text: "first"}, false)
	w.flushPeriodicOnce()
	if w.structured.err == nil {
		t.Fatal("expected write failure")
	}
	WriteEvent(w, Event{Type: EventTextDelta, Text: " second"}, false)
	got, err := w.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got != "first second" {
		t.Fatalf("retry output %q", got)
	}
	saved, err := env.Executions.GetByID(t.Context(), exec.ID)
	if err != nil || saved.Output != got {
		t.Fatal("incorrect stored retry")
	}
}

func TestStructuredWriterBackgroundSaveDoesNotHoldWriterLock(t *testing.T) {
	w, _, _ := structuredTestWriter(t)
	fault := &structuredFaultStore{executionOutputRepo: w.repo, StructuredOutputStore: w.structured.store, entered: make(chan struct{}), release: make(chan struct{})}
	w.structured.store = fault
	WriteEvent(w, Event{Type: EventTextDelta, Text: "first"}, false)
	flushed := make(chan struct{})
	go func() { w.flushPeriodicOnce(); close(flushed) }()
	<-fault.entered
	accepted := make(chan struct{})
	go func() { WriteEvent(w, Event{Type: EventTextDelta, Text: " second"}, false); close(accepted) }()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		close(fault.release)
		t.Fatal("database write blocked live event")
	}
	close(fault.release)
	<-flushed
	got, err := w.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got != "first second" {
		t.Fatalf("snapshot race %q", got)
	}
}

func TestStructuredWriterCancellationPersistsPartialOutput(t *testing.T) {
	_, env, exec := structuredTestWriter(t)
	ctx, cancel := context.WithCancel(t.Context())
	w := NewStructuredWriterWithPublisher(exec.ID, exec.TaskID, env.Executions, ctx, time.Hour, nil)
	defer w.Stop()
	WriteEvent(w, Event{Type: EventThinkingOpen}, false)
	WriteEvent(w, Event{Type: EventThinkingText, Text: "unfinished reasoning"}, false)
	cancel()
	w.Flush()
	if err := w.PersistenceError(); err != nil {
		t.Fatal(err)
	}
	saved, err := env.Executions.GetByID(t.Context(), exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Output, "unfinished reasoning") {
		t.Fatal("cancellation lost partial output")
	}
	if err := env.Executions.Complete(t.Context(), exec.ID, models.ExecCompleted, "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.structured.finishActive()
	w.mu.Unlock()
	WriteEvent(w, Event{Type: EventTextDelta, Text: "late output"}, false)
	w.Flush()
	if w.PersistenceError() == nil {
		t.Fatal("late writes must fail")
	}
	after, err := env.Executions.GetByID(t.Context(), exec.ID)
	if err != nil || after.Output != saved.Output {
		t.Fatal("late write changed completed transcript")
	}
}

func TestStructuredWriterCompactionPreservesDisplay(t *testing.T) {
	w, _, _ := structuredTestWriter(t)
	old := &Writer{}
	for _, writer := range []*Writer{w, old} {
		WriteEvent(writer, Event{Type: EventTextDelta, Text: "before\n```go\nunfinished"}, false)
		report := CompactionReporter(writer, nil)
		report(llmcontracts.CompactionProgress{State: "started"})
		report(llmcontracts.CompactionProgress{State: "done", Duration: time.Second})
		WriteEvent(writer, Event{Type: EventTextDelta, Text: "after"}, false)
	}
	output, err := w.Result()
	if err != nil {
		t.Fatal(err)
	}
	if output != old.String() {
		t.Fatalf("compaction display mismatch: %q != %q", output, old.String())
	}
}

func TestStructuredWriterTimerSavesUnfinishedMessage(t *testing.T) {
	original, env, exec := structuredTestWriter(t)
	w := NewStructuredWriterWithPublisher(exec.ID, exec.TaskID, env.Executions, t.Context(), 5*time.Millisecond, nil)
	defer w.Stop()
	_ = original // idle helper writer performs no appends
	WriteEvent(w, Event{Type: EventThinkingOpen}, false)
	WriteEvent(w, Event{Type: EventThinkingText, Text: "not finished yet"}, false)
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("background writer failed to persist partial message")
		case <-tick.C:
			saved, err := env.Executions.ReadOutputAfter(t.Context(), exec.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(saved.Output, "not finished yet") {
				if strings.Contains(saved.Output, "[/Thinking]") {
					t.Fatal("partial record prematurely closed")
				}
				return
			}
		}
	}
}

func TestStructuredWriterPagedPlanAndReconnect(t *testing.T) {
	w, env, exec := structuredTestWriter(t)
	const plan = "<proposed_plan>keep approval available</proposed_plan>"
	WriteEvent(w, Event{Type: EventTextDelta, Text: plan[:8]}, false)
	w.flushPeriodicOnce()
	WriteEvent(w, Event{Type: EventTextDelta, Text: plan[8:]}, false)
	for i := 0; i < 6; i++ {
		WriteEvent(w, Event{Type: EventToolUse, ToolName: "shell"}, false)
		WriteEvent(w, Event{Type: EventToolResult, ToolName: "shell", Output: strings.Repeat("output\n", 1024)}, false)
		w.flushPeriodicOnce()
	}
	reader := env.Executions.(interface {
		ReadProjectDisplayTail(context.Context, string, string, int64, int) (models.DisplayTail, error)
	})
	page, err := reader.ReadProjectDisplayTail(t.Context(), "default", exec.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.Plan, plan) || page.Before == 0 {
		t.Fatal("plan or older page lost")
	}
	offset := page.EndOffset
	var full string
	for {
		full = page.Text + full
		if page.Before == 0 {
			break
		}
		next, err := reader.ReadProjectDisplayTail(t.Context(), "default", exec.ID, page.Before, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if next.Before == page.Before {
			t.Fatal("cursor did not advance")
		}
		page = next
	}
	expected, err := w.Output()
	if err != nil || full != expected {
		t.Fatal("pages did not reconstruct stream")
	}
	WriteEvent(w, Event{Type: EventTextDelta, Text: "latest"}, false)
	w.flushPeriodicOnce()
	delta, err := env.Executions.ReadOutputAfter(t.Context(), exec.ID, int(offset))
	if err != nil || delta.Output != "latest" {
		t.Fatalf("reconnect: %v %#v", err, delta)
	}
}
