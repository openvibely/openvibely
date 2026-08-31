package models

import (
	"strings"
	"testing"
)

func TestSwarmConfigNormalizesLegacyIntegratorFields(t *testing.T) {
	cfg, err := ParseSwarmConfig(`{
		"integrator_enabled": true,
		"rerun_integrator_after_reviewer": true,
		"integrated_generation": 4,
		"merged_generation": 2,
		"integrator_prompt": "merge safely"
	}`)
	if err != nil {
		t.Fatalf("ParseSwarmConfig: %v", err)
	}
	if !cfg.MergerEnabled || !cfg.RerunMergerAfterReviewer || cfg.MergedGeneration != 4 || cfg.MergerPrompt != "merge safely" {
		t.Fatalf("legacy integrator fields were not normalized: %#v", cfg)
	}
	if cfg.IntegratorEnabled || cfg.RerunIntegratorAfterReviewer || cfg.IntegratedGeneration != 0 || cfg.IntegratorPrompt != "" {
		t.Fatalf("legacy integrator fields should be cleared after normalization: %#v", cfg)
	}

	out, err := cfg.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(out, "integrator_enabled") || strings.Contains(out, "integrator_prompt") {
		t.Fatalf("serialized config should omit cleared legacy fields: %s", out)
	}

	empty, err := ParseSwarmConfig("")
	if err != nil || empty.MergerEnabled {
		t.Fatalf("empty config should parse to zero value: %#v err=%v", empty, err)
	}
	if _, err := ParseSwarmConfig(`{"bad":`); err == nil {
		t.Fatal("expected malformed swarm config to fail")
	}
}

func TestTaskChainConfigRoundTripsDisabledNilAndEnabled(t *testing.T) {
	var task Task
	parsed, err := task.ParseChainConfig()
	if err != nil {
		t.Fatalf("ParseChainConfig empty: %v", err)
	}
	if parsed.Enabled {
		t.Fatalf("empty chain config should be disabled: %#v", parsed)
	}

	if err := task.SetChainConfig(nil); err != nil {
		t.Fatalf("SetChainConfig nil: %v", err)
	}
	if task.ChainConfig != "{}" {
		t.Fatalf("nil chain config should serialize as empty object, got %q", task.ChainConfig)
	}

	if err := task.SetChainConfig(&ChainConfiguration{Enabled: true, Trigger: "on_completion", ChildTitle: "Follow-up", ChildChainConfig: &ChainConfiguration{Enabled: true, Trigger: "on_planning_complete"}}); err != nil {
		t.Fatalf("SetChainConfig enabled: %v", err)
	}
	parsed, err = task.ParseChainConfig()
	if err != nil {
		t.Fatalf("ParseChainConfig enabled: %v", err)
	}
	if !parsed.Enabled || parsed.Trigger != "on_completion" || parsed.ChildChainConfig == nil || parsed.ChildChainConfig.Trigger != "on_planning_complete" {
		t.Fatalf("enabled chain config did not round trip: %#v", parsed)
	}

	task.ChainConfig = `{"enabled":`
	if _, err := task.ParseChainConfig(); err == nil {
		t.Fatal("expected malformed chain config to fail")
	}
}

func TestTaskStatusAndSwarmRoleClassifiers(t *testing.T) {
	for _, status := range []TaskStatus{StatusCompleted, StatusFailed, StatusCancelled} {
		if !IsTerminalStatus(status) {
			t.Fatalf("%s should be terminal", status)
		}
	}
	for _, status := range []TaskStatus{StatusPending, StatusQueued, StatusRunning, StatusBlocked} {
		if IsTerminalStatus(status) {
			t.Fatalf("%s should not be terminal", status)
		}
	}
	for _, role := range []SwarmRole{SwarmRolePlanner, SwarmRoleWorker, SwarmRoleReviewer, SwarmRoleMerger, SwarmRoleLegacyIntegrator} {
		if !IsSwarmChildRole(role) {
			t.Fatalf("%s should be a swarm child role", role)
		}
	}
	for _, role := range []SwarmRole{SwarmRoleNone, SwarmRoleParent, SwarmRole("custom")} {
		if IsSwarmChildRole(role) {
			t.Fatalf("%s should not be a swarm child role", role)
		}
	}
}
