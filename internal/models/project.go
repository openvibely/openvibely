package models

import "time"

type Project struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	RepoPath             string    `json:"repo_path"`
	RepoURL              string    `json:"repo_url"`
	IsDefault            bool      `json:"is_default"`
	DefaultAgentConfigID *string   `json:"default_agent_config_id"`
	MaxWorkers           *int      `json:"max_workers"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}
