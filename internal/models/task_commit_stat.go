package models

import "time"

// TaskCommitStat stores summary-only metadata for a commit produced by an OpenVibely task turn.
type TaskCommitStat struct {
	ID               string
	ProjectID        string
	TaskID           string
	ExecutionID      *string
	CommitSHA        string
	ShortSHA         string
	Subject          string
	Author           string
	ProducedAt       time.Time
	Insertions       int
	Deletions        int
	FilesChanged     int
	ChangedFilesJSON string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
