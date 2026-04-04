package models

import "time"

type Attachment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	FileName  string    `json:"file_name"`
	FilePath  string    `json:"file_path"`
	MediaType string    `json:"media_type"`
	FileSize  int64     `json:"file_size"`
	CreatedAt time.Time `json:"created_at"`
}
