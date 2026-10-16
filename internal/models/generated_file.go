package models

import "time"

// GeneratedFile is an output, never implicitly included as a model attachment.
type GeneratedFile struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	ExecutionID string    `json:"execution_id"`
	FileName    string    `json:"file_name"`
	FilePath    string    `json:"-"`
	MediaType   string    `json:"media_type"`
	FileSize    int64     `json:"file_size"`
	Display     string    `json:"display"`
	CreatedAt   time.Time `json:"created_at"`
}
