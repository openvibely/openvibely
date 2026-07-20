package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
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
	source := strings.TrimSpace(c.FormValue("source"))
	templateKey := strings.TrimSpace(c.FormValue("template_key"))
	var candidate models.AutomationDraftCandidate
	switch source {
	case "template":
		candidate, err = h.automationDraftSvc.TemplateCandidate(templateKey)
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
	draftSource := "manual"
	if source == "template" {
		draftSource = "template"
	}
	result, err := h.automationDraftSvc.CreateDraft(ctx, service.AutomationDraftCreateRequest{ProjectID: projectID, Source: draftSource, CreatedVia: "web", Candidate: candidate})
	if err != nil {
		return err
	}
	h.setAutomationDraftPushURL(c, result, projectID)
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *result})
}

func (h *Handler) CloneAutomationDraftWeb(c echo.Context) error {
	if h.automationDraftSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	result, err := h.automationDraftSvc.ClonePublishedVersion(c.Request().Context(), projectID, c.Param("automationId"))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	h.setAutomationDraftPushURL(c, result, projectID)
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *result})
}

func (h *Handler) GetAutomationDraftWeb(c echo.Context) error {
	result, err := h.loadAutomationDraftWeb(c)
	if err != nil {
		return err
	}
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *result})
}

func (h *Handler) UpdateAutomationDraftWeb(c echo.Context) error {
	result, err := h.loadAutomationDraftWeb(c)
	if err != nil {
		return err
	}
	candidate := result.Candidate
	if raw := strings.TrimSpace(c.FormValue("candidate_json")); raw != "" {
		candidate, err = service.DecodeAutomationDraftCandidate([]byte(raw))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	applyAutomationDraftFormValues(c, &candidate)
	if err := h.applyAutomationBuilderAction(c, &candidate); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	projectID, _ := h.getCurrentProjectID(c)
	updated, err := h.automationDraftSvc.UpdateDraft(c.Request().Context(), c.Param("automationId"), c.Param("versionId"), projectID, candidate)
	if err != nil {
		return err
	}
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *updated})
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
			if values, exists := automationDraftFormValues(c, prefix+"skills"); exists {
				node.Config["skills"] = values
			}
			if values, exists := automationDraftFormValues(c, prefix+"source_files"); exists {
				node.Config["source_files"] = values
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
			config = map[string]any{"run_at": "09:00", "repeat_type": string(models.RepeatDaily), "repeat_interval": 1, "enabled": true}
		case "agent_task":
			nodeType, role = models.AutomationNodeAgentTask, "task"
			config = map[string]any{"prompt": "Describe the work this node should perform.", "category": string(models.CategoryScheduled), "priority": 2}
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
			config = map[string]any{"prompt": "Process newly assigned GitHub issues.", "category": string(models.CategoryScheduled), "priority": 2}
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

func (h *Handler) PlanAutomationDraftWeb(c echo.Context) error {
	if h.automationPlanner == nil || h.automationConfirmationSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation publication planning unavailable")
	}
	result, err := h.loadAutomationDraftWeb(c)
	if err != nil {
		return err
	}
	projectID, _ := h.getCurrentProjectID(c)
	plan, err := h.automationPlanner.Plan(c.Request().Context(), projectID, c.Param("automationId"), c.Param("versionId"))
	if err != nil {
		return err
	}
	page := models.AutomationBuilderPage{Result: *result, Plan: plan}
	if len(plan.Validation) == 0 {
		page.ConfirmationToken, err = h.automationConfirmationSvc.Issue(c.Request().Context(), service.AutomationConfirmationIssue{ProjectID: projectID, AutomationID: c.Param("automationId"), VersionID: c.Param("versionId"), PlanRevision: plan.PlanRevision, PrincipalID: h.authPrincipalID(c), ThreadID: "web:" + c.Param("automationId"), PlanMessageID: "web:" + repository.NewID()})
		if err != nil {
			return err
		}
	}
	return h.renderAutomationBuilder(c, page)
}

func (h *Handler) PublishAutomationDraftWeb(c echo.Context) error {
	if h.automationPlanner == nil || h.automationConfirmationSvc == nil || h.automationCompiler == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation publication unavailable")
	}
	ctx := c.Request().Context()
	projectID, _ := h.getCurrentProjectID(c)
	if _, err := h.loadAutomationDraftWeb(c); err != nil {
		return err
	}
	plan, err := h.automationPlanner.Plan(ctx, projectID, c.Param("automationId"), c.Param("versionId"))
	if err != nil {
		return err
	}
	if plan.PlanRevision != c.FormValue("plan_revision") {
		return echo.NewHTTPError(http.StatusConflict, "publication plan changed; preview again")
	}
	if _, err := h.automationConfirmationSvc.ConfirmWeb(ctx, c.FormValue("confirmation_token"), projectID, c.Param("automationId"), c.Param("versionId"), plan.PlanRevision, h.authPrincipalID(c), plan.Effects); err != nil {
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	}
	published, publishErr := h.automationCompiler.Publish(ctx, service.AutomationPublishRequest{ProjectID: projectID, AutomationID: c.Param("automationId"), VersionID: c.Param("versionId"), PlanRevision: plan.PlanRevision})
	if publishErr != nil {
		result, _ := h.loadAutomationDraftWeb(c)
		if result != nil {
			page := models.AutomationBuilderPage{Result: *result, Plan: plan, Error: publishErr.Error()}
			if published != nil {
				page.PublicationSteps = published.Resources
			}
			return h.renderAutomationBuilder(c, page)
		}
		return publishErr
	}
	url := "/automations/" + c.Param("automationId") + "?project_id=" + projectID
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", url)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, url)
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

func (h *Handler) loadAutomationDraftWeb(c echo.Context) (*models.AutomationDraftResult, error) {
	if h.automationDraftSvc == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return nil, err
	}
	result, err := h.automationDraftSvc.GetDraft(c.Request().Context(), projectID, c.Param("automationId"), c.Param("versionId"))
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "automation draft not found")
	}
	return result, nil
}

func (h *Handler) setAutomationDraftPushURL(c echo.Context, result *models.AutomationDraftResult, projectID string) {
	if !isHTMX(c) || result == nil || result.Definition == nil {
		return
	}
	automationID := strings.TrimSpace(result.Definition.Automation.ID)
	versionID := strings.TrimSpace(result.Definition.Version.ID)
	if automationID == "" || versionID == "" {
		return
	}
	c.Response().Header().Set("HX-Push-Url", "/automations/"+automationID+"/drafts/"+versionID+"?project_id="+projectID)
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
