package handler

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) SetAutomationServices(graph *service.AutomationGraphService, registration *service.AutomationRegistrationService) {
	h.automationGraphSvc = graph
	h.automationRegistrationSvc = registration
	h.setChannelAutomationGraphService(graph)
}

func (h *Handler) SetAutomationExternalStateService(external *service.AutomationExternalStateService) {
	h.automationExternalStateSvc = external
}

func (h *Handler) SetAutomationLiveViewTracker(tracker *service.AutomationLiveViewTracker) {
	h.automationLiveViewTracker = tracker
}

type automationGraphServiceSetter interface {
	SetAutomationGraphService(*service.AutomationGraphService)
}

type automationTemplateUpdateServiceSetter interface {
	SetAutomationTemplateUpdateServices(*service.AutomationDraftService, *service.AutomationCompiler)
}

func (h *Handler) setChannelAutomationGraphService(graph *service.AutomationGraphService) {
	if h == nil {
		return
	}
	if h.telegramService != nil {
		h.telegramService.SetAutomationGraphService(graph)
	}
	if setter, ok := h.slackSvc.(automationGraphServiceSetter); ok {
		setter.SetAutomationGraphService(graph)
	}
	if setter, ok := h.discordSvc.(automationGraphServiceSetter); ok {
		setter.SetAutomationGraphService(graph)
	}
}

func (h *Handler) SetAutomationBuilderServices(drafts *service.AutomationDraftService, capabilities *service.AutomationCapabilitySnapshotBuilder, validator *service.AutomationSaveValidator, compiler *service.AutomationCompiler, confirmation *service.AutomationConfirmationService, lifecycle *service.AutomationLifecycleService) {
	h.automationDraftSvc = drafts
	h.automationCapabilitySvc = capabilities
	h.automationSaveValidator = validator
	h.automationCompiler = compiler
	h.automationConfirmationSvc = confirmation
	h.automationLifecycleSvc = lifecycle
	if drafts != nil {
		drafts.SetCapabilitySnapshotBuilder(capabilities)
	}
	if h.agentRepo != nil {
		if validator != nil {
			validator.SetAgentRepository(h.agentRepo)
		}
		if compiler != nil {
			compiler.SetAgentRepository(h.agentRepo)
		}
	}
	if h.telegramService != nil {
		h.telegramService.SetAutomationTemplateUpdateServices(drafts, compiler)
	}
	if setter, ok := h.slackSvc.(automationTemplateUpdateServiceSetter); ok {
		setter.SetAutomationTemplateUpdateServices(drafts, compiler)
	}
	if setter, ok := h.discordSvc.(automationTemplateUpdateServiceSetter); ok {
		setter.SetAutomationTemplateUpdateServices(drafts, compiler)
	}
}

func automationCardListFilter(c echo.Context, page cardPageRequest) repository.AutomationCardListFilter {
	return repository.AutomationCardListFilter{
		Search:         page.Search,
		LifecycleState: allowlistedQuery(c, "lifecycle_state", "", "active", "paused", "draft", "archived"),
		HealthState:    allowlistedQuery(c, "health_state", "", "unknown", "healthy", "degraded", "unhealthy"),
		AutomationType: allowlistedQuery(c, "automation_type", "", "custom", "native_sdlc", "github_sdlc", "vision_driver", "scheduled"),
		Adapter:        allowlistedQuery(c, "adapter", "", "custom", "native_sdlc", "github_sdlc", "vision_driver"),
		Sort:           allowlistedQuery(c, "sort", "updated_desc", "updated_desc", "updated_asc", "name_asc", "name_desc"),
	}
}

func automationCardListState(projectID string, filter repository.AutomationCardListFilter) pages.CardListState {
	return pages.CardListState{
		ProjectID: projectID,
		Search:    filter.Search,
		Sort:      filter.Sort,
		Filters: map[string]string{
			"lifecycle_state": filter.LifecycleState,
			"health_state":    filter.HealthState,
			"automation_type": filter.AutomationType,
			"adapter":         filter.Adapter,
		},
	}
}

func (h *Handler) ListAutomations(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	page := parseCardPageRequest(c)
	filter := automationCardListFilter(c, page)
	listState := automationCardListState(projectID, filter)
	cards, err := h.automationGraphSvc.ListPageFiltered(ctx, projectID, page.PageSize+1, page.Offset, filter)
	if err != nil {
		return err
	}
	cards, hasMore := cardPageItems(cards, page.PageSize)
	if page.IsFragment || isHTMX(c) {
		setCardPageResponse(c, hasMore)
		return render(c, http.StatusOK, pages.AutomationsContentPageWithState(cards, projectID, hasMore, listState))
	}
	projects, _ := h.projectSvc.ListSelectorOptions(ctx)
	return render(c, http.StatusOK, pages.AutomationsPageWithState(projects, projectID, cards, hasMore, listState))
}

func (h *Handler) GetAutomationLive(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	graph, err := h.automationGraphSvc.GetLive(ctx, projectID, c.Param("automationId"), time.Now())
	if err != nil {
		return err
	}
	if graph == nil {
		return echo.NewHTTPError(http.StatusNotFound, "automation not found")
	}
	h.automationLiveViewTracker.MarkViewed(projectID, graph.Automation.ID)
	deleteAvailable := true
	currentTemplateRevision := service.CurrentAutomationTemplateRevision(graph.Version.AdapterKey)
	graph.TemplateUpdateAvailable = currentTemplateRevision > 0 &&
		(graph.Automation.TemplateRevision == nil || *graph.Automation.TemplateRevision < currentTemplateRevision)
	if h.automationDraftSvc != nil {
		current, currentErr := h.automationDraftSvc.LoadLiveCandidate(ctx, projectID, graph)
		if currentErr != nil {
			return currentErr
		}
		graph.YAML, currentErr = service.EncodeAutomationDraftYAML(current.Candidate)
		if currentErr != nil {
			return currentErr
		}
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationLiveContent(*graph, projectID, deleteAvailable))
	}
	projects, _ := h.projectSvc.ListSelectorOptions(ctx)
	return render(c, http.StatusOK, pages.AutomationLive(projects, projectID, *graph, deleteAvailable))
}

func (h *Handler) RefreshAutomationExternalState(c echo.Context) error {
	if h.automationExternalStateSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation external refresh unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if _, err := h.automationExternalStateSvc.Refresh(c.Request().Context(), projectID, c.Param("automationId"), time.Now().UTC()); err != nil {
		if err.Error() == "automation not found" {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}
	c.Response().Header().Set("HX-Trigger", "automationExternalRefreshed")
	return h.GetAutomationLive(c)
}
