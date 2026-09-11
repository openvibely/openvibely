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

// ProjectAPIItem is the compact project row needed by GET /api/projects. It
// deliberately excludes project settings and repository metadata that the API
// response does not expose.
type ProjectAPIItem struct {
	ID        string
	Name      string
	RepoPath  string
	CreatedAt time.Time
}

// ProjectWorkerCapacity is the identity and configured worker limit needed to
// render Workers capacity rows. It intentionally excludes project metadata
// used only by management, detail, and repository configuration paths.
type ProjectWorkerCapacity struct {
	ID         string
	Name       string
	MaxWorkers *int
}
