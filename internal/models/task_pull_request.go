package models

import "time"

type TaskPullRequest struct {
	ID               string    `json:"id"`
	TaskID           string    `json:"task_id"`
	PRNumber         int       `json:"pr_number"`
	PRURL            string    `json:"pr_url"`
	PRState          string    `json:"pr_state"`
	PublishedHeadSHA string    `json:"published_head_sha"`
	IssueNumber      *int      `json:"issue_number,omitempty"`
	IssueURL         string    `json:"issue_url"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
