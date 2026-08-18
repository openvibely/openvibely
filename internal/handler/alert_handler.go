package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ListAlerts(c echo.Context) error {
	isHTMX := isHTMX(c)
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	alerts, err := h.alertSvc.ListSummariesByProject(ctx, currentProjectID, 100)
	if err != nil {
		applog.Infof("[handler] ListAlerts error: %v", err)
		return err
	}
	if alertID := strings.TrimSpace(c.QueryParam("alert_id")); alertID != "" {
		alert, getErr := h.alertSvc.GetByID(ctx, currentProjectID, alertID)
		if getErr != nil {
			return getErr
		}
		if alert == nil {
			return echo.NewHTTPError(http.StatusNotFound, "notification not found")
		}
		alerts = []models.AlertSummary{alertSummaryFromAlert(alert)}
	}

	unreadCount, _ := h.alertSvc.CountUnread(ctx, currentProjectID)

	// applog.Debugf("[handler] ListAlerts project=%s count=%d unread=%d htmx=%v", currentProjectID, len(alerts), unreadCount, isHTMX)

	if isHTMX {
		return render(c, http.StatusOK, pages.AlertsContent(alerts, currentProjectID, unreadCount))
	}
	projects, _ := h.projectSvc.ListSelectorOptions(ctx)
	return render(c, http.StatusOK, pages.Alerts(projects, currentProjectID, alerts, unreadCount))
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

func (h *Handler) renderAlertsRefresh(c echo.Context, ctx context.Context, projectID string) error {
	alerts, err := h.alertSvc.ListSummariesByProject(ctx, projectID, 100)
	if err != nil {
		return err
	}
	unreadCount, _ := h.alertSvc.CountUnread(ctx, projectID)
	c.Response().Header().Set("HX-Trigger", "alertUpdate")
	return render(c, http.StatusOK, pages.AlertsContent(alerts, projectID, unreadCount))
}

func (h *Handler) setAlertDecision(c echo.Context, state models.AlertDecisionState) error {
	ctx := c.Request().Context()
	projectID, _ := h.getCurrentProjectID(c)
	if err := h.alertSvc.SetDecision(ctx, projectID, c.Param("id"), state); err != nil {
		applog.Infof("[handler] setAlertDecision project=%s alert=%s state=%s error=%v", projectID, c.Param("id"), state, err)
		return echo.NewHTTPError(http.StatusNotFound, "notification not found or no longer pending")
	}
	return h.renderAlertsRefresh(c, ctx, projectID)
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

	return h.renderAlertsRefresh(c, ctx, currentProjectID)
}

func (h *Handler) MarkAllAlertsRead(c echo.Context) error {
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	if err := h.alertSvc.MarkAllRead(ctx, currentProjectID); err != nil {
		applog.Infof("[handler] MarkAllAlertsRead error: %v", err)
		return err
	}

	applog.Infof("[handler] MarkAllAlertsRead project=%s", currentProjectID)

	// MarkAllRead guarantees the project unread count is zero, so keep the
	// existing shortcut instead of re-counting while sharing the trigger/fragment
	// contract with the other refresh paths.
	alerts, _ := h.alertSvc.ListSummariesByProject(ctx, currentProjectID, 100)
	c.Response().Header().Set("HX-Trigger", "alertUpdate")
	return render(c, http.StatusOK, pages.AlertsContent(alerts, currentProjectID, 0))
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
		return h.renderAlertsRefresh(c, ctx, currentProjectID)
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

func (h *Handler) DeleteAllAlerts(c echo.Context) error {
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	if err := h.alertSvc.DeleteAll(ctx, currentProjectID); err != nil {
		applog.Infof("[handler] DeleteAllAlerts error: %v", err)
		return err
	}

	applog.Infof("[handler] DeleteAllAlerts project=%s", currentProjectID)

	// DeleteAll removes every project alert, so keep the existing empty-list and
	// zero-unread shortcut instead of re-querying after the destructive mutation.
	c.Response().Header().Set("HX-Trigger", "alertUpdate")
	return render(c, http.StatusOK, pages.AlertsContent([]models.AlertSummary{}, currentProjectID, 0))
}
