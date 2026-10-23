package skilldeps

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
)

var ErrApprovalRequired = errors.New("skill dependencies need installation; waiting for your answer")

// SkillIdentity is display metadata from the authorized skill catalog, not consent.
type SkillIdentity struct {
	ProjectID string `json:"project_id"`
	Source    string `json:"source"`
	Skill     string `json:"skill"`
	Agent     string `json:"agent,omitempty"`
}

// InstallationRequest describes only packages which need installation now.
// The answer applies to this pending tool call, not future uses of the skill.
type InstallationRequest struct {
	Identity     SkillIdentity `json:"identity"`
	Platform     string        `json:"platform"`
	Dependencies []Dependency  `json:"dependencies"`
}

type InstallationPrompt func(context.Context, InstallationRequest) error

// ResolveWithInstallPrompt uses installed executables normally. Only a missing
// or damaged installation asks for a human answer before downloading anything.
func (m *Manager) ResolveWithInstallPrompt(ctx context.Context, identity SkillIdentity, deps []Dependency, prompt InstallationPrompt) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Validate(deps); err != nil {
		return nil, err
	}
	// Snapshot the declarations so the question and subsequent install agree.
	raw, err := json.Marshal(deps)
	if err != nil {
		return nil, err
	}
	var snapshot []Dependency
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	manager := *m
	if manager.Platform == "" {
		manager.Platform = runtime.GOOS + "/" + runtime.GOARCH
	}
	results := make([]Result, len(snapshot))
	missing := make([]Dependency, 0)
	indexes := make([]int, 0)
	for i, d := range snapshot {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Explicit local executables never cause a download, including when unhealthy.
		override := ""
		if manager.LookupEnv != nil {
			override = manager.LookupEnv(OverrideVariable(d.Name))
		}
		if override != "" {
			local := manager
			local.LookupEnv = func(string) string { return override }
			resolved, err := local.Resolve(ctx, []Dependency{d})
			if err != nil {
				return nil, err
			}
			results[i] = resolved[0]
			continue
		}
		p, supported := d.Packages[manager.Platform]
		if !supported || manager.CacheDir == "" {
			local := manager
			local.LookupEnv = nil
			resolved, err := local.Resolve(ctx, []Dependency{d})
			if err != nil {
				return nil, err
			}
			results[i] = resolved[0]
			continue
		}
		root, err := manager.cacheRoot(d, p, manager.Platform)
		if err != nil {
			return nil, err
		}
		executable := filepath.Join(root, filepath.FromSlash(p.Executable))
		if cachedHealth(ctx, root, executable, d.HealthCheck) == nil {
			results[i] = Result{Name: d.Name, Version: d.Version, EnvironmentVariable: d.EnvironmentVariable, OverrideVariable: OverrideVariable(d.Name), Status: "cached", Executable: executable}
			manager.notify(d.Name, "ready")
			continue
		}
		missing = append(missing, d)
		indexes = append(indexes, i)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(missing) == 0 {
		return results, nil
	}
	if prompt == nil {
		return nil, ErrApprovalRequired
	}
	offered, _ := json.Marshal(InstallationRequest{Identity: identity, Platform: manager.Platform, Dependencies: missing})
	var question InstallationRequest
	if err := json.Unmarshal(offered, &question); err != nil {
		return nil, err
	}
	if err := prompt(ctx, question); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// These packages were offered as downloads, not mutable local overrides.
	manager.LookupEnv = nil
	resolved, err := manager.Resolve(ctx, missing)
	if err != nil {
		return nil, err
	}
	for i, result := range resolved {
		results[indexes[i]] = result
	}
	return results, nil
}
