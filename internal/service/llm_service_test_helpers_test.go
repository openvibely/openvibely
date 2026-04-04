package service

import (
	"context"

	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// ensureDefaultAgent creates a default agent if one doesn't exist and returns
// a copy with ProviderTest so it routes to the mock caller in tests.
func ensureDefaultAgent(t *testing.T, repo *repository.LLMConfigRepo) *models.LLMConfig {
	t.Helper()
	ctx := context.Background()
	agent, _ := repo.GetDefault(ctx)
	if agent != nil {
		a := *agent
		a.Provider = models.ProviderTest
		return &a
	}
	a := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderAnthropic,
		Model: "test-model", MaxTokens: 4096, IsDefault: true,
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create default agent: %v", err)
	}
	a.Provider = models.ProviderTest
	return a
}
