package models

import (
	"testing"
)

func TestWorkflowConfig_ParseAndSet(t *testing.T) {
	w := &Workflow{Config: "{}"}
	cfg, err := w.ParseWorkflowConfig()
	if err != nil {
		t.Fatalf("ParseWorkflowConfig error: %v", err)
	}
	// Defaults
	if cfg.MaxRetries != 1 {
		t.Errorf("expected MaxRetries=1, got %d", cfg.MaxRetries)
	}
	if cfg.QualityThreshold != 0.7 {
		t.Errorf("expected QualityThreshold=0.7, got %f", cfg.QualityThreshold)
	}

	// Set and re-parse
	cfg.MaxRetries = 3
	cfg.AutoRollback = true
	cfg.MaxCostCents = 500
	if err := w.SetWorkflowConfig(cfg); err != nil {
		t.Fatalf("SetWorkflowConfig error: %v", err)
	}

	cfg2, err := w.ParseWorkflowConfig()
	if err != nil {
		t.Fatalf("ParseWorkflowConfig (2nd) error: %v", err)
	}
	if cfg2.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", cfg2.MaxRetries)
	}
	if !cfg2.AutoRollback {
		t.Error("expected AutoRollback=true")
	}
	if cfg2.MaxCostCents != 500 {
		t.Errorf("expected MaxCostCents=500, got %d", cfg2.MaxCostCents)
	}
}

func TestStepConfig_ParseAndSet(t *testing.T) {
	s := &WorkflowStep{Config: "{}"}
	cfg, err := s.ParseStepConfig()
	if err != nil {
		t.Fatalf("ParseStepConfig error: %v", err)
	}
	if cfg.PassThreshold != 0 {
		t.Errorf("expected PassThreshold=0, got %f", cfg.PassThreshold)
	}

	cfg.PassThreshold = 0.8
	cfg.FailAction = "retry"
	cfg.MaxIterations = 3
	cfg.VoterAgentIDs = []string{"agent1", "agent2"}
	if err := s.SetStepConfig(cfg); err != nil {
		t.Fatalf("SetStepConfig error: %v", err)
	}

	cfg2, err := s.ParseStepConfig()
	if err != nil {
		t.Fatalf("ParseStepConfig (2nd) error: %v", err)
	}
	if cfg2.PassThreshold != 0.8 {
		t.Errorf("expected PassThreshold=0.8, got %f", cfg2.PassThreshold)
	}
	if cfg2.FailAction != "retry" {
		t.Errorf("expected FailAction=retry, got %s", cfg2.FailAction)
	}
	if len(cfg2.VoterAgentIDs) != 2 {
		t.Errorf("expected 2 voter agent IDs, got %d", len(cfg2.VoterAgentIDs))
	}
}

func TestStepDependsOn_ParseAndSet(t *testing.T) {
	s := &WorkflowStep{DependsOn: "[]"}
	deps, err := s.ParseDependsOn()
	if err != nil {
		t.Fatalf("ParseDependsOn error: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("expected 0 deps, got %d", len(deps))
	}

	if err := s.SetDependsOn([]string{"step1", "step2"}); err != nil {
		t.Fatalf("SetDependsOn error: %v", err)
	}

	deps2, err := s.ParseDependsOn()
	if err != nil {
		t.Fatalf("ParseDependsOn (2nd) error: %v", err)
	}
	if len(deps2) != 2 {
		t.Errorf("expected 2 deps, got %d", len(deps2))
	}
	if deps2[0] != "step1" || deps2[1] != "step2" {
		t.Errorf("unexpected deps: %v", deps2)
	}

	// Test empty
	if err := s.SetDependsOn(nil); err != nil {
		t.Fatalf("SetDependsOn(nil) error: %v", err)
	}
	if s.DependsOn != "[]" {
		t.Errorf("expected DependsOn=[], got %s", s.DependsOn)
	}
}

func TestHandoffContext_ParseAndJSON(t *testing.T) {
	hctx, err := ParseHandoffContext("{}")
	if err != nil {
		t.Fatalf("ParseHandoffContext error: %v", err)
	}
	if hctx.StepOutputs == nil {
		t.Error("expected StepOutputs to be non-nil")
	}

	hctx.StepOutputs["step1"] = "output from step 1"
	hctx.Summary = "Test summary"
	hctx.Decisions = []string{"Use pattern A", "Avoid pattern B"}
	hctx.TaskPrompt = "original prompt"

	jsonStr, err := hctx.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}

	hctx2, err := ParseHandoffContext(jsonStr)
	if err != nil {
		t.Fatalf("ParseHandoffContext (2nd) error: %v", err)
	}
	if hctx2.StepOutputs["step1"] != "output from step 1" {
		t.Error("step output not preserved")
	}
	if hctx2.Summary != "Test summary" {
		t.Errorf("expected summary='Test summary', got %q", hctx2.Summary)
	}
	if len(hctx2.Decisions) != 2 {
		t.Errorf("expected 2 decisions, got %d", len(hctx2.Decisions))
	}
}

func TestTemplateDefinition_ParseAndSet(t *testing.T) {
	tmpl := &WorkflowTemplate{Definition: "{}"}
	def, err := tmpl.ParseTemplateDefinition()
	if err != nil {
		t.Fatalf("ParseTemplateDefinition error: %v", err)
	}
	if def.Strategy != "" {
		t.Errorf("expected empty strategy, got %q", def.Strategy)
	}

	def.Strategy = StrategyHybrid
	def.Steps = []TemplateStep{
		{Name: "Step1", StepType: StepTypeExecute, StepOrder: 0, AgentRole: "planner"},
		{Name: "Step2", StepType: StepTypeReview, StepOrder: 1, AgentRole: "reviewer"},
	}
	if err := tmpl.SetTemplateDefinition(def); err != nil {
		t.Fatalf("SetTemplateDefinition error: %v", err)
	}

	def2, err := tmpl.ParseTemplateDefinition()
	if err != nil {
		t.Fatalf("ParseTemplateDefinition (2nd) error: %v", err)
	}
	if def2.Strategy != StrategyHybrid {
		t.Errorf("expected strategy=hybrid, got %q", def2.Strategy)
	}
	if len(def2.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(def2.Steps))
	}
	if def2.Steps[0].AgentRole != "planner" {
		t.Errorf("expected step0 role=planner, got %q", def2.Steps[0].AgentRole)
	}
}

func TestAgentPerformanceMetric_SuccessRate(t *testing.T) {
	tests := []struct {
		name     string
		success  int
		failure  int
		expected float64
	}{
		{"all success", 10, 0, 100.0},
		{"all failure", 0, 10, 0.0},
		{"no data", 0, 0, 0.0},
		{"mixed", 7, 3, 70.0},
		{"one each", 1, 1, 50.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &AgentPerformanceMetric{SuccessCount: tt.success, FailureCount: tt.failure}
			rate := m.SuccessRate()
			if rate != tt.expected {
				t.Errorf("expected success rate=%.1f, got %.1f", tt.expected, rate)
			}
		})
	}
}
