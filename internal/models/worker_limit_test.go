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

func TestProjectWorkerLimitPolicy(t *testing.T) {
	ptr := func(v int) *int { return &v }

	for _, tt := range []struct {
		name string
		a    *int
		b    *int
		want bool
	}{
		{name: "nil equals zero", a: nil, b: ptr(0), want: true},
		{name: "zero equals nil", a: ptr(0), b: nil, want: true},
		{name: "same positive", a: ptr(3), b: ptr(3), want: true},
		{name: "different positive", a: ptr(2), b: ptr(3), want: false},
		{name: "positive differs from inherited", a: ptr(1), b: nil, want: false},
	} {
		t.Run("equal/"+tt.name, func(t *testing.T) {
			if got := ProjectWorkerLimitsEqual(tt.a, tt.b); got != tt.want {
				t.Fatalf("ProjectWorkerLimitsEqual() = %v, want %v", got, tt.want)
			}
		})
	}

	for _, tt := range []struct {
		name string
		old  *int
		next *int
		want bool
	}{
		{name: "lower finite cap", old: ptr(4), next: ptr(2), want: false},
		{name: "raise finite cap", old: ptr(2), next: ptr(4), want: true},
		{name: "clear finite cap", old: ptr(2), next: nil, want: true},
		{name: "unchanged finite cap", old: ptr(2), next: ptr(2), want: false},
		{name: "unchanged inherited nil to zero", old: nil, next: ptr(0), want: false},
		{name: "initially unlimited to finite", old: nil, next: ptr(3), want: false},
	} {
		t.Run("increase/"+tt.name, func(t *testing.T) {
			if got := ProjectWorkerLimitIncrease(tt.old, tt.next); got != tt.want {
				t.Fatalf("ProjectWorkerLimitIncrease() = %v, want %v", got, tt.want)
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
