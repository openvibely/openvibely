package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

type recordingMixtureAdapter struct {
	mu        sync.Mutex
	requests  []llmcontracts.AgentRequest
	responses map[string]llmcontracts.AgentResult
	errors    map[string]error
	onCall    func(llmcontracts.AgentRequest)
}

func (a *recordingMixtureAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	onCall := a.onCall
	res := a.responses[req.Agent.ID]
	err := a.errors[req.Agent.ID]
	a.mu.Unlock()
	if onCall != nil {
		onCall(req)
	}
	return res, err
}

func (a *recordingMixtureAdapter) Requests() []llmcontracts.AgentRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]llmcontracts.AgentRequest, len(a.requests))
	copy(out, a.requests)
	return out
}

func createMixtureTestConfig(t *testing.T, repo *repository.LLMConfigRepo, name string, provider models.LLMProvider, model string) models.LLMConfig {
	t.Helper()
	cfg := &models.LLMConfig{Name: name, Provider: provider, Model: model, AuthMethod: models.AuthMethodAPIKey}
	if err := repo.Create(context.Background(), cfg); err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	return *cfg
}

func newMixtureAdapterTestService(t *testing.T) (*LLMService, *repository.LLMConfigRepo, *recordingMixtureAdapter) {
	t.Helper()
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	recorder := &recordingMixtureAdapter{responses: map[string]llmcontracts.AgentResult{}, errors: map[string]error{}}
	svc := &LLMService{llmConfigRepo: repo, broadcaster: events.NewBroadcaster()}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderMixture: &mixtureProviderAdapter{svc: svc},
		models.ProviderTest:    recorder,
	}
	return svc, repo, recorder
}

func TestMixtureProviderAdapterReferenceIsolationAndAggregatorContext(t *testing.T) {
	svc, repo, recorder := newMixtureAdapterTestService(t)
	ref1 := createMixtureTestConfig(t, repo, "Reference One", models.ProviderTest, "ref-one")
	ref2 := createMixtureTestConfig(t, repo, "Reference Two", models.ProviderTest, "ref-two")
	agg := createMixtureTestConfig(t, repo, "Aggregator", models.ProviderTest, "agg")
	recorder.responses[ref1.ID] = llmcontracts.AgentResult{Output: "first advice", TextOnlyOutput: "first advice", Usage: llmcontracts.Usage{TotalTokens: 11, InputTokens: 5, OutputTokens: 6}}
	recorder.responses[ref2.ID] = llmcontracts.AgentResult{Output: "second advice", TextOnlyOutput: "second advice", Usage: llmcontracts.Usage{TotalTokens: 13}}
	recorder.responses[agg.ID] = llmcontracts.AgentResult{Output: "final answer", TextOnlyOutput: "final answer", Usage: llmcontracts.Usage{TotalTokens: 17}}

	mixtureCfg := &models.LLMConfig{
		Name:     "Mixture",
		Provider: models.ProviderMixture,
		Model:    "default",
		MixtureConfigJSON: `{"enabled":true,"reference_models":[{"agent_config_id":"` + ref1.ID + `","label":"First"},{"agent_config_id":"` + ref2.ID + `","label":"Second"}],` +
			`"aggregator":{"agent_config_id":"` + agg.ID + `","label":"Final"},"reference_timeout_seconds":90,"max_reference_workers":2}`,
	}
	adapter := svc.providerAdapters[models.ProviderMixture]
	agentDef := &models.Agent{Name: "Acting Agent"}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "send_message", Access: llmcontracts.RuntimeToolAccessWrite}}})
	res, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:                 ctx,
		Operation:           llmcontracts.OperationStreaming,
		Message:             "current user request",
		Attachments:         []models.Attachment{{FileName: "image.png"}},
		Agent:               *mixtureCfg,
		ExecID:              "exec-main",
		ChatHistory:         []models.Execution{{PromptSent: "prior prompt", Output: "prior answer"}},
		ChatSystemContext:   "private chat context",
		WorkDir:             "/tmp/work",
		AgentDefinition:     agentDef,
		ProjectInstructions: "project instructions",
	})
	if err != nil {
		t.Fatalf("mixture call: %v", err)
	}
	if res.Output != "final answer" {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Usage.TotalTokens != 41 {
		t.Fatalf("combined tokens = %d, want 41", res.Usage.TotalTokens)
	}
	if res.Usage.ProviderRaw["reference_1_total_tokens"] != 11 || res.Usage.ProviderRaw["reference_2_total_tokens"] != 13 || res.Usage.ProviderRaw["aggregator_total_tokens"] != 17 {
		t.Fatalf("provider raw usage not namespaced: %+v", res.Usage.ProviderRaw)
	}

	reqs := recorder.Requests()
	if len(reqs) != 3 {
		t.Fatalf("expected 3 provider calls, got %d", len(reqs))
	}
	var refReqs []llmcontracts.AgentRequest
	var aggReq llmcontracts.AgentRequest
	for _, req := range reqs {
		if req.Agent.ID == agg.ID {
			aggReq = req
		} else {
			refReqs = append(refReqs, req)
		}
	}
	if len(refReqs) != 2 {
		t.Fatalf("expected 2 reference calls, got %d", len(refReqs))
	}
	for _, req := range refReqs {
		if req.Operation != llmcontracts.OperationDirect || !req.DisableTools || !req.RawDirectPrompt || req.ExecID != "" || req.AgentDefinition != nil || len(req.Attachments) != 0 || req.ProjectInstructions != "" || req.ChatSystemContext != "" {
			t.Fatalf("reference request was not isolated: %+v", req)
		}
		if rt := llmcontracts.RuntimeToolsFromContext(req.Ctx); rt != nil {
			t.Fatalf("reference request inherited runtime tools: %#v", rt)
		}
		if !strings.Contains(req.Message, "You are a reference model") || !strings.Contains(req.Message, "current user request") || !strings.Contains(req.Message, "prior prompt") {
			t.Fatalf("reference advisory prompt missing expected content:\n%s", req.Message)
		}
	}
	if aggReq.Operation != llmcontracts.OperationStreaming || aggReq.RawDirectPrompt || aggReq.ExecID != "exec-main" || aggReq.AgentDefinition != agentDef || len(aggReq.Attachments) != 1 {
		t.Fatalf("aggregator did not preserve acting context: %+v", aggReq)
	}
	if rt := llmcontracts.RuntimeToolsFromContext(aggReq.Ctx); rt == nil || !rt.HasDefinition("send_message") {
		t.Fatalf("aggregator did not preserve runtime tools: %#v", rt)
	}
	if !strings.Contains(aggReq.ChatSystemContext, "[Mixture of Models private context]") || !strings.Contains(aggReq.ChatSystemContext, "first advice") || !strings.Contains(aggReq.ChatSystemContext, "second advice") {
		t.Fatalf("aggregator missing private reference context:\n%s", aggReq.ChatSystemContext)
	}
	if strings.Index(aggReq.ChatSystemContext, "first advice") > strings.Index(aggReq.ChatSystemContext, "second advice") {
		t.Fatalf("reference context order not preserved:\n%s", aggReq.ChatSystemContext)
	}
}

func TestMixtureProviderAdapterDisabledRunsAggregatorOnly(t *testing.T) {
	svc, repo, recorder := newMixtureAdapterTestService(t)
	ref := createMixtureTestConfig(t, repo, "Reference", models.ProviderTest, "ref")
	agg := createMixtureTestConfig(t, repo, "Aggregator", models.ProviderTest, "agg")
	recorder.responses[agg.ID] = llmcontracts.AgentResult{Output: "agg only", Usage: llmcontracts.Usage{TotalTokens: 5}}
	mixtureCfg := models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":false,"reference_models":[{"agent_config_id":"` + ref.ID + `"}],"aggregator":{"agent_config_id":"` + agg.ID + `"}}`}
	res, err := svc.providerAdapters[models.ProviderMixture].Call(llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationTask, Message: "run", Agent: mixtureCfg, ExecID: "exec"})
	if err != nil {
		t.Fatalf("mixture call: %v", err)
	}
	if res.Output != "agg only" {
		t.Fatalf("output = %q", res.Output)
	}
	reqs := recorder.Requests()
	if len(reqs) != 1 || reqs[0].Agent.ID != agg.ID || reqs[0].Operation != llmcontracts.OperationTask {
		t.Fatalf("expected aggregator-only call, got %+v", reqs)
	}
}

func TestMixtureProviderAdapterMissingAggregatorFailsFast(t *testing.T) {
	svc, _, _ := newMixtureAdapterTestService(t)
	mixtureCfg := models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":true,"reference_models":[],"aggregator":{"agent_config_id":"missing"}}`}
	_, err := svc.providerAdapters[models.ProviderMixture].Call(llmcontracts.AgentRequest{Ctx: context.Background(), Agent: mixtureCfg})
	if err == nil || !strings.Contains(err.Error(), "aggregator") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing aggregator error, got %v", err)
	}
}

func TestMixtureProviderAdapterMissingReferenceBecomesFailureNote(t *testing.T) {
	svc, repo, recorder := newMixtureAdapterTestService(t)
	agg := createMixtureTestConfig(t, repo, "Aggregator", models.ProviderTest, "agg")
	recorder.responses[agg.ID] = llmcontracts.AgentResult{Output: "final", Usage: llmcontracts.Usage{TotalTokens: 7}}
	mixtureCfg := models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":true,"reference_models":[{"agent_config_id":"missing","label":"Gone"}],"aggregator":{"agent_config_id":"` + agg.ID + `"}}`}
	_, err := svc.providerAdapters[models.ProviderMixture].Call(llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationTask, Message: "run", Agent: mixtureCfg})
	if err != nil {
		t.Fatalf("mixture call: %v", err)
	}
	reqs := recorder.Requests()
	if len(reqs) != 1 || reqs[0].Agent.ID != agg.ID {
		t.Fatalf("expected only aggregator provider call, got %+v", reqs)
	}
	if !strings.Contains(reqs[0].Message, "Reference 1 - Gone") || !strings.Contains(reqs[0].Message, "[failed: model config not found]") {
		t.Fatalf("missing reference failure note not passed to aggregator:\n%s", reqs[0].Message)
	}
}

func TestMixtureProviderAdapterReferenceTimeoutContinues(t *testing.T) {
	svc, repo, recorder := newMixtureAdapterTestService(t)
	ref := createMixtureTestConfig(t, repo, "Slow Reference", models.ProviderTest, "ref")
	agg := createMixtureTestConfig(t, repo, "Aggregator", models.ProviderTest, "agg")
	recorder.responses[agg.ID] = llmcontracts.AgentResult{Output: "final"}
	recorder.onCall = func(req llmcontracts.AgentRequest) {
		if req.Agent.ID == ref.ID {
			<-req.Ctx.Done()
		}
	}
	recorder.errors[ref.ID] = context.DeadlineExceeded
	mixtureCfg := models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":true,"reference_timeout_seconds":1,"reference_models":[{"agent_config_id":"` + ref.ID + `"}],"aggregator":{"agent_config_id":"` + agg.ID + `"}}`}
	_, err := svc.providerAdapters[models.ProviderMixture].Call(llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationTask, Message: "run", Agent: mixtureCfg})
	if err != nil {
		t.Fatalf("mixture call: %v", err)
	}
	reqs := recorder.Requests()
	if len(reqs) != 2 || reqs[len(reqs)-1].Agent.ID != agg.ID {
		t.Fatalf("expected timeout then aggregator, got %+v", reqs)
	}
	if !strings.Contains(reqs[len(reqs)-1].Message, "deadline exceeded") {
		t.Fatalf("timeout failure note missing:\n%s", reqs[len(reqs)-1].Message)
	}
}

func TestMixtureProviderAdapterCancelledContextPreventsAggregator(t *testing.T) {
	svc, repo, recorder := newMixtureAdapterTestService(t)
	ref := createMixtureTestConfig(t, repo, "Reference", models.ProviderTest, "ref")
	agg := createMixtureTestConfig(t, repo, "Aggregator", models.ProviderTest, "agg")
	ctx, cancel := context.WithCancel(context.Background())
	recorder.onCall = func(req llmcontracts.AgentRequest) {
		if req.Agent.ID == ref.ID {
			cancel()
		}
	}
	mixtureCfg := models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":true,"reference_models":[{"agent_config_id":"` + ref.ID + `"}],"aggregator":{"agent_config_id":"` + agg.ID + `"}}`}
	_, err := svc.providerAdapters[models.ProviderMixture].Call(llmcontracts.AgentRequest{Ctx: ctx, Operation: llmcontracts.OperationTask, Message: "run", Agent: mixtureCfg})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	for _, req := range recorder.Requests() {
		if req.Agent.ID == agg.ID {
			t.Fatalf("aggregator started after cancellation: %+v", req)
		}
	}
}

func TestMixtureProviderAdapterPublishesProgressWithoutExecutionOutput(t *testing.T) {
	svc, repo, recorder := newMixtureAdapterTestService(t)
	ref1 := createMixtureTestConfig(t, repo, "Reference One", models.ProviderTest, "ref-one")
	ref2 := createMixtureTestConfig(t, repo, "Reference Two", models.ProviderTest, "ref-two")
	ref3 := createMixtureTestConfig(t, repo, "Reference Three", models.ProviderTest, "ref-three")
	agg := createMixtureTestConfig(t, repo, "Aggregator", models.ProviderTest, "agg")
	recorder.responses[ref1.ID] = llmcontracts.AgentResult{Output: "advice one"}
	recorder.responses[ref2.ID] = llmcontracts.AgentResult{Output: "advice two"}
	recorder.responses[ref3.ID] = llmcontracts.AgentResult{Output: "advice three"}
	recorder.responses[agg.ID] = llmcontracts.AgentResult{Output: "final"}
	sub, err := svc.broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer svc.broadcaster.Unsubscribe(sub)
	mixtureCfg := models.LLMConfig{Provider: models.ProviderMixture, MixtureConfigJSON: `{"enabled":true,"max_reference_workers":3,"reference_models":[{"agent_config_id":"` + ref1.ID + `"},{"agent_config_id":"` + ref2.ID + `"},{"agent_config_id":"` + ref3.ID + `"}],"aggregator":{"agent_config_id":"` + agg.ID + `"}}`}
	_, err = svc.providerAdapters[models.ProviderMixture].Call(llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationTask, Message: "run", Agent: mixtureCfg, ExecID: "exec-progress"})
	if err != nil {
		t.Fatalf("mixture call: %v", err)
	}
	var phases []string
	var completed []int
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-sub:
			if ev.Type == events.MixtureProgress && ev.ExecID == "exec-progress" {
				phases = append(phases, ev.Phase)
				if ev.Phase == "reference_complete" {
					completed = append(completed, ev.CompletedReferences)
					if ev.TotalReferences != 3 {
						t.Fatalf("total references = %d, want 3", ev.TotalReferences)
					}
				}
				if ev.Phase == "aggregator_starting" {
					joined := strings.Join(phases, ",")
					if !strings.Contains(joined, "running_references") || !strings.Contains(joined, "references_complete") {
						t.Fatalf("missing expected phases: %v", phases)
					}
					if len(completed) != 3 || completed[0] != 1 || completed[1] != 2 || completed[2] != 3 {
						t.Fatalf("completed reference counts = %v, want [1 2 3]", completed)
					}
					return
				}
			}
		case <-deadline:
			t.Fatalf("missing aggregator_starting progress event, phases=%v completed=%v", phases, completed)
		}
	}
}
