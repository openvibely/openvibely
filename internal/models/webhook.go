package models

import "time"

// TaskOriginWebhook identifies tasks created by inbound webhooks.
const TaskOriginWebhook = "webhook"

// WebhookEndpoint represents a configured inbound webhook for a project.
type WebhookEndpoint struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	Name               string    `json:"name"`
	Enabled            bool      `json:"enabled"`
	PathToken          string    `json:"path_token"`
	Secret             string    `json:"secret"`
	SystemInstructions string    `json:"system_instructions"`
	TitleTemplate      string    `json:"title_template"`
	PromptTemplate     string    `json:"prompt_template"`
	DefaultPriority    int       `json:"default_priority"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// WebhookEndpointAgent links an agent to a webhook endpoint with ordering.
type WebhookEndpointAgent struct {
	WebhookEndpointID string `json:"webhook_endpoint_id"`
	AgentDefinitionID string `json:"agent_definition_id"`
	Position          int    `json:"position"`
}

// TaskAgentAssignment stores a multi-agent assignment on a task for future execution.
type TaskAgentAssignment struct {
	TaskID            string `json:"task_id"`
	AgentDefinitionID string `json:"agent_definition_id"`
	Position          int    `json:"position"`
}
