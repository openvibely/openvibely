package handler

import (
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ViewUpcoming(c echo.Context) error {
	isHTMX := isHTMX(c)
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	upcoming, err := h.upcomingSvc.GenerateUpcoming(ctx, currentProjectID)
	if err != nil {
		log.Printf("[handler] ViewUpcoming error: %v", err)
		return err
	}

	log.Printf("[handler] ViewUpcoming project=%s running=%d pending=%d scheduled=%d htmx=%v",
		currentProjectID, len(upcoming.RunningTasks), len(upcoming.PendingTasks), len(upcoming.ScheduledTasks), isHTMX)

	if isHTMX {
		return render(c, http.StatusOK, pages.UpcomingContent(upcoming, currentProjectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.UpcomingPage(projects, currentProjectID, upcoming))
}

func (h *Handler) ViewHistory(c echo.Context) error {
	isHTMX := isHTMX(c)
	ctx := c.Request().Context()

	currentProjectID, _ := h.getCurrentProjectID(c)

	timeRange := models.TimeRange(c.QueryParam("range"))
	if timeRange == "" {
		timeRange = models.TimeRangeDay
	}

	history, err := h.upcomingSvc.GenerateHistory(ctx, currentProjectID, timeRange)
	if err != nil {
		log.Printf("[handler] ViewHistory error: %v", err)
		return err
	}

	log.Printf("[handler] ViewHistory project=%s range=%s executions=%d htmx=%v",
		currentProjectID, timeRange, history.Summary.TotalExecutions, isHTMX)

	if isHTMX {
		return render(c, http.StatusOK, pages.HistoryContent(history, currentProjectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.HistoryPage(projects, currentProjectID, history))
}

// GeneratePulseSummary generates an AI summary for the Pulse page
func (h *Handler) GeneratePulseSummary(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	upcoming, err := h.upcomingSvc.GenerateUpcoming(ctx, projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.AISummaryResult("", err))
	}

	summary, err := h.upcomingSvc.GeneratePulseSummary(ctx, projectID, upcoming)
	if err != nil {
		return render(c, http.StatusOK, pages.AISummaryResult("", err))
	}

	upcoming.AISummary = summary
	return render(c, http.StatusOK, pages.AISummaryResult(summary, nil))
}

// GenerateReflectionSummary generates an AI summary for the Reflection page
func (h *Handler) GenerateReflectionSummary(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	timeRange := models.TimeRange(c.QueryParam("range"))
	if timeRange == "" {
		timeRange = models.TimeRangeDay
	}

	history, err := h.upcomingSvc.GenerateHistory(ctx, projectID, timeRange)
	if err != nil {
		return render(c, http.StatusOK, pages.AISummaryResult("", err))
	}

	summary, err := h.upcomingSvc.GenerateReflectionSummary(ctx, projectID, history)
	if err != nil {
		return render(c, http.StatusOK, pages.AISummaryResult("", err))
	}

	return render(c, http.StatusOK, pages.AISummaryResult(summary, nil))
}
