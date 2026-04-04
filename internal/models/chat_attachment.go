package models

import "time"

type ChatAttachment struct {
	ID          string    `json:"id"`
	ExecutionID string    `json:"execution_id"`
	FileName    string    `json:"file_name"`
	FilePath    string    `json:"file_path"`
	MediaType   string    `json:"media_type"`
	FileSize    int64     `json:"file_size"`
	CreatedAt   time.Time `json:"created_at"`
}
