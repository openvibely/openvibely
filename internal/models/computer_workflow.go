package models

// ComputerWorkflow is a reusable, reviewed set of desktop instructions.
type ComputerWorkflow struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}
