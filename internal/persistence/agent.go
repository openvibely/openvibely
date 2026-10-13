package persistence

import (
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"time"
)

var (
	ErrAgentNameRequired                = errors.New("agent name is required")
	ErrSelectableAgentNameAlreadyExists = errors.New("enabled selectable primary agent name already exists")
)

// AgentRuntimeSummary is the compact projection needed by runtime list_agents.
type AgentRuntimeSummary struct {
	Name           string
	Description    string
	Model          string
	SkillCount     int
	MCPServerCount int
}

// AgentPickerOption is the compact projection needed by settings pickers that
// render only an agent identifier and display name.
type AgentPickerOption struct {
	ID   string
	Name string
}

// AgentScheduleOption is the compact projection needed by the Schedule primary
// Agent selector. Availability is enforced by ListScheduleOptions; this type
// contains only the display fields rendered by the selector.
type AgentScheduleOption struct {
	ID    string
	Name  string
	Model string
}

// AgentTaskUIOption is the compact Agent projection shared by Task board badges
// and Task create/edit selectors. It preserves scalar availability state without
// hydrating private prompt, tool, plugin, MCP, skill, or default configuration.
type AgentTaskUIOption struct {
	ID                  string
	Name                string
	Model               string
	Scope               models.AgentScope
	ProjectID           string
	SelectableAsPrimary bool
	Enabled             bool
	GeneratedStatus     models.AgentGeneratedStatus
	ArchivedAt          *time.Time
}

// AgentSelectableReference is the compact identity and project-scope projection
// used when validating Automation Agent references.
type AgentSelectableReference struct {
	ID        string
	Key       string
	ProjectID string
}

// AgentSkillCatalogRef is the compact projection needed to discover agent-owned
// skill catalogs without hydrating prompt/config/plugin/skill JSON fields.
type AgentSkillCatalogRef struct {
	ID        string
	Key       string
	ProjectID string
}

// AgentListSummary is the compact projection needed by the lifecycle agent_list
// inspector. It excludes full prompt, model, tool, plugin, MCP, permission,
// model-default, source-reference, and timestamp fields. Skill names are
// extracted in SQL so skill descriptions and content are never decoded here.
type AgentListSummary struct {
	Key                 string
	Name                string
	Description         string
	Scope               models.AgentScope
	SelectableAsPrimary bool
	Enabled             bool
	GeneratedStatus     models.AgentGeneratedStatus
	SystemKind          string
	ArchivedAt          *time.Time
	AttachedSkillNames  []string
}

// ListPage returns one bounded Models-page-compatible agent projection. The
// Agents card currently carries its edit metadata, so this method retains the
// existing row shape while bounding both the result count and response HTML.
type AgentPageFilter struct {
	ProjectID string
	Search    string
	Enabled   *bool
	Scope     string
	Origin    string
	Sort      string
}
