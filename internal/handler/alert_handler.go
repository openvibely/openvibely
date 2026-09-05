package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func alertCardListState(projectID string, filter models.AlertListFilter) pages.CardListState {
	read := ""
	if filter.Read != nil {
		if *filter.Read {
			read = "read"
		} else {
			read = "unread"
		}
	}
	state := pages.CardListState{
		ProjectID: projectID,
		Search:    filter.Search,
		Sort:      filter.Sort,
		Filters: map[string]string{
			"read":           read,
			"severity":       string(filter.Severity),
			"decision_state": string(filter.DecisionState),
			"type":           string(filter.Type),
			"source":         filter.Source,
		},
	}
	if filter.ProcessingState != "" {
		state.Preserved = []pages.CardListQueryValue{{Key: "processing_state", Value: string(filter.ProcessingState)}}
	}
	return state
}

func (h *Handler) ListAlerts(c echo.Context) error {
	htmxRequest := isHTMX(c)
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	page := parseCardPageRequest(c)
	filter := alertListFilter(c, page)
	alerts, err := h.alertSvc.ListSummariesPage(ctx, currentProjectID, filter)
	if err != nil {
		applog.Infof("[handler] ListAlerts error: %v", err)
		return err
	}
	alerts, hasMore := cardPageItems(alerts, page.PageSize)
	if alertID := strings.TrimSpace(c.QueryParam("alert_id")); alertID != "" {
		alert, getErr := h.alertSvc.GetByID(ctx, currentProjectID, alertID)
		if getErr != nil {
			return getErr
		}
		if alert == nil {
			return echo.NewHTTPError(http.StatusNotFound, "notification not found")
		}
		alerts = []models.AlertSummary{alertSummaryFromAlert(alert)}
		hasMore = false
	}

	unreadCount, _ := h.alertSvc.CountUnread(ctx, currentProjectID)

	// applog.Debugf("[handler] ListAlerts project=%s count=%d unread=%d htmx=%v", currentProjectID, len(alerts), unreadCount, isHTMX)

	if htmxRequest || page.IsFragment {
		if page.IsFragment {
			setCardPageResponse(c, hasMore)
		}
		return render(c, http.StatusOK, pages.AlertsContentPageWithState(alerts, currentProjectID, unreadCount, hasMore, filter.DecisionState, filter.ProcessingState, alertCardListState(currentProjectID, filter)))
	}
	projects, _ := h.projectSvc.ListSelectorOptions(ctx)
	return render(c, http.StatusOK, pages.AlertsPageWithState(projects, currentProjectID, alerts, unreadCount, hasMore, filter.DecisionState, filter.ProcessingState, alertCardListState(currentProjectID, filter)))
}

func alertListFilter(c echo.Context, page cardPageRequest) models.AlertListFilter {
	source := strings.TrimSpace(c.QueryParam("source"))
	if len(source) > 100 {
		source = ""
	}
	return models.AlertListFilter{
		DecisionState:   parseAlertDecisionState(c.QueryParam("decision_state")),
		ProcessingState: parseAlertProcessingState(c.QueryParam("processing_state")),
		Type:            models.AlertType(allowlistedQuery(c, "type", "", string(models.AlertTaskFailed), string(models.AlertTaskNeedsFollowup), string(models.AlertCustom))),
		Severity:        models.AlertSeverity(allowlistedQuery(c, "severity", "", string(models.SeverityInfo), string(models.SeverityWarning), string(models.SeverityError))),
		Source:          source,
		Read:            parseAlertRead(c.QueryParam("read")),
		Sort:            allowlistedQuery(c, "sort", "newest", "newest", "oldest", "severity", "unread_first"),
		Limit:           page.PageSize + 1,
		Offset:          page.Offset,
		Search:          page.Search,
	}
}

func parseAlertRead(value string) *bool {
	switch strings.TrimSpace(value) {
	case "read":
		read := true
		return &read
	case "unread":
		read := false
		return &read
	default:
		return nil
	}
}

func parseAlertDecisionState(value string) models.AlertDecisionState {
	switch strings.TrimSpace(value) {
	case "", "all":
		return ""
	case string(models.AlertDecisionNotRequired), string(models.AlertDecisionPending), string(models.AlertDecisionApproved), string(models.AlertDecisionRejected), string(models.AlertDecisionDismissed):
		return models.AlertDecisionState(strings.TrimSpace(value))
	default:
		return ""
	}
}

func parseAlertProcessingState(value string) models.AlertProcessingState {
	switch strings.TrimSpace(value) {
	case "", "all":
		return ""
	case string(models.AlertProcessingNotApplicable), string(models.AlertProcessingUnclaimed), string(models.AlertProcessingClaimed), string(models.AlertProcessingImplementationTaskLinked), string(models.AlertProcessingCompleted), string(models.AlertProcessingFailed):
		return models.AlertProcessingState(strings.TrimSpace(value))
	default:
		return ""
	}
}

// GetAlertDetail lazily returns the full body and metadata inspect fragment for
// a single project-scoped notification. The list and mutation refresh paths use
// bounded summaries and never embed body/metadata, so this endpoint is the only
// place the full detail is loaded, on demand, for one opened notification.
func (h *Handler) GetAlertDetail(c echo.Context) error {
	ctx := c.Request().Context()
	currentProjectID, _ := h.getCurrentProjectID(c)
	alert, err := h.alertSvc.GetByID(ctx, currentProjectID, c.Param("id"))
	if err != nil {
		applog.Infof("[handler] GetAlertDetail project=%s alert=%s error=%v", currentProjectID, c.Param("id"), err)
		return echo.NewHTTPError(http.StatusNotFound, "notification not found")
	}
	if alert == nil {
		return echo.NewHTTPError(http.StatusNotFound, "notification not found")
	}
	return render(c, http.StatusOK, pages.AlertDetail(*alert))
}

// alertSummaryFromAlert projects a full alert onto the bounded summary shape the
// list renders. Body and metadata are intentionally dropped.
func alertSummaryFromAlert(a *models.Alert) models.AlertSummary {
	return models.AlertSummary{
		ID:                   a.ID,
		ProjectID:            a.ProjectID,
		Scope:                a.Scope,
		TaskID:               a.TaskID,
		ExecutionID:          a.ExecutionID,
		SourceTaskID:         a.SourceTaskID,
		Type:                 a.Type,
		Severity:             a.Severity,
		Title:                a.Title,
		Message:              a.Message,
		Source:               a.Source,
		IdempotencyKey:       a.IdempotencyKey,
		DecisionState:        a.DecisionState,
		DecidedAt:            a.DecidedAt,
		ProcessingState:      a.ProcessingState,
		Claimant:             a.Claimant,
		ClaimedAt:            a.ClaimedAt,
		ClaimExpiresAt:       a.ClaimExpiresAt,
		ImplementationTaskID: a.ImplementationTaskID,
		ProcessingError:      a.ProcessingError,
		IsRead:               a.IsRead,
		CreatedAt:            a.CreatedAt,
		UpdatedAt:            a.UpdatedAt,
	}
}

func (h *Handler) renderAlertListRefresh(c echo.Context, projectID string, alerts []models.AlertSummary, unreadCountOverride *int) error {
	ctx := c.Request().Context()
	page := parseCardPageRequest(c)
	filter := alertListFilter(c, page)
	hasMore := false
	if alerts == nil {
		var err error
		alerts, err = h.alertSvc.ListSummariesPage(ctx, projectID, filter)
		if err != nil {
			return err
		}
		alerts, hasMore = cardPageItems(alerts, page.PageSize)
		if page.IsFragment {
			setCardPageResponse(c, hasMore)
		}
	}
	unreadCount := 0
	if unreadCountOverride != nil {
		unreadCount = *unreadCountOverride
	} else {
		unreadCount, _ = h.alertSvc.CountUnread(ctx, projectID)
	}
	c.Response().Header().Set("HX-Trigger", "alertUpdate")
	return render(c, http.StatusOK, pages.AlertsContentPageWithState(alerts, projectID, unreadCount, hasMore, filter.DecisionState, filter.ProcessingState, alertCardListState(projectID, filter)))
}

func (h *Handler) setAlertDecision(c echo.Context, state models.AlertDecisionState) error {
	ctx := c.Request().Context()
	projectID, _ := h.getCurrentProjectID(c)
	if err := h.alertSvc.SetDecision(ctx, projectID, c.Param("id"), state); err != nil {
		applog.Infof("[handler] setAlertDecision project=%s alert=%s state=%s error=%v", projectID, c.Param("id"), state, err)
		return echo.NewHTTPError(http.StatusNotFound, "notification not found or no longer pending")
	}
	return h.renderAlertListRefresh(c, projectID, nil, nil)
}

func (h *Handler) ApproveAlert(c echo.Context) error {
	return h.setAlertDecision(c, models.AlertDecisionApproved)
}

func (h *Handler) RejectAlert(c echo.Context) error {
	return h.setAlertDecision(c, models.AlertDecisionRejected)
}

func (h *Handler) DismissAlert(c echo.Context) error {
	return h.setAlertDecision(c, models.AlertDecisionDismissed)
}

func (h *Handler) MarkAlertRead(c echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	if err := h.alertSvc.MarkRead(ctx, currentProjectID, id); err != nil {
		applog.Infof("[handler] MarkAlertRead error: %v", err)
		return err
	}

	applog.Infof("[handler] MarkAlertRead id=%s", id)

	return h.renderAlertListRefresh(c, currentProjectID, nil, nil)
}

func (h *Handler) MarkAlertsReadBulk(c echo.Context) error {
	ids, err := decodeBulkIDs(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if err := h.alertSvc.MarkReadBulk(c.Request().Context(), projectID, ids); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "all selected alerts must belong to the current project")
	}
	return c.JSON(http.StatusOK, map[string]int{"updated": len(ids)})
}

func (h *Handler) MarkAllAlertsRead(c echo.Context) error {
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	if err := h.alertSvc.MarkAllRead(ctx, currentProjectID); err != nil {
		applog.Infof("[handler] MarkAllAlertsRead error: %v", err)
		return err
	}

	applog.Infof("[handler] MarkAllAlertsRead project=%s", currentProjectID)

	zeroUnread := 0
	return h.renderAlertListRefresh(c, currentProjectID, nil, &zeroUnread)
}

func (h *Handler) DeleteAlert(c echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)
	if err := h.alertSvc.Delete(ctx, currentProjectID, id); err != nil {
		applog.Infof("[handler] DeleteAlert error: %v", err)
		return err
	}

	applog.Infof("[handler] DeleteAlert id=%s", id)

	if isHTMX(c) {
		return h.renderAlertListRefresh(c, currentProjectID, nil, nil)
	}
	return c.Redirect(http.StatusSeeOther, "/alerts")
}

func (h *Handler) GetUnreadAlertCount(c echo.Context) error {
	ctx := c.Request().Context()

	projectID, _ := h.getCurrentProjectID(c)

	count, err := h.alertSvc.CountUnread(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] GetUnreadAlertCount error: %v", err)
		return err
	}

	return render(c, http.StatusOK, pages.AlertBadge(count))
}

func (h *Handler) DeleteAlertsBulk(c echo.Context) error {
	ids, err := decodeBulkIDs(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if err := h.alertSvc.DeleteBulk(c.Request().Context(), projectID, ids); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "all selected alerts must belong to the current project")
	}
	return c.JSON(http.StatusOK, map[string]int{"deleted": len(ids)})
}

func (h *Handler) DeleteAllAlerts(c echo.Context) error {
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	if err := h.alertSvc.DeleteAll(ctx, currentProjectID); err != nil {
		applog.Infof("[handler] DeleteAllAlerts error: %v", err)
		return err
	}

	applog.Infof("[handler] DeleteAllAlerts project=%s", currentProjectID)

	zeroUnread := 0
	return h.renderAlertListRefresh(c, currentProjectID, []models.AlertSummary{}, &zeroUnread)
}
