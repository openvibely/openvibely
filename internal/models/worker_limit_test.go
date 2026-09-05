package models

import (
	"strings"
	"testing"
)

func TestValidateGlobalWorkerLimit(t *testing.T) {
	for _, tt := range []struct {
		name       string
		maxWorkers int
		wantErr    bool
	}{
		{name: "unlimited", maxWorkers: 0},
		{name: "finite above legacy cap", maxWorkers: 25},
		{name: "negative", maxWorkers: -1, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGlobalWorkerLimit(tt.maxWorkers)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateGlobalWorkerLimit(%d) error = %v, wantErr=%v", tt.maxWorkers, err, tt.wantErr)
			}
		})
	}
}

func TestValidateProjectWorkerLimit(t *testing.T) {
	finiteGlobal := 25
	unlimitedGlobal := 0
	zero := 0
	one := 1
	atGlobal := 25
	underGlobal := 20
	aboveGlobal := 26
	high := 100
	negative := -1
	negativeGlobal := -1

	for _, tt := range []struct {
		name             string
		maxWorkers       *int
		globalMaxWorkers int
		wantErrContains  string
	}{
		{name: "nil inherits", maxWorkers: nil, globalMaxWorkers: finiteGlobal},
		{name: "zero clears", maxWorkers: &zero, globalMaxWorkers: finiteGlobal},
		{name: "minimum positive", maxWorkers: &one, globalMaxWorkers: finiteGlobal},
		{name: "equal to finite global", maxWorkers: &atGlobal, globalMaxWorkers: finiteGlobal},
		{name: "above ten under finite global", maxWorkers: &underGlobal, globalMaxWorkers: finiteGlobal},
		{name: "above finite global", maxWorkers: &aboveGlobal, globalMaxWorkers: finiteGlobal, wantErrContains: "exceeds global worker limit"},
		{name: "unlimited global permits high project cap", maxWorkers: &high, globalMaxWorkers: unlimitedGlobal},
		{name: "negative project value", maxWorkers: &negative, globalMaxWorkers: finiteGlobal, wantErrContains: "non-negative"},
		{name: "negative global value", maxWorkers: &one, globalMaxWorkers: negativeGlobal, wantErrContains: "global max_workers must be non-negative"}} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectWorkerLimit(tt.maxWorkers, tt.globalMaxWorkers)
			if tt.wantErrContains == "" {
				if err != nil {
					t.Fatalf("ValidateProjectWorkerLimit() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("ValidateProjectWorkerLimit() error = %v, want substring %q", err, tt.wantErrContains)
			}
		})
	}
}
