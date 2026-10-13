package persistence

import (
	"time"
)

// AnalyticsDashboardFilter applies one authoritative project and a half-open
// [DateFrom, DateTo) window to every period-sensitive dashboard query.
type AnalyticsDashboardFilter struct {
	ProjectID            string
	DateFrom             time.Time
	DateTo               time.Time
	Compare              bool
	Limit                int
	EvidenceOffset       int
	GroupBy              string
	View                 string
	AgentID              string
	WorkflowID           string
	WorkType             string
	EvidenceSkillHandle  string
	EvidenceSkillScope   string
	EvidenceSkillAgentID string
}
