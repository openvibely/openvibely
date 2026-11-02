package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

type revisionFixture struct {
	persistence.ExecutionStore
	value   *models.ChatCompactionCheckpoint
	version string
	loads   int
	err     error
}

func (r *revisionFixture) GetChatCompactionCheckpointIfChanged(_ context.Context, _, _, previous string) (*models.ChatCompactionCheckpoint, string, error) {
	if r.err != nil {
		return nil, "", r.err
	}
	if r.value == nil {
		return nil, "", nil
	}
	if previous == r.version {
		return nil, r.version, nil
	}
	r.loads++
	return cloneActiveCheckpoint(*r.value), r.version, nil
}
func TestActiveCheckpointReuseAndInvalidation(t *testing.T) {
	store := &revisionFixture{value: &models.ChatCompactionCheckpoint{ProviderStateJSON: `[{"role":"user","content":"hello"}]`, History: []models.Execution{{Output: "original"}}}, version: "1"}
	svc := &LLMService{execRepo: store}
	ctx := context.Background()
	first, prepared, err := svc.loadActiveCheckpoint(ctx, "task", "one")
	require.NoError(t, err)
	first.History[0].Output = "mutated"
	second, reused, err := svc.loadActiveCheckpoint(ctx, "task", "one")
	require.NoError(t, err)
	require.Equal(t, 1, store.loads)
	require.Same(t, prepared, reused)
	require.Equal(t, "original", second.History[0].Output)
	store.version = "2"
	store.value.History[0].Output = "new"
	second, reused, err = svc.loadActiveCheckpoint(ctx, "task", "one")
	require.NoError(t, err)
	require.Equal(t, 2, store.loads)
	require.NotSame(t, prepared, reused)
	require.Equal(t, "new", second.History[0].Output)
	store.err = errors.New("read failed")
	second, _, err = svc.loadActiveCheckpoint(ctx, "task", "one")
	require.Error(t, err)
	require.Nil(t, second)
	store.err = nil
	store.value = nil
	second, _, err = svc.loadActiveCheckpoint(ctx, "task", "one")
	require.NoError(t, err)
	require.Nil(t, second)
	require.Empty(t, svc.checkpointCache.entries)
}
func TestActiveCheckpointBudgetAndEviction(t *testing.T) {
	store := &revisionFixture{value: &models.ChatCompactionCheckpoint{ProviderStateJSON: strings.Repeat("x", 8<<20)}, version: "1"}
	svc := &LLMService{execRepo: store}
	for _, id := range []string{"one", "two", "three"} {
		_, _, err := svc.loadActiveCheckpoint(context.Background(), "task", id)
		require.NoError(t, err)
	}
	require.Len(t, svc.checkpointCache.entries, 2)
	require.Nil(t, svc.checkpointCache.entries[[2]string{"task", "one"}])
	store.value.ProviderStateJSON = strings.Repeat("x", activeCheckpointBudget/3+1)
	store.version = "2"
	_, prepared, err := svc.loadActiveCheckpoint(context.Background(), "task", "three")
	require.NoError(t, err)
	require.Nil(t, prepared)
	require.Nil(t, svc.checkpointCache.entries[[2]string{"task", "three"}])
	for key := range svc.checkpointCache.entries {
		svc.checkpointCache.remove(key)
	}
}
func TestActiveCheckpointIdleExpiry(t *testing.T) {
	cache := &activeCheckpointCache{idle: time.Millisecond, entries: make(map[[2]string]*activeCheckpointEntry)}
	key := [2]string{"task", "one"}
	entry := &activeCheckpointEntry{}
	cache.entries[key] = entry
	cache.touch(key, entry)
	require.Eventually(t, func() bool { cache.mu.Lock(); defer cache.mu.Unlock(); return len(cache.entries) == 0 }, time.Second, time.Millisecond)
}

type countingCheckpointStore struct {
	persistence.ExecutionStore
	revisions checkpointRevisionStore
	payloads  int
}

func (r *countingCheckpointStore) GetChatCompactionCheckpointIfChanged(ctx context.Context, typ, id, version string) (*models.ChatCompactionCheckpoint, string, error) {
	c, v, e := r.revisions.GetChatCompactionCheckpointIfChanged(ctx, typ, id, version)
	if c != nil {
		r.payloads++
	}
	return c, v, e
}
func TestActiveCheckpointRestoreAcrossProviders(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			svc, read, req, cp := checkpointResumeFixture(t, provider, 1024, 2)
			store := &countingCheckpointStore{ExecutionStore: read.ExecutionStore, revisions: read.ExecutionStore.(checkpointRevisionStore)}
			svc.execRepo = store
			require.NoError(t, store.UpsertChatCompactionCheckpoint(req.Ctx, cp))
			for range 3 {
				restored := svc.restoreCompactionCheckpoint(req)
				require.True(t, resumeHasSummary(restored.ChatHistory))
			}
			require.Equal(t, 1, store.payloads)
			cp.History[0].PromptSent = "changed summary"
			require.NoError(t, store.UpsertChatCompactionCheckpoint(req.Ctx, cp))
			restored := svc.restoreCompactionCheckpoint(req)
			require.Equal(t, 2, store.payloads)
			found := false
			for _, e := range restored.ChatHistory {
				if e.PromptSent == "changed summary" {
					found = true
				}
			}
			require.True(t, found)
			svc.checkpointCache.mu.Lock()
			for key := range svc.checkpointCache.entries {
				svc.checkpointCache.remove(key)
			}
			svc.checkpointCache.mu.Unlock()
		})
	}
}
