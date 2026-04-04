package models

import (
	"encoding/json"
	"time"
)

// AutonomousConfig stores per-project autonomous build settings
type AutonomousConfig struct {
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	Enabled           bool      `json:"enabled"`
	MaxExecutionHours int       `json:"max_execution_hours"`
	ProtectedFiles    string    `json:"protected_files"`
	ExcludedAreas     string    `json:"excluded_areas"`
	ScheduleHour      int       `json:"schedule_hour"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ParseProtectedFiles returns the list of protected file patterns
func (c *AutonomousConfig) ParseProtectedFiles() ([]string, error) {
	if c.ProtectedFiles == "" || c.ProtectedFiles == "[]" {
		return nil, nil
	}
	var files []string
	if err := json.Unmarshal([]byte(c.ProtectedFiles), &files); err != nil {
		return nil, err
	}
	return files, nil
}

// ParseExcludedAreas returns the list of excluded areas
func (c *AutonomousConfig) ParseExcludedAreas() ([]string, error) {
	if c.ExcludedAreas == "" || c.ExcludedAreas == "[]" {
		return nil, nil
	}
	var areas []string
	if err := json.Unmarshal([]byte(c.ExcludedAreas), &areas); err != nil {
		return nil, err
	}
	return areas, nil
}
