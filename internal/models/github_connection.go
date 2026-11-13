package models

// GitHubConnection belongs to one OV user. Tokens must never be serialized into
// API responses or logs; callers expose connection metadata separately.
type GitHubConnection struct {
	PAT         string `json:"-"`
	Login       string `json:"login"`
	APIEndpoint string `json:"api_endpoint"`
	AuthorName  string `json:"author_name"`
	AuthorEmail string `json:"author_email"`
	Revision    int64  `json:"revision"`
}
