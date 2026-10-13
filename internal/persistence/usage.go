package persistence

import (
	"time"
)

type UsageFilter struct {
	AccountGroupKey  string
	ProjectID        string
	Provider         string
	AccountID        string
	AgentID          string
	WorkflowID       string
	DateFrom         time.Time
	DateTo           time.Time
	GroupBy          string
	Refresh          bool
	EvidencePeriod   string
	EvidenceProvider string
	EvidenceModel    string
	EvidenceLimit    int
}
