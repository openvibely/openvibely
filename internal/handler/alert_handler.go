package handler

import (
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ListAlerts(c echo.Context) error {
	isHTMX := isHTMX(c)
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	alerts, err := h.alertSvc.ListByProject(ctx, currentProjectID, 100)
	if err != nil {
		log.Printf("[handler] ListAlerts error: %v", err)
		return err
	}

	unreadCount, _ := h.alertSvc.CountUnread(ctx, currentProjectID)

	log.Printf("[handler] ListAlerts project=%s count=%d unread=%d htmx=%v", currentProjectID, len(alerts), unreadCount, isHTMX)

	if isHTMX {
		return render(c, http.StatusOK, pages.AlertsContent(alerts, currentProjectID, unreadCount))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.Alerts(projects, currentProjectID, alerts, unreadCount))
}

func (h *Handler) MarkAlertRead(c echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	if err := h.alertSvc.MarkRead(ctx, id); err != nil {
		log.Printf("[handler] MarkAlertRead error: %v", err)
		return err
	}

	log.Printf("[handler] MarkAlertRead id=%s", id)

	// Return updated alerts list
	currentProjectID, _ := h.getCurrentProjectID(c)

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
		log.Printf("[handler] MarkAllAlertsRead error: %v", err)
		return err
	}

	log.Printf("[handler] MarkAllAlertsRead project=%s", currentProjectID)

	alerts, _ := h.alertSvc.ListByProject(ctx, currentProjectID, 100)

	// Trigger alert badge refresh in sidebar
	c.Response().Header().Set("HX-Trigger", "alertUpdate")

	return render(c, http.StatusOK, pages.AlertsContent(alerts, currentProjectID, 0))
}

func (h *Handler) DeleteAlert(c echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	if err := h.alertSvc.Delete(ctx, id); err != nil {
		log.Printf("[handler] DeleteAlert error: %v", err)
		return err
	}

	log.Printf("[handler] DeleteAlert id=%s", id)

	if isHTMX(c) {
		// Re-render alerts list with updated count
		currentProjectID, _ := h.getCurrentProjectID(c)

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
		log.Printf("[handler] GetUnreadAlertCount error: %v", err)
		return err
	}

	return render(c, http.StatusOK, pages.AlertBadge(count))
}

func (h *Handler) DeleteAllAlerts(c echo.Context) error {
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	if err := h.alertSvc.DeleteAll(ctx, currentProjectID); err != nil {
		log.Printf("[handler] DeleteAllAlerts error: %v", err)
		return err
	}

	log.Printf("[handler] DeleteAllAlerts project=%s", currentProjectID)

	// Trigger alert badge refresh in sidebar
	c.Response().Header().Set("HX-Trigger", "alertUpdate")

	return render(c, http.StatusOK, pages.AlertsContent([]models.Alert{}, currentProjectID, 0))
}
