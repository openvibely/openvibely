package persistence

import (
	"time"
)

type SkillAnalyticsFilter struct {
	DateFrom            time.Time
	DateTo              time.Time
	ProjectID           string
	AgentID             string
	WorkflowID          string
	Surface             string
	SkillScope          string
	EventType           string
	Limit               int
	GroupBy             string
	EvidencePeriod      string
	EvidenceEvent       string
	EvidenceAgentID     string
	EvidenceSkillHandle string
	EvidenceSkillScope  string
	EvidenceLimit       int
}

type EnabledSkillInfo struct {
	Handle    string
	Scope     string
	Enabled   bool
	AlwaysUse bool
}
