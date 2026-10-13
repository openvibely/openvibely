package persistence

type TaskRunPeriod struct {
	AverageRunMs    float64 `json:"average_run_ms"`
	DurationSamples int     `json:"duration_samples"`
	Period          string  `json:"period"`
	Runs            int     `json:"runs"`
	Completed       int     `json:"completed"`
	Failed          int     `json:"failed"`
	Cancelled       int     `json:"cancelled"`
}

type TaskRunModel struct {
	ModelConfigID   string          `json:"model_config_id"`
	ConfigName      string          `json:"config_name"`
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	ReasoningEffort string          `json:"reasoning_effort"`
	Runs            int             `json:"runs"`
	AverageRunMs    float64         `json:"average_run_ms"`
	DurationSamples int             `json:"duration_samples"`
	Trend           []TaskRunPeriod `json:"trend"`
}

type TaskRunActivity struct {
	Hours  [24]int        `json:"hours"`
	Models []TaskRunModel `json:"models"`
}
