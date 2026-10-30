package models

// TaskModelSettings is a credential-free projection of the selected configuration
// and the task's overrides. Empty overrides retain the configuration defaults.
type TaskModelSettings struct {
	Config      LLMConfig
	Effort      string
	ServiceTier string
}
