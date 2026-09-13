package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

type countingAgentLookup struct {
	mu     sync.Mutex
	agents map[string]*models.Agent
	errors map[string]error
	counts map[string]int
}

func (l *countingAgentLookup) GetByID(_ context.Context, id string) (*models.Agent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[id]++
	return l.agents[id], l.errors[id]
}

func (l *countingAgentLookup) count(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[id]
}

func TestAgentDefinitionCacheMemoizesDistinctDefinitionsAndFailures(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	assigned := &models.Agent{ID: "assigned", Key: "task-agent"}
	custom := &models.Agent{ID: "custom", Key: "custom-agent"}
	lookup := &countingAgentLookup{
		agents: map[string]*models.Agent{"assigned": assigned, "custom": custom},
		errors: map[string]error{"broken": lookupErr},
		counts: map[string]int{},
	}
	cache := NewAgentDefinitionCache(lookup)
	cache.Seed(assigned)

	if got, err := cache.GetByID(context.Background(), "assigned"); err != nil || got != assigned {
		t.Fatalf("seeded assigned definition = %p, %v; want %p, nil", got, err, assigned)
	}
	if got, err := cache.GetByID(context.Background(), "custom"); err != nil || got != custom {
		t.Fatalf("custom definition = %p, %v; want %p, nil", got, err, custom)
	}
	if got, err := cache.GetByID(context.Background(), "custom"); err != nil || got != custom {
		t.Fatalf("cached custom definition = %p, %v; want %p, nil", got, err, custom)
	}
	for i := 0; i < 2; i++ {
		if _, err := cache.GetByID(context.Background(), "broken"); !errors.Is(err, lookupErr) {
			t.Fatalf("broken lookup error = %v; want %v", err, lookupErr)
		}
	}
	if got := lookup.count("assigned"); got != 0 {
		t.Fatalf("seeded assigned lookup count = %d; want 0", got)
	}
	if got := lookup.count("custom"); got != 1 {
		t.Fatalf("custom lookup count = %d; want 1", got)
	}
	if got := lookup.count("broken"); got != 1 {
		t.Fatalf("failed lookup count = %d; want 1", got)
	}
}

func TestAgentDefinitionCacheSerializesConcurrentSameIDReads(t *testing.T) {
	agent := &models.Agent{ID: "agent-1"}
	lookup := &countingAgentLookup{agents: map[string]*models.Agent{"agent-1": agent}, counts: map[string]int{}}
	cache := NewAgentDefinitionCache(lookup)

	const callers = 16
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.GetByID(context.Background(), "agent-1")
			if err != nil || got != agent {
				t.Errorf("concurrent lookup = %p, %v; want %p, nil", got, err, agent)
			}
		}()
	}
	wg.Wait()
	if got := lookup.count("agent-1"); got != 1 {
		t.Fatalf("concurrent lookup count = %d; want 1", got)
	}
}

func TestAgentDefinitionCacheIsFreshPerTurn(t *testing.T) {
	first := &models.Agent{ID: "agent-1", Name: "first"}
	second := &models.Agent{ID: "agent-1", Name: "second"}
	lookup := &countingAgentLookup{agents: map[string]*models.Agent{"agent-1": first}, counts: map[string]int{}}
	firstTurn := NewAgentDefinitionCache(lookup)
	got, err := firstTurn.GetByID(context.Background(), "agent-1")
	if err != nil || got != first {
		t.Fatalf("first turn lookup = %p, %v; want %p, nil", got, err, first)
	}

	lookup.mu.Lock()
	lookup.agents["agent-1"] = second
	lookup.mu.Unlock()
	secondTurn := NewAgentDefinitionCache(lookup)
	got, err = secondTurn.GetByID(context.Background(), "agent-1")
	if err != nil || got != second {
		t.Fatalf("second turn lookup = %p, %v; want %p, nil", got, err, second)
	}
	if got := lookup.count("agent-1"); got != 2 {
		t.Fatalf("fresh-turn lookup count = %d; want 2", got)
	}
}

func TestLLMHookInvokerUsesTurnAgentDefinitionCache(t *testing.T) {
	assigned := &models.Agent{ID: "assigned", Name: "Assigned"}
	custom := &models.Agent{ID: "custom", Name: "Custom"}
	lookup := &countingAgentLookup{
		agents: map[string]*models.Agent{"assigned": assigned, "custom": custom},
		counts: map[string]int{},
	}
	caller := &fakeCaller{reply: `{"summary":"ok","changed_paths":[]}`}
	invoker := NewLLMHookInvoker(caller, lookup, nil)
	ctx := WithAgentDefinitionCache(context.Background(), NewAgentDefinitionCache(lookup))

	for _, hook := range []models.AgentLifecycleHook{
		{ID: "assigned-1", AgentID: assigned.ID, OutputContract: models.OutputContractActivitySummary},
		{ID: "assigned-2", AgentID: assigned.ID, OutputContract: models.OutputContractActivitySummary},
		{ID: "custom", AgentID: custom.ID, OutputContract: models.OutputContractActivitySummary},
		{ID: "custom-2", AgentID: custom.ID, OutputContract: models.OutputContractActivitySummary},
	} {
		if _, err := invoker.Invoke(ctx, hook, HookInput{TaskID: "task", TaskRunID: hook.ID}); err != nil {
			t.Fatalf("invoke %s: %v", hook.ID, err)
		}
	}
	if got := lookup.count(assigned.ID); got != 1 {
		t.Fatalf("assigned hook Agent lookup count = %d; want 1", got)
	}
	if got := lookup.count(custom.ID); got != 1 {
		t.Fatalf("custom hook Agent lookup count = %d; want 1", got)
	}
	if caller.lastAgentDef != custom {
		t.Fatalf("last hook received %p; want custom definition %p", caller.lastAgentDef, custom)
	}
}
