package handler

import (
	"context"
	"errors"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"net/http"
)

func ownInsightProject(h *Handler, ctx context.Context, projectID string) (string, authz.Permission, error) {
	if h.insightsSvc == nil {
		return "", "", statusErr(http.StatusBadRequest, "insights service not available")
	}
	return projectID, authz.Edit, nil
}

var actInsightsRunAnalysis = defineAction("insights.analyze", ownInsightProject, func(h *Handler, ctx context.Context, projectID string) (*models.InsightReport, error) {
	return h.insightsSvc.RunAnalysis(ctx, projectID)
})
var actInsightsRunHealthCheck = defineAction("insights.health", ownInsightProject, func(h *Handler, ctx context.Context, projectID string) (*models.HealthCheck, error) {
	return h.insightsSvc.RunHealthCheck(ctx, projectID)
})
var actInsightsGradeIdeas = defineAction("insights.grade", ownInsightProject, func(h *Handler, ctx context.Context, projectID string) (*models.IdeaGrade, error) {
	return h.insightsSvc.GradeIdeas(ctx, projectID)
})

type insightMutationArgs struct {
	ProjectID, InsightID, Operation string
	Status                          models.InsightStatus
}

func (h *Handler) mutateInsight(ctx context.Context, a insightMutationArgs) error {
	_, err := actInsightMutation(h, ctx, a)
	return err
}

var actInsightMutation = defineAction("insights.mutate", func(h *Handler, ctx context.Context, a insightMutationArgs) (string, authz.Permission, error) {
	return ownInsightProject(h, ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a insightMutationArgs) (struct{}, error) {
	var err error
	switch a.Operation {
	case "status":
		err = h.insightsSvc.UpdateInsightStatus(ctx, a.ProjectID, a.InsightID, a.Status)
	case "delete":
		err = h.insightsSvc.DeleteInsight(ctx, a.ProjectID, a.InsightID)
	default:
		return struct{}{}, statusErr(http.StatusBadRequest, "unknown insight action")
	}
	if errors.Is(err, service.ErrInsightNotFound) {
		return struct{}{}, statusErr(http.StatusNotFound, err.Error())
	}
	return struct{}{}, err
})

func isActionNotFound(err error) bool {
	var status *echo.HTTPError
	return errors.As(err, &status) && status.Code == http.StatusNotFound
}

type activitySummaryArgs struct {
	ProjectID  string
	Reflection bool
	Range      models.TimeRange
}

var actActivitySummary = defineAction("activity.summary", func(h *Handler, ctx context.Context, a activitySummaryArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a activitySummaryArgs) (string, error) {
	if h.upcomingSvc == nil {
		return "", statusErr(http.StatusServiceUnavailable, "activity service unavailable")
	}
	if a.Reflection {
		history, err := h.upcomingSvc.GenerateHistory(ctx, a.ProjectID, a.Range)
		if err != nil {
			return "", err
		}
		return h.upcomingSvc.GenerateReflectionSummary(ctx, a.ProjectID, history)
	}
	upcoming, err := h.upcomingSvc.GenerateUpcoming(ctx, a.ProjectID)
	if err != nil {
		return "", err
	}
	return h.upcomingSvc.GeneratePulseSummary(ctx, a.ProjectID, upcoming)
})
