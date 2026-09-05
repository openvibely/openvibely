package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestModelAndAgentBulkDeleteEligibilityIsAtomic(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	modelsRepo := NewLLMConfigRepo(db)
	first := &models.LLMConfig{Name: "Default bulk model", Provider: models.ProviderTest, Model: "default-bulk", IsDefault: true}
	second := &models.LLMConfig{Name: "Other bulk model", Provider: models.ProviderTest, Model: "other-bulk"}
	require.NoError(t, modelsRepo.Create(ctx, first))
	require.NoError(t, modelsRepo.Create(ctx, second))
	require.ErrorContains(t, modelsRepo.DeleteBulk(ctx, []string{first.ID, second.ID}), "default")
	configs, err := modelsRepo.GetByIDs(ctx, []string{first.ID, second.ID})
	require.NoError(t, err)
	require.Len(t, configs, 2)

	agentsRepo := NewAgentRepo(db)
	protected := &models.Agent{Name: "Protected bulk", Model: "inherit", GeneratedStatus: models.AgentStatusProtected}
	ordinary := &models.Agent{Name: "Ordinary bulk", Model: "inherit"}
	require.NoError(t, agentsRepo.Create(ctx, protected))
	require.NoError(t, agentsRepo.Create(ctx, ordinary))
	require.ErrorContains(t, agentsRepo.DeleteBulk(ctx, []string{protected.ID, ordinary.ID}), "protected")
	agents, err := agentsRepo.GetByIDs(ctx, []string{protected.ID, ordinary.ID})
	require.NoError(t, err)
	require.Len(t, agents, 2)
}

func TestScopedBulkDeletesPreflightWithoutPartialDeletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	first := &models.Project{Name: "Bulk first"}
	second := &models.Project{Name: "Bulk second"}
	require.NoError(t, projectRepo.Create(ctx, first))
	require.NoError(t, projectRepo.Create(ctx, second))
	alerts := NewAlertRepo(db)
	own := &models.Alert{ProjectID: first.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "own"}
	foreign := &models.Alert{ProjectID: second.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "foreign"}
	require.NoError(t, alerts.Create(ctx, own))
	require.NoError(t, alerts.Create(ctx, foreign))
	require.Error(t, alerts.DeleteBulk(ctx, first.ID, []string{own.ID, foreign.ID}))
	remaining, err := alerts.ListByProject(ctx, first.ID, 10)
	require.NoError(t, err)
	require.Len(t, remaining, 1)

	webhooks := NewWebhookRepo(db)
	ownHook := &models.WebhookEndpoint{ProjectID: first.ID, Name: "own", Enabled: true}
	foreignHook := &models.WebhookEndpoint{ProjectID: second.ID, Name: "foreign", Enabled: true}
	require.NoError(t, webhooks.Create(ctx, ownHook))
	require.NoError(t, webhooks.Create(ctx, foreignHook))
	require.Error(t, webhooks.DeleteBulk(ctx, first.ID, []string{ownHook.ID, foreignHook.ID}))
	hooks, err := webhooks.ListByProject(ctx, first.ID)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
}

func TestCollectionFiltersAndSortsApplyBeforePagination(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	modelsRepo := NewLLMConfigRepo(db)
	for _, item := range []struct {
		name     string
		provider models.LLMProvider
	}{
		{"Zulu Anthropic", models.ProviderAnthropic}, {"Alpha OpenAI", models.ProviderOpenAI}, {"Beta Anthropic", models.ProviderAnthropic},
	} {
		require.NoError(t, modelsRepo.Create(ctx, &models.LLMConfig{Name: item.name, Provider: item.provider, Model: strings.ToLower(strings.ReplaceAll(item.name, " ", "-"))}))
	}
	page, err := modelsRepo.ListCardsPageFiltered(ctx, 1, 1, ModelCardListFilter{Provider: string(models.ProviderAnthropic), Sort: "name_asc"})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "Zulu Anthropic", page[0].Name)

	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Filtered hooks"}
	require.NoError(t, projectRepo.Create(ctx, project))
	webhookRepo := NewWebhookRepo(db)
	for _, endpoint := range []*models.WebhookEndpoint{{ProjectID: project.ID, Name: "A disabled", Enabled: false}, {ProjectID: project.ID, Name: "B enabled", Enabled: true}, {ProjectID: project.ID, Name: "C enabled", Enabled: true}} {
		require.NoError(t, webhookRepo.Create(ctx, endpoint))
	}
	enabled := true
	hooks, err := webhookRepo.ListCardsByProjectPageFiltered(ctx, project.ID, 1, 1, WebhookCardFilter{Enabled: &enabled})
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.Equal(t, "C enabled", hooks[0].Name)
}

func TestLLMConfigRepoListCardsPageBoundsSearchAndCompactProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		config := &models.LLMConfig{
			Name:          fmt.Sprintf("Page Model %02d", i),
			Provider:      models.ProviderTest,
			Model:         fmt.Sprintf("page-model-%02d", i),
			APIKey:        "card-secret",
			ExtraBodyJSON: strings.Repeat("large-edit-only-payload", 20),
		}
		require.NoError(t, repo.Create(ctx, config))
	}

	first, err := repo.ListCardsPage(ctx, 3, 0, "page model")
	require.NoError(t, err)
	require.Len(t, first, 3)
	require.Equal(t, "Page Model 00", first[0].Name)
	require.Equal(t, "Page Model 02", first[2].Name)
	require.Equal(t, "present", first[0].APIKey)
	require.Empty(t, first[0].ExtraBodyJSON)

	second, err := repo.ListCardsPage(ctx, 3, 3, "page model")
	require.NoError(t, err)
	require.Len(t, second, 2)
	require.Equal(t, "Page Model 03", second[0].Name)
	require.Equal(t, "Page Model 04", second[1].Name)

	filtered, err := repo.ListCardsPage(ctx, 3, 0, "MODEL 04")
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.Equal(t, "Page Model 04", filtered[0].Name)
}

func TestAgentRepoListPageFiltersArchivedAndSearchesBeforeOffset(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		agent := &models.Agent{
			Name:                fmt.Sprintf("Paged Agent %02d", i),
			Description:         "card description",
			SystemPrompt:        "bounded prompt",
			Model:               "inherit",
			SelectableAsPrimary: true,
			Enabled:             true,
		}
		require.NoError(t, repo.Create(ctx, agent))
	}
	archived := &models.Agent{Name: "Paged Agent Archived", Model: "inherit", GeneratedStatus: models.AgentStatusArchived}
	require.NoError(t, repo.Create(ctx, archived))

	first, err := repo.ListPage(ctx, 3, 0, "paged agent")
	require.NoError(t, err)
	require.Len(t, first, 3)
	require.Equal(t, "Paged Agent 00", first[0].Name)
	require.Equal(t, "Paged Agent 02", first[2].Name)

	second, err := repo.ListPage(ctx, 3, 3, "paged agent")
	require.NoError(t, err)
	require.Len(t, second, 2)
	require.Equal(t, "Paged Agent 03", second[0].Name)
	require.Equal(t, "Paged Agent 04", second[1].Name)

	for _, agent := range append(first, second...) {
		require.NotEqual(t, models.AgentStatusArchived, agent.GeneratedStatus)
	}
}

func TestWebhookRepoListCardsByProjectPageIsolatedAndSearchable(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	repo := NewWebhookRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Webhook page project"}
	other := &models.Project{Name: "Other webhook page project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, other))

	for i := 0; i < 3; i++ {
		endpoint := &models.WebhookEndpoint{ProjectID: project.ID, Name: fmt.Sprintf("Paged Hook %02d", i), Enabled: true}
		require.NoError(t, repo.Create(ctx, endpoint))
	}
	otherEndpoint := &models.WebhookEndpoint{ProjectID: other.ID, Name: "Paged Hook Other Project", Enabled: true}
	require.NoError(t, repo.Create(ctx, otherEndpoint))

	first, err := repo.ListCardsByProjectPage(ctx, project.ID, 2, 0, "paged hook")
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, "Paged Hook 00", first[0].Name)

	second, err := repo.ListCardsByProjectPage(ctx, project.ID, 2, 2, "paged hook")
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, "Paged Hook 02", second[0].Name)

	otherPage, err := repo.ListCardsByProjectPage(ctx, project.ID, 20, 0, "other project")
	require.NoError(t, err)
	require.Empty(t, otherPage)
	labelSearch, err := repo.ListCardsByProjectPage(ctx, project.ID, 20, 0, "webhook")
	require.NoError(t, err)
	require.Len(t, labelSearch, 3)
}

func TestCustomPersonalityRepoListPageUsesPromptPreviewAndSearchesPageSet(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewCustomPersonalityRepo(db)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		personality := &models.CustomPersonality{
			Name:         fmt.Sprintf("Paged Personality %02d", i),
			Key:          fmt.Sprintf("paged_personality_%02d", i),
			Description:  "custom card",
			SystemPrompt: strings.Repeat("prompt body ", 30),
		}
		require.NoError(t, repo.Create(ctx, personality))
	}

	first, err := repo.ListPage(ctx, 3, 0, "paged personality")
	require.NoError(t, err)
	require.Len(t, first, 3)
	require.Equal(t, "Paged Personality 00", first[0].Name)
	require.Len(t, first[0].SystemPromptPreview, 150)
	require.Empty(t, first[0].SystemPrompt)

	second, err := repo.ListPage(ctx, 3, 3, "paged personality")
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, "Paged Personality 03", second[0].Name)
}

func TestAlertRepoListFilteredSummariesPageSearchesWithinProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	repo := NewAlertRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Alert page project"}
	other := &models.Project{Name: "Other alert page project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, other))

	for i := 0; i < 4; i++ {
		alert := &models.Alert{
			ProjectID: project.ID,
			Type:      models.AlertCustom,
			Severity:  models.SeverityWarning,
			Title:     fmt.Sprintf("Paged notification %02d", i),
			Message:   "visible summary",
			Source:    "pagination-test",
		}
		require.NoError(t, repo.Create(ctx, alert))
	}
	otherAlert := &models.Alert{
		ProjectID: other.ID,
		Type:      models.AlertCustom,
		Severity:  models.SeverityError,
		Title:     "Paged notification other project",
		Message:   "foreign summary",
	}
	require.NoError(t, repo.Create(ctx, otherAlert))

	first, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{Limit: 3, Offset: 0, Search: "PAGED NOTIFICATION"})
	require.NoError(t, err)
	require.Len(t, first, 3)
	bySource, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{Limit: 20, Search: "pagination-test"})
	require.NoError(t, err)
	require.Len(t, bySource, 4)
	formattedCreatedAt := first[0].CreatedAt.Local().Format("Jan 2, 2006 3:04 PM")
	byFormattedDate, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{Limit: 20, Search: formattedCreatedAt})
	require.NoError(t, err)
	require.NotEmpty(t, byFormattedDate)
	second, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{Limit: 3, Offset: 3, Search: "paged notification"})
	require.NoError(t, err)
	require.Len(t, second, 1)

	foreign, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{Limit: 20, Search: "foreign summary"})
	require.NoError(t, err)
	require.Empty(t, foreign)
}

func TestAlertRepoListFilteredSummariesSupportsWorkflowFiltersAndPagination(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	repo := NewAlertRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Alert workflow filter project"}
	other := &models.Project{Name: "Other alert workflow project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, other))

	implementationTask := &models.Task{
		ProjectID: project.ID, Title: "Alert implementation", Prompt: "Implement the alert.",
		Category: models.CategoryBacklog, Priority: 2, Status: models.StatusPending,
	}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, implementationTask))
	implementationTaskID := implementationTask.ID
	alerts := []struct {
		title      string
		decision   models.AlertDecisionState
		processing models.AlertProcessingState
		message    string
	}{
		{"Pending unclaimed", models.AlertDecisionPending, models.AlertProcessingUnclaimed, "pending work"},
		{"Approved failed", models.AlertDecisionApproved, models.AlertProcessingFailed, "search intersection"},
		{"Approved claimed", models.AlertDecisionApproved, models.AlertProcessingClaimed, "claimed work"},
		{"Rejected completed", models.AlertDecisionRejected, models.AlertProcessingCompleted, "completed work"},
		{"Dismissed linked", models.AlertDecisionDismissed, models.AlertProcessingImplementationTaskLinked, "linked work"},
		{"Operational not applicable", models.AlertDecisionNotRequired, models.AlertProcessingNotApplicable, "operational work"},
	}
	for _, tc := range alerts {
		alert := &models.Alert{
			ProjectID:       project.ID,
			Type:            models.AlertCustom,
			Severity:        models.SeverityInfo,
			Title:           tc.title,
			Message:         tc.message,
			DecisionState:   tc.decision,
			ProcessingState: tc.processing,
		}
		if tc.processing == models.AlertProcessingImplementationTaskLinked {
			alert.ImplementationTaskID = &implementationTaskID
		}
		require.NoError(t, repo.Create(ctx, alert))
	}
	foreign := &models.Alert{
		ProjectID:       other.ID,
		Type:            models.AlertCustom,
		Severity:        models.SeverityInfo,
		Title:           "Approved failed foreign",
		Message:         "search intersection",
		DecisionState:   models.AlertDecisionApproved,
		ProcessingState: models.AlertProcessingFailed,
	}
	require.NoError(t, repo.Create(ctx, foreign))

	pending, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{
		DecisionState: models.AlertDecisionPending, Limit: 20,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "Pending unclaimed", pending[0].Title)

	failed, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{
		ProcessingState: models.AlertProcessingFailed, Limit: 20,
	})
	require.NoError(t, err)
	require.Len(t, failed, 1)
	require.Equal(t, "Approved failed", failed[0].Title)

	intersection, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{
		DecisionState: models.AlertDecisionApproved, ProcessingState: models.AlertProcessingFailed,
		Search: "SEARCH INTERSECTION", Limit: 20,
	})
	require.NoError(t, err)
	require.Len(t, intersection, 1)
	require.Equal(t, "Approved failed", intersection[0].Title)

	operational, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{
		DecisionState: models.AlertDecisionNotRequired, ProcessingState: models.AlertProcessingNotApplicable, Limit: 20,
	})
	require.NoError(t, err)
	require.Len(t, operational, 1)
	require.Equal(t, "Operational not applicable", operational[0].Title)

	allApproved, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{
		DecisionState: models.AlertDecisionApproved, Limit: 1, Offset: 0,
	})
	require.NoError(t, err)
	require.Len(t, allApproved, 1)
	secondApproved, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{
		DecisionState: models.AlertDecisionApproved, Limit: 1, Offset: 1,
	})
	require.NoError(t, err)
	require.Len(t, secondApproved, 1)
	require.NotEqual(t, allApproved[0].ID, secondApproved[0].ID)

	foreignSearch, err := repo.ListFilteredSummaries(ctx, project.ID, models.AlertListFilter{
		DecisionState: models.AlertDecisionApproved, ProcessingState: models.AlertProcessingFailed,
		Search: "foreign", Limit: 20,
	})
	require.NoError(t, err)
	require.Empty(t, foreignSearch)
}
