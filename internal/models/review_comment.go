package models

import "time"

// ReviewComment represents an inline code review comment on a diff line.
type ReviewComment struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	FilePath    string    `json:"file_path"`
	LineNumber  int       `json:"line_number"`
	LineType    string    `json:"line_type"` // "new", "old", or "ctx"
	CommentText string   `json:"comment_text"`
	ReviewedBy  string    `json:"reviewed_by"`
	CreatedAt   time.Time `json:"created_at"`
}
