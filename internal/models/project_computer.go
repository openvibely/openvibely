package models

// ProjectComputer records application ownership; credentials never enter this row.
type ComputerResources struct {
	CPUs      int `json:"cpus"`
	MemoryGiB int `json:"memory_gib"`
	PidsLimit int `json:"pids_limit"`
}

func DefaultComputerResources() ComputerResources {
	return ComputerResources{CPUs: 2, MemoryGiB: 4, PidsLimit: 4096}
}

type ProjectComputer struct {
	PreviewQuality      string            `json:"preview_quality"`
	Resources           ComputerResources `json:"resources"`
	TaskEnabled         bool              `json:"task_enabled"`
	ProjectEnabled      bool              `json:"project_enabled"`
	CleanupRequired     bool              `json:"-"`
	IdleMinutes         int               `json:"idle_minutes"`
	TimeZone            string            `json:"time_zone"`
	Enabled             bool              `json:"enabled"`
	HandoffReason       string            `json:"handoff_reason,omitempty"`
	HandoffExpectedText string            `json:"handoff_expected_text,omitempty"`
	HandoffAlertID      string            `json:"handoff_alert_id,omitempty"`
	ToolTarget          string            `json:"tool_target"`
	HandoffKind         string            `json:"handoff_kind,omitempty"`
	HandoffURL          string            `json:"handoff_url,omitempty"`
	ProjectID           string            `json:"project_id"`
	Provider            string            `json:"provider"`
	Held                bool              `json:"held"`
	Owner               string            `json:"owner,omitempty"`
	LeaseUntil          int64             `json:"lease_until,omitempty"`
}

// ComputerSettings is the project-wide configuration edited in Settings.
type ComputerSettings struct {
	PreviewQuality string
	Resources      ComputerResources
	Enabled        bool
	IdleMinutes    int
	TimeZone       string
}
