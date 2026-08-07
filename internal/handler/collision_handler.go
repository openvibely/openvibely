package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// --- Impact Analysis ---

// AnalyzeTaskImpact godoc
// @Summary Analyze task impact
// @Description Performs AI-powered semantic analysis of what files, APIs, schemas, and components a task will likely modify
// @Tags collisions
// @Produce json
// @Param taskId path string true "Task ID"
// @Success 200 {object} models.ImpactAnalysis
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/collisions/analyze/{taskId} [post]
func (h *Handler) AnalyzeTaskImpact(c echo.Context) error {
	taskID := c.Param("taskId")
	if taskID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "task ID required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	analysis, err := h.collisionSvc.AnalyzeTaskImpact(c.Request().Context(), taskID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, analysis)
}

// GetTaskImpact godoc
// @Summary Get task impact analysis
// @Description Returns the latest impact analysis for a task
// @Tags collisions
// @Produce json
// @Param taskId path string true "Task ID"
// @Success 200 {object} models.ImpactAnalysis
// @Failure 404 {object} map[string]string
// @Router /api/collisions/impact/{taskId} [get]
func (h *Handler) GetTaskImpact(c echo.Context) error {
	taskID := c.Param("taskId")
	if taskID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "task ID required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	analysis, err := h.collisionSvc.GetImpactAnalysis(c.Request().Context(), taskID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if analysis == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "no impact analysis found for this task"})
	}

	return c.JSON(http.StatusOK, analysis)
}

// --- Conflict Detection ---

// DetectConflicts godoc
// @Summary Detect conflicts between tasks
// @Description Compares impact analyses across pending/running tasks to predict conflicts
// @Tags collisions
// @Produce json
// @Param project_id query string true "Project ID"
// @Success 200 {array} models.ConflictPrediction
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/collisions/detect [post]
func (h *Handler) DetectConflicts(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	conflicts, err := h.collisionSvc.DetectConflicts(c.Request().Context(), projectID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	if conflicts == nil {
		conflicts = []models.ConflictPrediction{}
	}
	return c.JSON(http.StatusOK, conflicts)
}

// GetTaskConflicts godoc
// @Summary Get conflicts for a task
// @Description Returns all active conflicts involving a specific task
// @Tags collisions
// @Produce json
// @Param taskId path string true "Task ID"
// @Success 200 {array} models.ConflictPrediction
// @Router /api/collisions/conflicts/{taskId} [get]
func (h *Handler) GetTaskConflicts(c echo.Context) error {
	taskID := c.Param("taskId")
	if taskID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "task ID required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	conflicts, err := h.collisionSvc.GetConflictsForTask(c.Request().Context(), taskID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	if conflicts == nil {
		conflicts = []models.ConflictPrediction{}
	}
	return c.JSON(http.StatusOK, conflicts)
}

// UpdateConflictStatus godoc
// @Summary Update conflict status
// @Description Updates the status of a conflict prediction (acknowledge, resolve, mark as false positive)
// @Tags collisions
// @Accept json
// @Produce json
// @Param id path string true "Conflict Prediction ID"
// @Param body body object true "Status update" SchemaExample({"status": "resolved"})
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Router /api/collisions/conflicts/{id}/status [patch]
func (h *Handler) UpdateConflictStatus(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "conflict ID required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	// Validate status
	status := models.ConflictStatus(body.Status)
	switch status {
	case models.ConflictAcknowledged, models.ConflictResolved, models.ConflictFalsePositive:
		// Valid
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid status, must be one of: acknowledged, resolved, false_positive"})
	}

	if err := h.collisionSvc.UpdateConflictStatus(c.Request().Context(), id, status); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "updated"})
}

// --- Collision Report ---

// GetCollisionReport godoc
// @Summary Get collision report
// @Description Returns a full collision report for a project including analyses, conflicts, and recommendations
// @Tags collisions
// @Produce json
// @Param project_id query string true "Project ID"
// @Success 200 {object} models.CollisionReport
// @Failure 400 {object} map[string]string
// @Router /api/collisions/report [get]
func (h *Handler) GetCollisionReport(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	report, err := h.collisionSvc.GetCollisionReport(c.Request().Context(), projectID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, report)
}

// --- Execution Order Recommendations ---

// RecommendExecutionOrder godoc
// @Summary Recommend task execution order
// @Description Generates an optimal task execution order recommendation based on detected conflicts
// @Tags collisions
// @Produce json
// @Param project_id query string true "Project ID"
// @Success 200 {object} models.ExecutionOrderRecommendation
// @Failure 400 {object} map[string]string
// @Router /api/collisions/recommend [post]
func (h *Handler) RecommendExecutionOrder(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	rec, err := h.collisionSvc.RecommendExecutionOrder(c.Request().Context(), projectID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if rec == nil {
		return c.JSON(http.StatusOK, map[string]string{"message": "fewer than 2 pending tasks, no recommendation needed"})
	}

	return c.JSON(http.StatusOK, rec)
}

// GetLatestRecommendation godoc
// @Summary Get latest execution order recommendation
// @Description Returns the latest pending recommendation for a project
// @Tags collisions
// @Produce json
// @Param project_id query string true "Project ID"
// @Success 200 {object} models.ExecutionOrderRecommendation
// @Router /api/collisions/recommendation [get]
func (h *Handler) GetLatestRecommendation(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	rec, err := h.collisionSvc.GetLatestRecommendation(c.Request().Context(), projectID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if rec == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"message": "no pending recommendation"})
	}

	return c.JSON(http.StatusOK, rec)
}

// AcceptRecommendation godoc
// @Summary Accept execution order recommendation
// @Description Marks a recommendation as accepted
// @Tags collisions
// @Param id path string true "Recommendation ID"
// @Success 200 {object} map[string]string
// @Router /api/collisions/recommendation/{id}/accept [post]
func (h *Handler) AcceptRecommendation(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "recommendation ID required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	if err := h.collisionSvc.AcceptRecommendation(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "accepted"})
}

// RejectRecommendation godoc
// @Summary Reject execution order recommendation
// @Description Marks a recommendation as rejected
// @Tags collisions
// @Param id path string true "Recommendation ID"
// @Success 200 {object} map[string]string
// @Router /api/collisions/recommendation/{id}/reject [post]
func (h *Handler) RejectRecommendation(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "recommendation ID required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	if err := h.collisionSvc.RejectRecommendation(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "rejected"})
}

// --- Conflict History & Learning ---

// GetConflictHistory godoc
// @Summary Get conflict history
// @Description Returns historical conflict data for a project (for learning)
// @Tags collisions
// @Produce json
// @Param project_id query string true "Project ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/collisions/history [get]
func (h *Handler) GetConflictHistory(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id required"})
	}

	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	history, err := h.collisionSvc.GetConflictHistory(c.Request().Context(), projectID, 50)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	predicted, total, err := h.collisionSvc.GetPredictionAccuracy(c.Request().Context(), projectID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	accuracy := 0.0
	if total > 0 {
		accuracy = float64(predicted) / float64(total) * 100
	}

	if history == nil {
		history = []models.ConflictHistory{}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"history":           history,
		"total_conflicts":   total,
		"predicted_count":   predicted,
		"accuracy_percent":  accuracy,
	})
}

// RecordConflict godoc
// @Summary Record an actual conflict
// @Description Records a conflict that actually occurred for learning and prediction improvement
// @Tags collisions
// @Accept json
// @Produce json
// @Param body body models.ConflictHistory true "Conflict history record"
// @Success 201 {object} models.ConflictHistory
// @Failure 400 {object} map[string]string
// @Router /api/collisions/history [post]
func (h *Handler) RecordConflict(c echo.Context) error {
	if h.collisionSvc == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "collision detection service not available"})
	}

	var history models.ConflictHistory
	if err := c.Bind(&history); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	if history.ProjectID == "" || history.TaskAID == "" || history.TaskBID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id, task_a_id, and task_b_id are required"})
	}

	if err := h.collisionSvc.RecordConflict(c.Request().Context(), &history); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusCreated, history)
}
