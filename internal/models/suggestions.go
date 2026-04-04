package models

// SuggestionsDashboardData combines Proactive Insights and Backlog Suggestions
// into a single unified view.
type SuggestionsDashboardData struct {
	Insights *InsightDashboardData `json:"insights,omitempty"`
	Backlog  *BacklogDashboardData `json:"backlog,omitempty"`
}
