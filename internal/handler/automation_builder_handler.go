package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) NewAutomationBuilder(c echo.Context) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationNewContent(projectID))
	}
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.AutomationNew(projects, projectID))
}

func (h *Handler) CreateAutomationDraftWeb(c echo.Context) error {
	if h.automationDraftSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	source := strings.TrimSpace(c.FormValue("builder_source"))
	if source == "" {
		source = strings.TrimSpace(c.FormValue("source"))
	}
	var candidate models.AutomationDraftCandidate
	hasPostedCandidate := strings.TrimSpace(c.FormValue("candidate_json")) != ""
	retryPending := automationBuilderRetryPending(c)
	if hasPostedCandidate {
		candidate, err = service.DecodeAutomationDraftCandidate([]byte(strings.TrimSpace(c.FormValue("candidate_json"))))
	} else {
		switch source {
		case "template":
			candidate, err = h.automationDraftSvc.TemplateCandidate(strings.TrimSpace(c.FormValue("template_key")))
		case "blank":
			candidate, err = h.automationDraftSvc.BlankCandidate("")
		case "describe":
			var preview *models.AutomationDraftResult
			preview, err = h.previewAutomationDescription(ctx, projectID, c.FormValue("description"))
			if err == nil {
				candidate = preview.Candidate
			}
		default:
			err = echo.NewHTTPError(http.StatusBadRequest, "source must be template, describe, or blank")
		}
	}
	if err != nil {
		if source == "describe" {
			if isHTMX(c) {
				return render(c, http.StatusUnprocessableEntity, pages.AutomationNewFailureContent(projectID, err.Error()))
			}
			projects, _ := h.projectSvc.List(ctx)
			return render(c, http.StatusUnprocessableEntity, pages.AutomationNewFailure(projects, projectID, err.Error()))
		}
		return err
	}
	if hasPostedCandidate && (!retryPending || automationBuilderSaveRequested(c)) {
		applyAutomationDraftFormValues(c, &candidate)
		if err := h.applyAutomationBuilderAction(c, &candidate); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	result, err := h.automationDraftSvc.PreviewCandidate(ctx, projectID, candidate, nil)
	if err != nil {
		return err
	}
	page := models.AutomationBuilderPage{Result: *result, Source: source}
	applyAutomationBuilderRetryForm(c, &page)
	if retryPending && !automationBuilderSaveRequested(c) {
		page.Error = "This publication already created resources. Retry Save changes without editing, or reopen the builder to start over."
		return h.renderAutomationBuilder(c, page)
	}
	if !automationBuilderSaveRequested(c) {
		return h.renderAutomationBuilder(c, page)
	}
	return h.publishAutomationBuilderCandidate(c, projectID, page)
}

func (h *Handler) CloneAutomationDraftWeb(c echo.Context) error {
	if h.automationDraftSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	automationID := c.Param("automationId")
	published, err := h.automationDraftSvc.PublishedCandidate(ctx, projectID, automationID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	candidate := published.Candidate
	hasPostedCandidate := strings.TrimSpace(c.FormValue("candidate_json")) != ""
	retryPending := automationBuilderRetryPending(c)
	if hasPostedCandidate {
		candidate, err = service.DecodeAutomationDraftCandidate([]byte(strings.TrimSpace(c.FormValue("candidate_json"))))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if !retryPending || automationBuilderSaveRequested(c) {
			applyAutomationDraftFormValues(c, &candidate)
			if err := h.applyAutomationBuilderAction(c, &candidate); err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
		}
	}
	result, err := h.automationDraftSvc.PreviewCandidate(ctx, projectID, candidate, published.Definition)
	if err != nil {
		return err
	}
	page := models.AutomationBuilderPage{
		Result: *result, AutomationID: automationID,
		Source: published.Definition.Version.Source,
	}
	applyAutomationBuilderRetryForm(c, &page)
	if isHTMX(c) {
		c.Response().Header().Set("HX-Push-Url", "/automations/"+automationID+"?project_id="+projectID+"&view=definition")
	}
	if retryPending && !automationBuilderSaveRequested(c) {
		page.Error = "This publication already created resources. Retry Save changes without editing, or reopen the builder to start over."
		return h.renderAutomationBuilder(c, page)
	}
	if !automationBuilderSaveRequested(c) {
		return h.renderAutomationBuilder(c, page)
	}
	return h.publishAutomationBuilderCandidate(c, projectID, page)
}

func automationBuilderSaveRequested(c echo.Context) bool {
	return c.FormValue("save_changes") == "true" && strings.TrimSpace(c.FormValue("builder_action")) == "" &&
		strings.TrimSpace(c.FormValue("remove_node")) == "" && strings.TrimSpace(c.FormValue("remove_edge")) == ""
}

func automationBuilderRetryPending(c echo.Context) bool {
	return strings.TrimSpace(c.FormValue("retry_automation_id")) != "" ||
		strings.TrimSpace(c.FormValue("retry_version_id")) != "" ||
		strings.TrimSpace(c.FormValue("retry_plan_revision")) != ""
}

func applyAutomationBuilderRetryForm(c echo.Context, page *models.AutomationBuilderPage) {
	if page == nil {
		return
	}
	page.RetryAutomationID = strings.TrimSpace(c.FormValue("retry_automation_id"))
	page.RetryVersionID = strings.TrimSpace(c.FormValue("retry_version_id"))
	page.RetryPlanRevision = strings.TrimSpace(c.FormValue("retry_plan_revision"))
}

func automationDraftCandidatesEqual(left, right models.AutomationDraftCandidate) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (h *Handler) publishAutomationBuilderCandidate(c echo.Context, projectID string, page models.AutomationBuilderPage) error {
	if h.automationPlanner == nil || h.automationCompiler == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation publication unavailable")
	}
	if len(page.Result.ValidationErrors) > 0 {
		return h.renderAutomationBuilder(c, page)
	}
	if page.RetryAutomationID != "" || page.RetryVersionID != "" || page.RetryPlanRevision != "" {
		return h.retryAutomationBuilderPublication(c, projectID, page)
	}
	ctx := c.Request().Context()
	source := "manual"
	if page.Source == "template" {
		source = "template"
	}
	var staged *models.AutomationDraftResult
	var err error
	if page.AutomationID == "" {
		staged, err = h.automationDraftSvc.CreateDraft(ctx, service.AutomationDraftCreateRequest{
			ProjectID: projectID, Source: source, CreatedVia: "web", Candidate: page.Result.Candidate,
		})
	} else {
		staged, err = h.automationDraftSvc.CreateVersionForSave(ctx, projectID, page.AutomationID, source, page.Result.Candidate)
	}
	if err != nil {
		return err
	}
	if staged == nil || staged.Definition == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "automation save could not be staged")
	}
	automationID := staged.Definition.Automation.ID
	versionID := staged.Definition.Version.ID
	plan, err := h.automationPlanner.Plan(ctx, projectID, automationID, versionID)
	if err != nil {
		return err
	}
	if len(plan.Validation) > 0 {
		if discardErr := h.automationDraftSvc.DiscardStagedVersion(ctx, projectID, automationID, versionID); discardErr != nil {
			return discardErr
		}
		page.Result.ValidationErrors = plan.Validation
		return h.renderAutomationBuilder(c, page)
	}
	published, publishErr := h.automationCompiler.Publish(ctx, service.AutomationPublishRequest{
		ProjectID: projectID, AutomationID: automationID, VersionID: versionID, PlanRevision: plan.PlanRevision,
	})
	if publishErr != nil {
		page.Error = publishErr.Error()
		page.RetryAutomationID = automationID
		page.RetryVersionID = versionID
		page.RetryPlanRevision = plan.PlanRevision
		if published != nil {
			page.PublicationSteps = published.Resources
		}
		return h.renderAutomationBuilder(c, page)
	}
	return h.redirectToAutomation(c, projectID, automationID)
}

func (h *Handler) retryAutomationBuilderPublication(c echo.Context, projectID string, page models.AutomationBuilderPage) error {
	if page.RetryAutomationID == "" || page.RetryVersionID == "" || page.RetryPlanRevision == "" {
		page.Error = "The publication retry is incomplete. Reopen the builder before saving again."
		return h.renderAutomationBuilder(c, page)
	}
	if page.AutomationID != "" && page.AutomationID != page.RetryAutomationID {
		page.Error = "The publication retry does not belong to this Automation. Reopen the editor before saving."
		return h.renderAutomationBuilder(c, page)
	}
	ctx := c.Request().Context()
	staged, err := h.automationDraftSvc.GetDraft(ctx, projectID, page.RetryAutomationID, page.RetryVersionID)
	if err != nil || staged == nil || staged.Definition == nil || staged.Definition.Version.State != models.AutomationVersionDraft {
		page.Error = "The failed publication can no longer be retried. Reopen the builder before saving."
		return h.renderAutomationBuilder(c, page)
	}
	if !automationDraftCandidatesEqual(staged.Candidate, page.Result.Candidate) {
		page.Error = "The failed publication can only retry its exact saved design. Reopen the builder to make different changes."
		return h.renderAutomationBuilder(c, page)
	}
	plan, err := h.automationPlanner.Plan(ctx, projectID, page.RetryAutomationID, page.RetryVersionID)
	if err != nil || plan.PlanRevision != page.RetryPlanRevision || len(plan.Validation) > 0 {
		page.Error = "The failed publication plan changed and cannot be retried. Reopen the builder before saving."
		return h.renderAutomationBuilder(c, page)
	}
	published, publishErr := h.automationCompiler.Retry(ctx, service.AutomationPublishRequest{
		ProjectID: projectID, AutomationID: page.RetryAutomationID, VersionID: page.RetryVersionID, PlanRevision: page.RetryPlanRevision,
	})
	if publishErr != nil {
		page.Error = publishErr.Error()
		if published != nil {
			page.PublicationSteps = published.Resources
		}
		return h.renderAutomationBuilder(c, page)
	}
	return h.redirectToAutomation(c, projectID, page.RetryAutomationID)
}

func (h *Handler) redirectToAutomation(c echo.Context, projectID, automationID string) error {
	url := "/automations/" + automationID + "?project_id=" + projectID
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", url)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, url)
}

func applyAutomationDraftFormValues(c echo.Context, candidate *models.AutomationDraftCandidate) {
	if candidate == nil {
		return
	}
	if value, exists := automationDraftFormValue(c, "automation_name"); exists && strings.TrimSpace(value) != "" {
		candidate.Name = strings.TrimSpace(value)
	}
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		prefix := "node_" + node.Key + "_"
		if value, exists := automationDraftFormValue(c, prefix+"name"); exists && strings.TrimSpace(value) != "" {
			node.Name = strings.TrimSpace(value)
		}
		if _, ok := node.Config["prompt"]; ok {
			if value, exists := automationDraftFormValue(c, prefix+"prompt"); exists {
				node.Config["prompt"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"category"); exists {
				node.Config["category"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"priority"); exists {
				if priority, parseErr := strconv.Atoi(value); parseErr == nil {
					node.Config["priority"] = priority
				}
			}
			if value, exists := automationDraftFormValue(c, prefix+"agent_ref"); exists {
				node.Config["agent_ref"] = strings.TrimSpace(value)
			}
			if candidate.AdapterKey != service.AutomationAdapterCustom {
				if values, exists := automationDraftFormValues(c, prefix+"skills"); exists {
					node.Config["skills"] = values
				}
				if values, exists := automationDraftFormValues(c, prefix+"source_files"); exists {
					node.Config["source_files"] = values
				}
			}
		}
		if _, ok := node.Config["run_at"]; ok {
			if value, exists := automationDraftFormValue(c, prefix+"run_at"); exists {
				node.Config["run_at"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"repeat_type"); exists {
				node.Config["repeat_type"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"repeat_interval"); exists {
				if interval, parseErr := strconv.Atoi(value); parseErr == nil {
					node.Config["repeat_interval"] = interval
				}
			}
			if _, exists := automationDraftFormValue(c, prefix+"enabled"); exists || strings.TrimSpace(c.FormValue("builder_action")) == "" {
				node.Config["enabled"] = c.FormValue(prefix+"enabled") == "true"
			}
		}
		if _, ok := node.Config["notification_type"]; ok {
			if value, exists := automationDraftFormValue(c, prefix+"notification_type"); exists {
				node.Config["notification_type"] = strings.TrimSpace(value)
			}
			if value, exists := automationDraftFormValue(c, prefix+"instructions"); exists {
				node.Config["instructions"] = value
			}
		}
		if node.Role == "create_github_issue" {
			if value, exists := automationDraftFormValue(c, prefix+"instructions"); exists {
				node.Config["instructions"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"labels"); exists {
				labels := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' })
				for i := range labels {
					labels[i] = strings.TrimSpace(labels[i])
				}
				node.Config["labels"] = labels
			}
		}
		if node.Role == "open_pull_request" {
			if value, exists := automationDraftFormValue(c, prefix+"instructions"); exists {
				node.Config["instructions"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"base"); exists {
				node.Config["base"] = strings.TrimSpace(value)
			}
			if _, exists := automationDraftFormValue(c, prefix+"draft"); exists || strings.TrimSpace(c.FormValue("builder_action")) == "" {
				node.Config["draft"] = c.FormValue(prefix+"draft") == "true"
			}
		}
	}
	for i := range candidate.Edges {
		key := "edge_" + candidate.Edges[i].Key + "_label"
		if value, exists := automationDraftFormValue(c, key); exists {
			candidate.Edges[i].Label = strings.TrimSpace(value)
		}
		conditionKey := "edge_" + candidate.Edges[i].Key + "_state"
		if value, exists := automationDraftFormValue(c, conditionKey); exists {
			value = strings.TrimSpace(value)
			if value == "" {
				candidate.Edges[i].Condition = map[string]any{}
			} else {
				candidate.Edges[i].Condition = map[string]any{"state": value}
			}
		}
	}
}

func automationDraftFormValue(c echo.Context, key string) (string, bool) {
	if err := c.Request().ParseForm(); err != nil {
		return "", false
	}
	values, ok := c.Request().Form[key]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

func automationDraftFormValues(c echo.Context, key string) ([]string, bool) {
	if err := c.Request().ParseForm(); err != nil {
		return nil, false
	}
	values, ok := c.Request().Form[key]
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out, true
}

func (h *Handler) applyAutomationBuilderAction(c echo.Context, candidate *models.AutomationDraftCandidate) error {
	action := strings.TrimSpace(c.FormValue("builder_action"))
	if action == "" && strings.TrimSpace(c.FormValue("remove_node")) != "" {
		action = "remove_node"
	}
	if action == "" && strings.TrimSpace(c.FormValue("remove_edge")) != "" {
		action = "remove_edge"
	}
	if action == "" || candidate == nil {
		return nil
	}
	palette, err := h.automationDraftSvc.TemplateCandidate(candidate.AdapterKey)
	if err != nil {
		return err
	}
	switch action {
	case "create_node":
		name := strings.TrimSpace(c.FormValue("node_name"))
		nodeKind := strings.TrimSpace(c.FormValue("node_kind"))
		if strings.HasPrefix(nodeKind, "runtime:") {
			runtimeNodeKey := strings.TrimSpace(strings.TrimPrefix(nodeKind, "runtime:"))
			for _, existing := range candidate.Nodes {
				if existing.Key == runtimeNodeKey {
					return nil
				}
			}
			for _, node := range palette.Nodes {
				if node.Key != runtimeNodeKey {
					continue
				}
				if name != "" {
					node.Name = name
				}
				candidate.Nodes = append(candidate.Nodes, node)
				return nil
			}
			return echo.NewHTTPError(http.StatusBadRequest, "unsupported automation step")
		}
		if name == "" || len(name) > 200 {
			return echo.NewHTTPError(http.StatusBadRequest, "node name and purpose are required")
		}
		var nodeType models.AutomationNodeType
		var role string
		config := map[string]any{}
		switch nodeKind {
		case "schedule":
			nodeType, role = models.AutomationNodeTrigger, "fixed_schedule"
			config = map[string]any{"prompt": "Describe the scheduled work this node should perform.", "category": string(models.CategoryScheduled), "priority": 2, "run_at": "09:00", "repeat_type": string(models.RepeatDaily), "repeat_interval": 1, "enabled": true}
		case "agent_task":
			nodeType, role = models.AutomationNodeAgentTask, "task"
			config = map[string]any{"prompt": "Describe the work this node should perform.", "category": string(models.CategoryBacklog), "priority": 2}
		case "create_notification":
			nodeType, role = models.AutomationNodeAction, "create_notification"
			config = map[string]any{"notification_type": "approval_request", "instructions": "Summarize the proposal that needs a human decision."}
		case "human_approval":
			nodeType, role = models.AutomationNodeHumanGate, "native_approval"
			config = map[string]any{"approval_method": "native_alert"}
		case "create_github_issue":
			nodeType, role = models.AutomationNodeAction, "create_github_issue"
			config = map[string]any{"instructions": "Open one focused, reviewable GitHub issue.", "labels": []string{}}
		case "human_assignment":
			nodeType, role = models.AutomationNodeHumanGate, "github_assignment"
			config = map[string]any{"approval_method": "github_assignment"}
		case "github_inbox":
			nodeType, role = models.AutomationNodeAgentTask, "github_inbox"
			config = map[string]any{"prompt": "Process newly assigned GitHub issues.", "category": string(models.CategoryBacklog), "priority": 2}
		case "implementation":
			nodeType, role = models.AutomationNodeAgentTask, "implementation"
			config = map[string]any{"prompt": "Implement the accepted GitHub issue and run relevant validation.", "category": string(models.CategoryActive), "priority": 2}
		case "open_pull_request":
			nodeType, role = models.AutomationNodeAction, "open_pull_request"
			config = map[string]any{"instructions": "Open a reviewable pull request linked to the source issue.", "base": "", "draft": false}
		case "human_review":
			nodeType, role = models.AutomationNodeHumanGate, "pull_request_review"
			config = map[string]any{"approval_method": "pull_request_review"}
		case "outcome":
			nodeType, role = models.AutomationNodeOutcome, "completed"
		default:
			return echo.NewHTTPError(http.StatusBadRequest, "unsupported automation node purpose")
		}
		key := automationDraftUniqueKey(candidate, automationDraftKey(name, "node"), false)
		index := len(candidate.Nodes)
		candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{
			Key: key, Name: name, Type: nodeType, Role: role, Config: config,
			Position: &models.AutomationDraftPoint{X: float64((index % 4) * 260), Y: float64((index / 4) * 180)},
		})
		return nil
	case "add_node":
		key := strings.TrimSpace(c.FormValue("node_key"))
		for _, node := range candidate.Nodes {
			if node.Key == key {
				return nil
			}
		}
		for _, node := range palette.Nodes {
			if node.Key == key {
				candidate.Nodes = append(candidate.Nodes, node)
				return nil
			}
		}
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported node")
	case "remove_node":
		key := strings.TrimSpace(c.FormValue("remove_node"))
		if key == "" {
			key = strings.TrimSpace(c.FormValue("node_key"))
		}
		nodes := candidate.Nodes[:0]
		for _, node := range candidate.Nodes {
			if node.Key != key {
				nodes = append(nodes, node)
			}
		}
		candidate.Nodes = nodes
		edges := candidate.Edges[:0]
		for _, edge := range candidate.Edges {
			if edge.From != key && edge.To != key {
				edges = append(edges, edge)
			}
		}
		candidate.Edges = edges
		return nil
	case "connect_nodes":
		fromKey := strings.TrimSpace(c.FormValue("from_key"))
		toKey := strings.TrimSpace(c.FormValue("to_key"))
		if fromKey == toKey || !automationDraftContainsNode(candidate.Nodes, fromKey) || !automationDraftContainsNode(candidate.Nodes, toKey) {
			return echo.NewHTTPError(http.StatusBadRequest, "transition endpoints must be different nodes on the canvas")
		}
		for _, existing := range candidate.Edges {
			if existing.From == fromKey && existing.To == toKey {
				return nil
			}
		}
		for _, edge := range palette.Edges {
			if edge.From == fromKey && edge.To == toKey {
				candidate.Edges = append(candidate.Edges, edge)
				return nil
			}
		}
		baseKey := "edge_" + automationDraftKey(fromKey, "source") + "_" + automationDraftKey(toKey, "target")
		candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{
			Key: automationDraftUniqueKey(candidate, baseKey, true), From: fromKey, To: toKey, Condition: map[string]any{},
		})
		return nil
	case "add_edge":
		key := strings.TrimSpace(c.FormValue("edge_key"))
		for _, edge := range candidate.Edges {
			if edge.Key == key {
				return nil
			}
		}
		for _, edge := range palette.Edges {
			if edge.Key == key && automationDraftContainsNode(candidate.Nodes, edge.From) && automationDraftContainsNode(candidate.Nodes, edge.To) {
				candidate.Edges = append(candidate.Edges, edge)
				return nil
			}
		}
		return echo.NewHTTPError(http.StatusBadRequest, "transition endpoints must be on the canvas")
	case "remove_edge":
		key := strings.TrimSpace(c.FormValue("remove_edge"))
		if key == "" {
			key = strings.TrimSpace(c.FormValue("edge_key"))
		}
		edges := candidate.Edges[:0]
		for _, edge := range candidate.Edges {
			if edge.Key != key {
				edges = append(edges, edge)
			}
		}
		candidate.Edges = edges
		return nil
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported builder action")
	}
}

func automationDraftEditableNodeType(nodeType models.AutomationNodeType) bool {
	switch nodeType {
	case models.AutomationNodeTrigger, models.AutomationNodeAgentTask, models.AutomationNodeHumanGate,
		models.AutomationNodeAction, models.AutomationNodeCondition, models.AutomationNodeOutcome:
		return true
	default:
		return false
	}
}

func automationDraftKey(value, fallback string) string {
	var key strings.Builder
	lastUnderscore := false
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			key.WriteRune(character)
			lastUnderscore = false
			continue
		}
		if key.Len() > 0 && !lastUnderscore {
			key.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(key.String(), "_")
	if result == "" {
		return fallback
	}
	if len(result) > 80 {
		result = strings.TrimRight(result[:80], "_")
	}
	return result
}

func automationDraftUniqueKey(candidate *models.AutomationDraftCandidate, base string, edge bool) string {
	exists := func(key string) bool {
		if edge {
			for _, item := range candidate.Edges {
				if item.Key == key {
					return true
				}
			}
			return false
		}
		return automationDraftContainsNode(candidate.Nodes, key)
	}
	if !exists(base) {
		return base
	}
	for suffix := 2; ; suffix++ {
		key := base + "_" + strconv.Itoa(suffix)
		if !exists(key) {
			return key
		}
	}
}

func automationDraftContainsNode(nodes []models.AutomationDraftNode, key string) bool {
	for _, node := range nodes {
		if node.Key == key {
			return true
		}
	}
	return false
}

func (h *Handler) PauseAutomation(c echo.Context) error {
	return h.changeAutomationLifecycle(c, "pause")
}
func (h *Handler) ResumeAutomation(c echo.Context) error {
	return h.changeAutomationLifecycle(c, "resume")
}

func (h *Handler) DeleteAutomation(c echo.Context) error {
	if h.automationLifecycleSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation lifecycle unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if err := h.automationLifecycleSvc.Delete(c.Request().Context(), projectID, c.Param("automationId")); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	url := "/automations?project_id=" + projectID
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", url)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, url)
}

func (h *Handler) changeAutomationLifecycle(c echo.Context, action string) error {
	if h.automationLifecycleSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation lifecycle unavailable")
	}
	projectID, _ := h.getCurrentProjectID(c)
	var err error
	switch action {
	case "pause":
		err = h.automationLifecycleSvc.Pause(c.Request().Context(), projectID, c.Param("automationId"))
	case "resume":
		err = h.automationLifecycleSvc.Resume(c.Request().Context(), projectID, c.Param("automationId"))
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.Redirect(http.StatusSeeOther, "/automations/"+c.Param("automationId")+"?project_id="+projectID)
}

func (h *Handler) renderAutomationBuilder(c echo.Context, page models.AutomationBuilderPage) error {
	projectID, _ := h.getCurrentProjectID(c)
	if h.automationDraftSvc != nil {
		if palette, err := h.automationDraftSvc.TemplateCandidate(page.Result.Candidate.AdapterKey); err == nil {
			page.NodePalette = palette.Nodes
			page.EdgePalette = palette.Edges
		}
	}
	if h.automationCapabilitySvc != nil {
		capabilities, err := h.automationCapabilitySvc.Build(c.Request().Context(), projectID)
		if err != nil {
			return err
		}
		page.Capabilities = capabilities
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationBuilderContent(page, projectID))
	}
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.AutomationBuilder(projects, projectID, page))
}
