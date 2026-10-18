package models

import "time"

type Project struct {
	SkillMemoryPermissions *SkillMemoryPermissions `json:"skill_memory_permissions,omitempty"`
	ID                     string                  `json:"id"`
	Name                   string                  `json:"name"`
	Description            string                  `json:"description"`
	RepoPath               string                  `json:"repo_path"`
	RepoURL                string                  `json:"repo_url"`
	IsDefault              bool                    `json:"is_default"`
	DefaultAgentConfigID   *string                 `json:"default_agent_config_id"`
	MaxWorkers             *int                    `json:"max_workers"`
	CreatedAt              time.Time               `json:"created_at"`
	UpdatedAt              time.Time               `json:"updated_at"`
}

// ProjectIdentity is the compact project row needed by channel current-project
// responses. It intentionally excludes project metadata and settings used by
// full project detail and management paths.
type ProjectIdentity struct {
	ID   string
	Name string
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

type RepoPathHealthStatus string

const (
	RepoPathHealthNone    RepoPathHealthStatus = "none"
	RepoPathHealthHealthy RepoPathHealthStatus = "healthy"
	RepoPathHealthMissing RepoPathHealthStatus = "missing"
	RepoPathHealthUnknown RepoPathHealthStatus = "unknown"
)

type ProjectRepoPathHealth struct {
	Status   RepoPathHealthStatus
	Path     string
	Message  string
	Guidance string
}

// SkillMemoryPermissions controls managed-library access independently of other tools.
type SkillMemoryPermissions struct {
	AllowGlobalSkillWrites bool `json:"allow_global_skill_writes"`
	ReadSkills             bool `json:"read_skills"`
	WriteSkills            bool `json:"write_skills"`
	ReadMemories           bool `json:"read_memories"`
	WriteMemories          bool `json:"write_memories"`
}

func (p *Project) ManagedPermissions() SkillMemoryPermissions {
	if p == nil {
		return SkillMemoryPermissions{}
	}
	if p.SkillMemoryPermissions == nil {
		return SkillMemoryPermissions{ReadSkills: true, WriteSkills: true, ReadMemories: true, WriteMemories: true}
	}
	return *p.SkillMemoryPermissions
}
