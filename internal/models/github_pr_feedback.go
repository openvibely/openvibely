package models

import "time"

type GitHubPRFeedbackForwarded struct {
	ID                  string    `json:"id"`
	TaskPullRequestID   string    `json:"task_pull_request_id"`
	TaskID              string    `json:"task_id"`
	RepoFullName        string    `json:"repo_full_name"`
	PRNumber            int       `json:"pr_number"`
	FeedbackKind        string    `json:"feedback_kind"`
	GitHubID            string    `json:"github_id"`
	GitHubNodeID        string    `json:"github_node_id"`
	AuthorUserID        int64     `json:"author_user_id"`
	ProviderHost        string    `json:"provider_host"`
	AuthorLogin         string    `json:"author_login"`
	HTMLURL             string    `json:"html_url"`
	Body                string    `json:"body"`
	CreatedAt           time.Time `json:"created_at"`
	ForwardedAt         time.Time `json:"forwarded_at"`
	QueuedThreadInputID string    `json:"queued_thread_input_id"`
}
