package models

import "time"

// GitHubAuthorizedActor represents a GitHub user on the optional authorization allowlist.
type GitHubAuthorizedActor struct {
	ReviewerIdentity *GitHubReviewerIdentity `json:"reviewer_identity,omitempty"`
	ID               string                  `json:"id"`
	GitHubUserID     *int64                  `json:"github_user_id,omitempty"`
	GitHubLogin      string                  `json:"github_login"`
	DisplayName      string                  `json:"display_name"`
	Permission       string                  `json:"permission"`
	AddedAt          time.Time               `json:"added_at"`
	AddedBy          string                  `json:"added_by"`
}

// GitHubProjectInbox stores the project-scoped GitHub authorized user whose assigned issues scheduled tasks should inspect.
type GitHubProjectInbox struct {
	ProjectID    string    `json:"project_id"`
	GitHubUserID *int64    `json:"github_user_id,omitempty"`
	GitHubLogin  string    `json:"github_login"`
	AgentID      *string   `json:"agent_id,omitempty"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GitHubReviewerIdentity is an explicit, project-scoped administrator attestation.
type GitHubReviewerIdentity struct {
	ProjectID         string `json:"project_id"`
	ActorID           string `json:"actor_id"`
	Host              string `json:"host"`
	ExternalUserID    int64  `json:"external_user_id"`
	ApplicationUserID string `json:"application_user_id"`
	VerifiedBy        string `json:"verified_by"`
}
