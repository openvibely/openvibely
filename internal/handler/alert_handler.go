package handler

import (
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

	alerts, err := h.alertSvc.ListByProject(ctx, currentProjectID, 100)
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
		alerts = []models.Alert{*alert}
	}

	unreadCount, _ := h.alertSvc.CountUnread(ctx, currentProjectID)

	// applog.Debugf("[handler] ListAlerts project=%s count=%d unread=%d htmx=%v", currentProjectID, len(alerts), unreadCount, isHTMX)

	if isHTMX {
		return render(c, http.StatusOK, pages.AlertsContent(alerts, currentProjectID, unreadCount))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.Alerts(projects, currentProjectID, alerts, unreadCount))
}

func (h *Handler) setAlertDecision(c echo.Context, state models.AlertDecisionState) error {
	ctx := c.Request().Context()
	projectID, _ := h.getCurrentProjectID(c)
	if err := h.alertSvc.SetDecision(ctx, projectID, c.Param("id"), state); err != nil {
		applog.Infof("[handler] setAlertDecision project=%s alert=%s state=%s error=%v", projectID, c.Param("id"), state, err)
		return echo.NewHTTPError(http.StatusNotFound, "notification not found or no longer pending")
	}
	alerts, err := h.alertSvc.ListByProject(ctx, projectID, 100)
	if err != nil {
		return err
	}
	unreadCount, _ := h.alertSvc.CountUnread(ctx, projectID)
	c.Response().Header().Set("HX-Trigger", "alertUpdate")
	return render(c, http.StatusOK, pages.AlertsContent(alerts, projectID, unreadCount))
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

	// Return updated alerts list
	alerts, _ := h.alertSvc.ListByProject(ctx, currentProjectID, 100)
	unreadCount, _ := h.alertSvc.CountUnread(ctx, currentProjectID)

	// Trigger alert badge refresh in sidebar
	c.Response().Header().Set("HX-Trigger", "alertUpdate")

	return render(c, http.StatusOK, pages.AlertsContent(alerts, currentProjectID, unreadCount))
}

func (h *Handler) MarkAllAlertsRead(c echo.Context) error {
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	if err := h.alertSvc.MarkAllRead(ctx, currentProjectID); err != nil {
		applog.Infof("[handler] MarkAllAlertsRead error: %v", err)
		return err
	}

	applog.Infof("[handler] MarkAllAlertsRead project=%s", currentProjectID)

	alerts, _ := h.alertSvc.ListByProject(ctx, currentProjectID, 100)

	// Trigger alert badge refresh in sidebar
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
		// Re-render alerts list with updated count
		alerts, _ := h.alertSvc.ListByProject(ctx, currentProjectID, 100)
		unreadCount, _ := h.alertSvc.CountUnread(ctx, currentProjectID)

		// Trigger alert badge refresh in sidebar
		c.Response().Header().Set("HX-Trigger", "alertUpdate")

		return render(c, http.StatusOK, pages.AlertsContent(alerts, currentProjectID, unreadCount))
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

	// Trigger alert badge refresh in sidebar
	c.Response().Header().Set("HX-Trigger", "alertUpdate")

	return render(c, http.StatusOK, pages.AlertsContent([]models.Alert{}, currentProjectID, 0))
}
