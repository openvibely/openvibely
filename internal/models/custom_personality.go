package models

import "time"

// CustomPersonality represents a user-defined chat personality stored in the database.
type CustomPersonality struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Key          string    `json:"key"`
	Description  string    `json:"description"`
	SystemPrompt string    `json:"system_prompt"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
