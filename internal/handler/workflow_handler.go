package handler

import (
	"fmt"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// ListWorkflows shows the workflows page
func (h *Handler) ListWorkflows(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		projectID = "default"
	}

	workflows, err := h.workflowSvc.ListWorkflows(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] ListWorkflows error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to list workflows")
	}

	templates, err := h.workflowSvc.ListTemplates(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ListWorkflows templates error: %v", err)
		templates = nil
	}

	// Get all agents for display
	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ListWorkflows agents error: %v", err)
		agents = nil
	}

	data := map[string]interface{}{
		"ProjectID": projectID,
		"Workflows": workflows,
		"Templates": templates,
		"Agents":    agents,
	}

	if isHTMX(c) {
		return c.JSON(http.StatusOK, data)
	}
	return c.JSON(http.StatusOK, data)
}

// CreateWorkflow creates a new workflow
func (h *Handler) CreateWorkflow(c echo.Context) error {
	projectID := c.FormValue("project_id")
	if projectID == "" {
		projectID = "default"
	}

	name := c.FormValue("name")
	if name == "" {
		return c.String(http.StatusBadRequest, "Name is required")
	}

	templateID := c.FormValue("template_id")

	var workflow *models.Workflow
	var err error

	if templateID != "" {
		// Create from template
		workflow, err = h.workflowSvc.CreateWorkflowFromTemplate(c.Request().Context(), projectID, templateID, name)
		if err != nil {
			log.Printf("[handler] CreateWorkflow from template error: %v", err)
			return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to create workflow: %v", err))
		}
	} else {
		// Create empty workflow
		strategy := models.WorkflowStrategy(c.FormValue("strategy"))
		if strategy == "" {
			strategy = models.StrategySequential
		}

		workflow = &models.Workflow{
			ProjectID:   projectID,
			Name:        name,
			Description: c.FormValue("description"),
			Strategy:    strategy,
			Config:      "{}",
		}
		if err := h.workflowSvc.CreateWorkflow(c.Request().Context(), workflow); err != nil {
			log.Printf("[handler] CreateWorkflow error: %v", err)
			return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to create workflow: %v", err))
		}
	}

	return c.JSON(http.StatusOK, workflow)
}

// GetWorkflow returns workflow details
func (h *Handler) GetWorkflow(c echo.Context) error {
	id := c.Param("id")

	workflow, err := h.workflowSvc.GetWorkflow(c.Request().Context(), id)
	if err != nil {
		return c.String(http.StatusNotFound, "Workflow not found")
	}

	steps, err := h.workflowSvc.ListSteps(c.Request().Context(), id)
	if err != nil {
		log.Printf("[handler] GetWorkflow steps error: %v", err)
		steps = nil
	}

	executions, err := h.workflowSvc.ListWorkflowExecutions(c.Request().Context(), id)
	if err != nil {
		log.Printf("[handler] GetWorkflow executions error: %v", err)
		executions = nil
	}

	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] GetWorkflow agents error: %v", err)
		agents = nil
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"Workflow":   workflow,
		"Steps":      steps,
		"Executions": executions,
		"Agents":     agents,
	})
}

// UpdateWorkflow updates a workflow
func (h *Handler) UpdateWorkflow(c echo.Context) error {
	id := c.Param("id")

	workflow, err := h.workflowSvc.GetWorkflow(c.Request().Context(), id)
	if err != nil {
		return c.String(http.StatusNotFound, "Workflow not found")
	}

	if name := c.FormValue("name"); name != "" {
		workflow.Name = name
	}
	if desc := c.FormValue("description"); desc != "" {
		workflow.Description = desc
	}
	if strategy := c.FormValue("strategy"); strategy != "" {
		workflow.Strategy = models.WorkflowStrategy(strategy)
	}

	if err := h.workflowSvc.UpdateWorkflow(c.Request().Context(), workflow); err != nil {
		log.Printf("[handler] UpdateWorkflow error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to update workflow")
	}

	return c.JSON(http.StatusOK, workflow)
}

// DeleteWorkflow deletes a workflow
func (h *Handler) DeleteWorkflow(c echo.Context) error {
	id := c.Param("id")
	if err := h.workflowSvc.DeleteWorkflow(c.Request().Context(), id); err != nil {
		log.Printf("[handler] DeleteWorkflow error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to delete workflow")
	}
	return c.NoContent(http.StatusOK)
}

// AddWorkflowStep adds a step to a workflow
func (h *Handler) AddWorkflowStep(c echo.Context) error {
	workflowID := c.Param("id")

	step := &models.WorkflowStep{
		WorkflowID: workflowID,
		Name:       c.FormValue("name"),
		StepType:   models.StepType(c.FormValue("step_type")),
		Prompt:     c.FormValue("prompt"),
		DependsOn:  "[]",
		Config:     "{}",
	}

	if step.Name == "" {
		return c.String(http.StatusBadRequest, "Step name is required")
	}
	if step.StepType == "" {
		step.StepType = models.StepTypeExecute
	}

	// Parse step order
	orderStr := c.FormValue("step_order")
	if orderStr != "" {
		fmt.Sscanf(orderStr, "%d", &step.StepOrder)
	}

	// Agent assignment
	agentID := c.FormValue("agent_id")
	if agentID != "" {
		step.AgentID = &agentID
	}

	if err := h.workflowSvc.CreateStep(c.Request().Context(), step); err != nil {
		log.Printf("[handler] AddWorkflowStep error: %v", err)
		return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to add step: %v", err))
	}

	return c.JSON(http.StatusOK, step)
}

// DeleteWorkflowStep removes a step from a workflow
func (h *Handler) DeleteWorkflowStep(c echo.Context) error {
	stepID := c.Param("stepId")
	if err := h.workflowSvc.DeleteStep(c.Request().Context(), stepID); err != nil {
		log.Printf("[handler] DeleteWorkflowStep error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to delete step")
	}
	return c.NoContent(http.StatusOK)
}

// ExecuteWorkflow starts a workflow execution
func (h *Handler) ExecuteWorkflow(c echo.Context) error {
	workflowID := c.Param("id")
	taskID := c.FormValue("task_id")

	if taskID == "" {
		return c.String(http.StatusBadRequest, "task_id is required")
	}

	exec, err := h.workflowSvc.ExecuteWorkflow(c.Request().Context(), workflowID, taskID)
	if err != nil {
		log.Printf("[handler] ExecuteWorkflow error: %v", err)
		return c.String(http.StatusInternalServerError, fmt.Sprintf("Failed to execute workflow: %v", err))
	}

	return c.JSON(http.StatusOK, exec)
}

// CancelWorkflowExecution cancels a running workflow execution
func (h *Handler) CancelWorkflowExecution(c echo.Context) error {
	execID := c.Param("execId")
	if err := h.workflowSvc.CancelWorkflowExecution(c.Request().Context(), execID); err != nil {
		log.Printf("[handler] CancelWorkflowExecution error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to cancel workflow execution")
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
}

// GetWorkflowExecution returns workflow execution details with step executions
func (h *Handler) GetWorkflowExecution(c echo.Context) error {
	execID := c.Param("execId")

	exec, err := h.workflowSvc.GetWorkflowExecution(c.Request().Context(), execID)
	if err != nil {
		return c.String(http.StatusNotFound, "Workflow execution not found")
	}

	stepExecs, err := h.workflowSvc.ListStepExecutions(c.Request().Context(), execID)
	if err != nil {
		log.Printf("[handler] GetWorkflowExecution steps error: %v", err)
		stepExecs = nil
	}

	// Get workflow and step definitions for display
	workflow, _ := h.workflowSvc.GetWorkflow(c.Request().Context(), exec.WorkflowID)
	steps, _ := h.workflowSvc.ListSteps(c.Request().Context(), exec.WorkflowID)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"Execution":      exec,
		"StepExecutions": stepExecs,
		"Workflow":       workflow,
		"Steps":          steps,
	})
}

// ListTemplates returns available workflow templates
func (h *Handler) ListWorkflowTemplates(c echo.Context) error {
	templates, err := h.workflowSvc.ListTemplates(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ListWorkflowTemplates error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to list templates")
	}
	return c.JSON(http.StatusOK, templates)
}

// AnalyzeTaskComplexity analyzes a task and recommends a workflow
func (h *Handler) AnalyzeTaskComplexity(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskRepo.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return c.String(http.StatusNotFound, "Task not found")
	}

	recommendation, err := h.workflowSvc.AnalyzeTaskComplexity(c.Request().Context(), task)
	if err != nil {
		log.Printf("[handler] AnalyzeTaskComplexity error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to analyze task")
	}

	return c.JSON(http.StatusOK, recommendation)
}

// GetAgentMetrics returns performance metrics for a specific agent
// @Summary Get metrics for a model
// @Description Returns workflow performance metrics for a specific model configuration.
// @Tags workflows
// @Produce json
// @Param agentId path string true "Model configuration ID"
// @Success 200 {array} models.AgentPerformanceMetric "Model performance metrics"
// @Failure 500 {string} string "Failed to get metrics"
// @Router /api/workflows/metrics/{agentId} [get]
func (h *Handler) GetAgentMetrics(c echo.Context) error {
	agentID := c.Param("agentId")

	metrics, err := h.workflowSvc.GetAgentMetrics(c.Request().Context(), agentID)
	if err != nil {
		log.Printf("[handler] GetAgentMetrics error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to get metrics")
	}

	return c.JSON(http.StatusOK, metrics)
}

// GetAllAgentMetrics returns performance metrics for all agents
// @Summary Get metrics for all models
// @Description Returns workflow performance metrics for all configured model configurations.
// @Tags workflows
// @Produce json
// @Success 200 {array} models.AgentPerformanceMetric "All model performance metrics"
// @Failure 500 {string} string "Failed to get metrics"
// @Router /api/workflows/metrics [get]
func (h *Handler) GetAllAgentMetrics(c echo.Context) error {
	metrics, err := h.workflowSvc.GetAllMetrics(c.Request().Context())
	if err != nil {
		log.Printf("[handler] GetAllAgentMetrics error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to get metrics")
	}

	return c.JSON(http.StatusOK, metrics)
}

// GetBestAgent returns the best agent for a given task type
// @Summary Get best model for task type
// @Description Returns the best-performing model for the requested task type based on workflow metrics.
// @Tags workflows
// @Produce json
// @Param task_type query string false "Task type" default(general)
// @Success 200 {object} map[string]interface{} "Best model and metric payload, or message when unavailable"
// @Failure 500 {string} string "Agent not found"
// @Router /api/workflows/best-agent [get]
func (h *Handler) GetBestAgent(c echo.Context) error {
	taskType := c.QueryParam("task_type")
	if taskType == "" {
		taskType = models.TaskTypeGeneral
	}

	metric, err := h.workflowSvc.GetBestAgentForTaskType(c.Request().Context(), taskType)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]string{"message": "No performance data available"})
	}

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), metric.AgentConfigID)
	if err != nil {
		return c.String(http.StatusInternalServerError, "Agent not found")
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":   agent,
		"metrics": metric,
	})
}

// GetCheapestAgent returns the cheapest agent meeting a quality threshold
// @Summary Get cheapest model for task type
// @Description Returns the cheapest model that meets the requested minimum quality threshold.
// @Tags workflows
// @Produce json
// @Param task_type query string false "Task type" default(general)
// @Param min_quality query number false "Minimum quality threshold (0..1)" default(0.6)
// @Success 200 {object} map[string]interface{} "Cheapest model and metric payload, or message when unavailable"
// @Failure 500 {string} string "Agent not found"
// @Router /api/workflows/cheapest-agent [get]
func (h *Handler) GetCheapestAgent(c echo.Context) error {
	taskType := c.QueryParam("task_type")
	if taskType == "" {
		taskType = models.TaskTypeGeneral
	}

	minQuality := 0.6
	if q := c.QueryParam("min_quality"); q != "" {
		fmt.Sscanf(q, "%f", &minQuality)
	}

	metric, err := h.workflowSvc.GetCheapestAgentForTaskType(c.Request().Context(), taskType, minQuality)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]string{"message": "No agents meet the quality threshold"})
	}

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), metric.AgentConfigID)
	if err != nil {
		return c.String(http.StatusInternalServerError, "Agent not found")
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":   agent,
		"metrics": metric,
	})
}

// GetVoteRecords returns votes for a step execution
// @Summary Get vote records for workflow step execution
// @Description Returns all agent vote records for a workflow voting step execution.
// @Tags workflows
// @Produce json
// @Param stepExecId path string true "Step execution ID"
// @Success 200 {array} models.VoteRecord "Vote records"
// @Failure 500 {string} string "Failed to get votes"
// @Router /api/workflows/votes/{stepExecId} [get]
func (h *Handler) GetVoteRecords(c echo.Context) error {
	stepExecID := c.Param("stepExecId")

	votes, err := h.workflowSvc.ListVoteRecords(c.Request().Context(), stepExecID)
	if err != nil {
		log.Printf("[handler] GetVoteRecords error: %v", err)
		return c.String(http.StatusInternalServerError, "Failed to get votes")
	}

	return c.JSON(http.StatusOK, votes)
}
