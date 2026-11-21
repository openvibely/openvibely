package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/computerrecording"
	"github.com/openvibely/openvibely/internal/models"
)

// RecordingCommand uses the existing authenticated, task-scoped helper API.
func (s *ProjectComputerService) RecordingCommand(ctx context.Context, projectID, action, id string, limit int) (computerrecording.Result, error) {
	conn, err := s.connection(ctx, projectID, true)
	if err != nil {
		return computerrecording.Result{}, err
	}
	body, _ := json.Marshal(map[string]any{"action": action, "id": id, "limit": limit})
	data, err := s.request(ctx, conn, "POST", "/recording", body)
	if err != nil {
		return computerrecording.Result{}, err
	}
	var out computerrecording.Result
	err = json.Unmarshal(data, &out)
	return out, err
}

// RecordingImageBudget reserves room for existing context and tools. The normal
// provider admission/compaction path still validates the actual dispatch request.
func RecordingImageBudget(agent models.LLMConfig, transcript string, currentTokens ...int) (int, error) {
	budget := requestBudgetForAgent(agent)
	available := budget.SafeInputLimit/2 - estimatedUTF8Tokens(transcript) - estimateDefaultProviderToolTokens(agent.Provider) - 1024
	if len(currentTokens) > 0 && currentTokens[0] > 0 {
		available = min(available, budget.SafeInputLimit-currentTokens[0]-estimatedUTF8Tokens(transcript)-1024)
	}
	count := min(computerrecording.MaxImages, available/2100)
	if count < 2 {
		return 0, fmt.Errorf("demonstration does not fit this model; choose a larger-context model and retry Save")
	}
	return count, nil
}
