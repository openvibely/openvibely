package service

// Opt-in end-to-end measurement. The runner starts the existing mock model in a
// separate process; no user database, credentials, or external endpoint is used.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/transcript"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

type checkpointUncachedStore struct {
	persistence.ExecutionStore
	reads int
}

func (s *checkpointUncachedStore) GetChatCompactionCheckpoint(ctx context.Context, typ, id string) (*models.ChatCompactionCheckpoint, error) {
	s.reads++
	return s.ExecutionStore.GetChatCompactionCheckpoint(ctx, typ, id)
}

// Preserve the production row reader through both instrumentation wrappers.
// Embedding ExecutionStore alone hides this optional interface and accidentally
// benchmarks legacy full-transcript materialization instead.
type checkpointBenchmarkStore struct{ *countingCheckpointStore }

func checkpointBenchmarkRows(ctx context.Context, store persistence.ExecutionStore, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return store.(interface {
		ReplayRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
	}).ReplayRowsByExecutionIDs(ctx, ids)
}
func (s *checkpointBenchmarkStore) ReplayRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return checkpointBenchmarkRows(ctx, s.ExecutionStore, ids)
}
func (s *checkpointUncachedStore) ReplayRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return checkpointBenchmarkRows(ctx, s.ExecutionStore, ids)
}

func (s *checkpointBenchmarkStore) ReplayContinuationRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return checkpointBenchmarkContinuation(ctx, s.ExecutionStore, ids)
}
func (s *checkpointUncachedStore) ReplayContinuationRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return checkpointBenchmarkContinuation(ctx, s.ExecutionStore, ids)
}
func checkpointBenchmarkContinuation(ctx context.Context, store persistence.ExecutionStore, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	return store.(interface {
		ReplayContinuationRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
	}).ReplayContinuationRowsByExecutionIDs(ctx, ids)
}

func syntheticImageCheckpoint(t testing.TB, cp *models.ChatCompactionCheckpoint, provider models.LLMProvider, encodedBytes int) {
	t.Helper()
	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 32, 32))))
	// Valid PNG followed by inert padding: deterministic, decodable dimensions,
	// and a large base64 payload without checking any binary fixture into Git.
	data := make([]byte, encodedBytes/4*3)
	copy(data, pngData.Bytes())
	encoded := base64.StdEncoding.EncodeToString(data)
	text := map[string]any{"type": "text", "text": "APPFLOW_COMPACT CHECKPOINT_FIXTURE_V1"}
	img := map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + encoded}}
	var state any
	switch provider {
	case models.ProviderOpenAI:
		text["type"] = "input_text"
		img = map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + encoded}
		state = []any{map[string]any{"type": "message", "role": "user", "content": []any{text, img}}}
		cp.Strategy = "openai_responses"
		cp.History = nil
	case models.ProviderAnthropic:
		img = map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": encoded}}
		state = map[string]any{"version": 1, "messages": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "compaction", "content": "APPFLOW_COMPACT CHECKPOINT_FIXTURE_V1"}}}, map[string]any{"role": "user", "content": []any{text, img}}}}
		cp.Strategy = "anthropic_context_management"
		cp.History = nil
	default:
		state = []any{map[string]any{"role": "user", "content": []any{text, img}}}
	}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	if provider == models.ProviderOpenAI || provider == models.ProviderAnthropic {
		cp.ProviderStateJSON = string(raw)
	} else {
		cp.History = []models.Execution{{ID: "synthetic-checkpoint", PromptSent: "CHECKPOINT_FIXTURE_V1", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
	}
}

type checkpointSample struct {
	Turn         int     `json:"turn"`
	PayloadReads int     `json:"payload_reads"`
	Allocated    uint64  `json:"allocated_bytes"`
	HeapAfterGC  uint64  `json:"heap_after_gc_bytes"`
	DurationMS   float64 `json:"duration_ms"`
}

func TestCheckpointFollowupMock(t *testing.T) {
	peer := os.Getenv("OV_CHECKPOINT_MODEL_URL")
	if peer == "" {
		t.Skip("run scripts/replay-memory-bench/checkpoint/run.py")
	}
	u, err := url.Parse(peer)
	require.NoError(t, err)
	require.Equal(t, "http", u.Scheme)
	require.Equal(t, "127.0.0.1", u.Hostname())
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	protocol := os.Getenv("OV_CHECKPOINT_PROVIDER")
	provider := models.LLMProvider(protocol)
	if protocol == "openai-compatible" {
		provider = models.ProviderOpenAICompatible
	}
	require.Contains(t, []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible}, provider)
	mode := os.Getenv("OV_CHECKPOINT_MODE")
	require.Contains(t, []string{"reuse", "reload", "update", "evict"}, mode)
	turns := 6
	if raw := os.Getenv("OV_CHECKPOINT_TURNS"); raw != "" {
		turns, err = strconv.Atoi(raw)
		require.NoError(t, err)
		require.GreaterOrEqual(t, turns, 4)
	}
	oldOpenAI, oldAnthropic := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
	openaiclient.OpenAIAPIBaseURL = peer + "/v1/"
	anthropicclient.AnthropicAPIHost = peer
	defer func() { openaiclient.OpenAIAPIBaseURL = oldOpenAI; anthropicclient.AnthropicAPIHost = oldAnthropic }()
	svc, read, req, cp := checkpointResumeFixture(t, provider, 128, 2)
	original := read.ExecutionStore
	counted := &checkpointBenchmarkStore{&countingCheckpointStore{ExecutionStore: original, revisions: original.(checkpointRevisionStore)}}
	uncached := &checkpointUncachedStore{ExecutionStore: original}
	svc.execRepo = counted
	if mode == "reload" {
		svc.execRepo = uncached
	}
	t.Cleanup(func() {
		svc.checkpointCache.mu.Lock()
		defer svc.checkpointCache.mu.Unlock()
		for k := range svc.checkpointCache.entries {
			svc.checkpointCache.remove(k)
		}
	})
	req.Followup = true
	req.ProjectID = "default"
	req.DisableTools = true
	req.Agent.AuthMethod = models.AuthMethodAPIKey
	req.Agent.APIKey = "local-mock-only"
	req.Agent.Model = "gpt-5.3-codex"
	if provider == models.ProviderAnthropic {
		req.Agent.Model = "claude-opus-5"
	}
	if provider == models.ProviderOpenAICompatible {
		req.Agent.Model = "benchmark-compatible"
		req.Agent.BaseURL = peer + "/v1"
		req.Agent.CustomAuthConfigJSON = `{"allow_private_endpoints":true}`
	}
	req.Agent.ContextWindow = 100000000
	req.Agent.ProviderContextWindow = 100000000
	req.Agent.CompactionThreshold = 75000000
	cp.CompatibilityKey = providerCompatibilityKey(req.Agent)
	syntheticImageCheckpoint(t, &cp, provider, 11<<20)
	require.NoError(t, original.UpsertChatCompactionCheckpoint(req.Ctx, cp))
	// Do not keep the generated serialized image alive alongside app state.
	cp.ProviderStateJSON = ""
	cp.History = nil
	adapter, ok := svc.adapterFor(provider)
	require.True(t, ok)
	client := &http.Client{Timeout: 10 * time.Second}
	var samples []checkpointSample
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	for turn := 0; turn < turns; turn++ {
		if turn == 2 && mode == "update" {
			updated, err := original.GetChatCompactionCheckpoint(req.Ctx, cp.ScopeType, cp.ScopeID)
			require.NoError(t, err)
			updated.Summary = "changed checkpoint metadata"
			require.NoError(t, original.UpsertChatCompactionCheckpoint(req.Ctx, *updated))
		}
		if turn == 2 && mode == "evict" {
			svc.checkpointCache.mu.Lock()
			for k := range svc.checkpointCache.entries {
				svc.checkpointCache.remove(k)
			}
			svc.checkpointCache.mu.Unlock()
		}
		control := fmt.Sprintf(`{"turn":%d,"output_bytes":256,"delta_ms":0}`, turn)
		response, err := client.Post(peer+"/control", "application/json", bytes.NewBufferString(control))
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, 200, response.StatusCode)
		execution := models.Execution{TaskID: cp.ScopeID, Status: models.ExecRunning, PromptSent: fmt.Sprintf("follow-up %d", turn), IsFollowup: true}
		require.NoError(t, original.Create(req.Ctx, &execution))
		req.ExecID = execution.ID
		req.Message = execution.PromptSent
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		result, err := svc.callProviderWithCompaction(adapter, req)
		duration := time.Since(start)
		require.NoError(t, err)
		require.Contains(t, result.Output, fmt.Sprintf("TURN_%d_END", turn))
		runtime.ReadMemStats(&after)
		allocated := after.TotalAlloc - before.TotalAlloc
		execution.Status = models.ExecCompleted
		execution.Output = result.Output
		require.NoError(t, original.Complete(req.Ctx, execution.ID, models.ExecCompleted, "", "", 0, 0))
		// Read physical records outside the measured interval. Logical replay
		// readers intentionally reconstruct a complete snapshot for recovery.
		if provider == models.ProviderOpenAI && mode == "reuse" && turn > 0 {
			heads, err := original.ReadConversationHeads(req.Ctx, models.ConversationOwner{ExecutionID: execution.ID}, "", 0)
			require.NoError(t, err)
			frames, err := transcript.RestoreReplayRecords(heads)
			require.NoError(t, err)
			require.Len(t, frames, 1)
			require.Contains(t, frames[0].TranscriptJSON, `"responses_delta"`)
			require.Less(t, len(frames[0].TranscriptJSON), 64*1024, "ordinary follow-ups must not save the large checkpoint again")
		}
		req.ChatHistory = append(req.ChatHistory, execution)
		runtime.GC()
		runtime.ReadMemStats(&after)
		loads := counted.payloads + uncached.reads
		expected := 1
		if mode == "reload" {
			expected = turn + 1
		}
		if turn >= 2 && (mode == "update" || mode == "evict") {
			expected = 2
		}
		require.Equal(t, expected, loads, "unexpected checkpoint reload")
		samples = append(samples, checkpointSample{turn, loads, allocated, after.HeapAlloc, float64(duration.Microseconds()) / 1000})
	}
	response, err := client.Get(peer + "/stats")
	require.NoError(t, err)
	defer response.Body.Close()
	var stats struct {
		Requests []struct {
			Provider     string `json:"provider"`
			ContextBytes int    `json:"context_bytes"`
			Checkpoint   bool   `json:"checkpoint_seen"`
			Prior        bool   `json:"prior_turn_seen"`
		} `json:"requests"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&stats))
	require.Len(t, stats.Requests, turns)
	for _, r := range stats.Requests {
		require.Equal(t, protocol, r.Provider)
		require.True(t, r.Checkpoint, "checkpoint missing from model input")
		require.GreaterOrEqual(t, r.ContextBytes, 11<<20, "image dropped from model input")
		require.True(t, r.Prior, "previous answer missing")
	}
	output := struct {
		Provider     models.LLMProvider
		Mode         string
		BaselineHeap uint64
		Samples      []checkpointSample
	}{provider, mode, baseline.HeapAlloc, samples}
	encoded, err := json.MarshalIndent(output, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("OV_CHECKPOINT_RESULT"), encoded, 0600))
}

func TestCheckpointBenchmarkUsesProductionReplayRows(t *testing.T) {
	_, read, req, _ := checkpointResumeFixture(t, models.ProviderOpenAI, 1024, 2)
	original := read.ExecutionStore
	cached := &checkpointBenchmarkStore{&countingCheckpointStore{ExecutionStore: original, revisions: original.(checkpointRevisionStore)}}
	uncached := &checkpointUncachedStore{ExecutionStore: original}
	type rowReader interface {
		ReplayContinuationRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
		ReplayRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
	}
	for _, store := range []persistence.ExecutionStore{cached, uncached} {
		rows, ok := store.(rowReader)
		require.True(t, ok)
		id := req.ChatHistory[0].ID
		replay, err := rows.ReplayContinuationRowsByExecutionIDs(req.Ctx, []string{id})
		require.NoError(t, err)
		require.NotEmpty(t, replay[id])
		require.NotNil(t, replay[id][0].Rows)
		require.Empty(t, replay[id][0].TranscriptJSON, "benchmark must not materialize compatibility JSON")
	}
}
