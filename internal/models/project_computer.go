package models

// ProjectComputer records application ownership; credentials never enter this row.
type ProjectComputer struct {
	IdleMinutes         int    `json:"idle_minutes"`
	TakeoverPending     bool   `json:"takeover_pending"`
	TimeZone            string `json:"time_zone"`
	Enabled             bool   `json:"enabled"`
	HandoffReason       string `json:"handoff_reason,omitempty"`
	HandoffExpectedText string `json:"handoff_expected_text,omitempty"`
	HandoffAlertID      string `json:"handoff_alert_id,omitempty"`
	ToolTarget          string `json:"tool_target"`
	HandoffKind         string `json:"handoff_kind,omitempty"`
	HandoffURL          string `json:"handoff_url,omitempty"`
	ProjectID           string `json:"project_id"`
	Provider            string `json:"provider"`
	Held                bool   `json:"held"`
	Owner               string `json:"owner,omitempty"`
	LeaseUntil          int64  `json:"lease_until,omitempty"`
}
