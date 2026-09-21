package models

import (
	"testing"
)

func TestTask_ParseChainConfig(t *testing.T) {
	tests := []struct {
		name        string
		chainConfig string
		wantEnabled bool
		wantTrigger string
		wantErr     bool
	}{
		{
			name:        "empty config",
			chainConfig: "{}",
			wantEnabled: false,
			wantTrigger: "",
			wantErr:     false,
		},
		{
			name:        "enabled config",
			chainConfig: `{"enabled":true,"trigger":"on_completion","child_agent_id":"agent1","child_model":"sonnet","child_category":"active"}`,
			wantEnabled: true,
			wantTrigger: "on_completion",
			wantErr:     false,
		},
		{
			name:        "disabled config",
			chainConfig: `{"enabled":false,"trigger":"on_completion"}`,
			wantEnabled: false,
			wantTrigger: "on_completion",
			wantErr:     false,
		},
		{
			name:        "invalid JSON",
			chainConfig: `{invalid}`,
			wantEnabled: false,
			wantTrigger: "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{
				ChainConfig: tt.chainConfig,
			}
			config, err := task.ParseChainConfig()
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseChainConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if config.Enabled != tt.wantEnabled {
				t.Errorf("ParseChainConfig() Enabled = %v, want %v", config.Enabled, tt.wantEnabled)
			}
			if config.Trigger != tt.wantTrigger {
				t.Errorf("ParseChainConfig() Trigger = %v, want %v", config.Trigger, tt.wantTrigger)
			}
		})
	}
}

func TestTask_SetChainConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *ChainConfiguration
		wantErr bool
	}{
		{
			name: "enabled config",
			config: &ChainConfiguration{
				Enabled:       true,
				Trigger:       "on_completion",
				ChildAgentID:  "agent1",
				ChildModel:    "sonnet",
				ChildCategory: "active",
			},
			wantErr: false,
		},
		{
			name: "disabled config",
			config: &ChainConfiguration{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name:    "nil config",
			config:  nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{}
			if err := task.SetChainConfig(tt.config); (err != nil) != tt.wantErr {
				t.Errorf("SetChainConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.config != nil && tt.config.Enabled {
				// Verify we can parse it back
				parsed, err := task.ParseChainConfig()
				if err != nil {
					t.Errorf("SetChainConfig() failed to parse back: %v", err)
				}
				if parsed.Enabled != tt.config.Enabled {
					t.Errorf("SetChainConfig() round-trip failed: Enabled = %v, want %v", parsed.Enabled, tt.config.Enabled)
				}
				if parsed.Trigger != tt.config.Trigger {
					t.Errorf("SetChainConfig() round-trip failed: Trigger = %v, want %v", parsed.Trigger, tt.config.Trigger)
				}
			}
		})
	}
}

func TestTaskCancellableByUser(t *testing.T) {
	tests := []struct {
		name string
		task *Task
		want bool
	}{
		{name: "nil", task: nil, want: false},
		{name: "running", task: &Task{Status: StatusRunning, Category: CategoryActive}, want: true},
		{name: "queued", task: &Task{Status: StatusQueued, Category: CategoryActive}, want: true},
		{name: "pending active", task: &Task{Status: StatusPending, Category: CategoryActive}, want: true},
		{name: "blocked swarm parent active", task: &Task{Status: StatusBlocked, Category: CategoryActive, SwarmRole: SwarmRoleParent}, want: true},
		{name: "completed", task: &Task{Status: StatusCompleted, Category: CategoryCompleted}, want: false},
		{name: "failed", task: &Task{Status: StatusFailed, Category: CategoryBacklog}, want: false},
		{name: "cancelled", task: &Task{Status: StatusCancelled, Category: CategoryBacklog}, want: false},
		{name: "pending backlog", task: &Task{Status: StatusPending, Category: CategoryBacklog}, want: false},
		{name: "pending scheduled", task: &Task{Status: StatusPending, Category: CategoryScheduled}, want: false},
		{name: "blocked without swarm parent", task: &Task{Status: StatusBlocked, Category: CategoryActive}, want: false},
		{name: "blocked swarm parent backlog", task: &Task{Status: StatusBlocked, Category: CategoryBacklog, SwarmRole: SwarmRoleParent}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.CancellableByUser(); got != tt.want {
				t.Errorf("CancellableByUser() = %v, want %v", got, tt.want)
			}
		})
	}
}
