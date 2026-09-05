package handler

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type automationYAMLParseResult struct {
	Valid   bool   `json:"valid"`
	Message string `json:"message,omitempty"`
}

// ParseAutomationYAML verifies YAML syntax for the browser editor without
// normalizing, previewing, or persisting an Automation.
func (h *Handler) ParseAutomationYAML(c echo.Context) error {
	if _, err := h.getCurrentProjectID(c); err != nil {
		return err
	}
	rawYAML, submitted := automationDraftFormValue(c, "automation_yaml")
	if !submitted {
		return c.JSON(http.StatusOK, automationYAMLParseResult{Message: "YAML is required."})
	}
	if _, err := service.DecodeAutomationDraftYAML([]byte(rawYAML)); err != nil {
		return c.JSON(http.StatusOK, automationYAMLParseResult{Message: "Malformed YAML: " + err.Error()})
	}
	return c.JSON(http.StatusOK, automationYAMLParseResult{Valid: true})
}

func (h *Handler) BuildAutomationWeb(c echo.Context) error {
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
	rawYAML, yamlSubmitted := automationDraftFormValue(c, "automation_yaml")
	hasPostedCandidate := yamlSubmitted || strings.TrimSpace(c.FormValue("candidate_json")) != ""
	if hasPostedCandidate {
		candidate, err = decodeAutomationBuilderCandidate(c)
	} else {
		switch source {
		case "template":
			candidate, err = h.automationDraftSvc.CreationTemplateCandidate(strings.TrimSpace(c.FormValue("template_key")))
			if err == nil {
				h.applyAutomationTemplateDefaultModel(ctx, projectID, &candidate)
			}
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
		if yamlSubmitted {
			fallback, fallbackErr := h.automationDraftSvc.BlankCandidate("")
			if fallbackErr != nil {
				return fallbackErr
			}
			return h.renderAutomationBuilder(c, models.AutomationBuilderPage{
				Result: models.AutomationDraftResult{Candidate: fallback}, Source: source, YAML: rawYAML, YAMLProvided: true,
				Error: "YAML did not parse: " + err.Error(),
			})
		}
		if source == "template" {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if source == "describe" {
			message := "Could not generate a supported Automation: " + err.Error()
			description := c.FormValue("description")
			if isHTMX(c) {
				c.Response().Header().Set("HX-Retarget", "#automation-describe-modal-content")
				c.Response().Header().Set("HX-Reswap", "outerHTML")
				return render(c, http.StatusOK, pages.AutomationDescribeModalContent(projectID, description, message))
			}
			if h.automationGraphSvc == nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
			}
			page := parseCardPageRequest(c)
			cards, listErr := h.automationGraphSvc.ListPage(ctx, projectID, page.PageSize+1, page.Offset, page.Search)
			if listErr != nil {
				return listErr
			}
			cards, hasMore := cardPageItems(cards, page.PageSize)
			projects, _ := h.projectSvc.ListSelectorOptions(ctx)
			return render(c, http.StatusUnprocessableEntity, pages.AutomationsDescribeFailurePage(projects, projectID, cards, description, message, hasMore))
		}
		return err
	}
	if hasPostedCandidate {
		if err := h.applySubmittedAutomationBuilderCandidate(c, &candidate, yamlSubmitted); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	result, err := h.previewAutomationBuilderCandidate(ctx, projectID, candidate, nil)
	if err != nil {
		return err
	}
	page := models.AutomationBuilderPage{Result: *result, Source: source, InitialView: automationBuilderInitialView(c)}
	if !automationBuilderSaveRequested(c) {
		return h.renderAutomationBuilder(c, page)
	}
	return h.saveAutomationBuilderCandidate(c, projectID, page, false)
}

func (h *Handler) applyAutomationTemplateDefaultModel(_ context.Context, _ string, candidate *models.AutomationDraftCandidate) {
	service.ApplyAutomationTemplateDefaultModel(candidate)
}

func (h *Handler) EditAutomationBuilder(c echo.Context) error {
	if h.automationDraftSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	automationID := c.Param("automationId")
	opened, err := h.automationDraftSvc.LoadCurrentCandidate(ctx, projectID, automationID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	candidate := opened.Candidate
	currentTemplateRevision := service.CurrentAutomationTemplateRevision(candidate.AdapterKey)
	templateUpdateAvailable := currentTemplateRevision > 0 &&
		(opened.Definition.Automation.TemplateRevision == nil || *opened.Definition.Automation.TemplateRevision < currentTemplateRevision)
	isOpenRequest := c.Request().Method == http.MethodGet
	updateTemplate := !isOpenRequest && c.FormValue("update_template") == "true"
	if updateTemplate {
		if !templateUpdateAvailable {
			return echo.NewHTTPError(http.StatusBadRequest, "this Automation already uses the latest template")
		}
		candidate, err = h.automationDraftSvc.TemplateCandidate(candidate.AdapterKey)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		h.applyAutomationTemplateDefaultModel(ctx, projectID, &candidate)
		candidate.Name = opened.Candidate.Name
	} else if rawYAML, yamlSubmitted := automationDraftFormValue(c, "automation_yaml"); !isOpenRequest && (yamlSubmitted || strings.TrimSpace(c.FormValue("candidate_json")) != "") {
		candidate, err = decodeAutomationBuilderCandidate(c)
		if err != nil {
			validated, validationErr := h.automationDraftSvc.PreviewCandidate(ctx, projectID, opened.Candidate, opened.Definition)
			if validationErr != nil {
				return echo.NewHTTPError(http.StatusBadRequest, validationErr.Error())
			}
			return h.renderAutomationBuilder(c, models.AutomationBuilderPage{
				Result: *validated, AutomationID: automationID, Source: opened.Definition.Version.Source,
				TemplateUpdateAvailable: templateUpdateAvailable, LifecycleState: opened.Definition.Automation.LifecycleState,
				YAML: rawYAML, YAMLProvided: true, Error: "YAML did not parse: " + err.Error(),
			})
		}
		if err := h.applySubmittedAutomationBuilderCandidate(c, &candidate, yamlSubmitted); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	result, err := h.previewAutomationBuilderCandidate(ctx, projectID, candidate, opened.Definition)
	if err != nil {
		return err
	}
	page := models.AutomationBuilderPage{Result: *result, AutomationID: automationID, Source: opened.Definition.Version.Source,
		TemplateUpdateAvailable: templateUpdateAvailable, LifecycleState: opened.Definition.Automation.LifecycleState, InitialView: automationBuilderInitialView(c)}
	if isHTMX(c) {
		c.Response().Header().Set("HX-Push-Url", "/automations/"+automationID+"/builder?project_id="+projectID)
	}
	if isOpenRequest || (!updateTemplate && !automationBuilderSaveRequested(c)) {
		return h.renderAutomationBuilder(c, page)
	}
	return h.saveAutomationBuilderCandidate(c, projectID, page, updateTemplate)
}

func (h *Handler) applySubmittedAutomationBuilderCandidate(c echo.Context, candidate *models.AutomationDraftCandidate, yamlSubmitted bool) error {
	if !yamlSubmitted {
		h.discardStaleTemplateOnlyNodeConfig(candidate)
		applyAutomationDraftFormValues(c, candidate)
	}
	if !yamlSubmitted || automationBuilderVisualActionRequested(c) {
		return h.applyAutomationBuilderAction(c, candidate)
	}
	return nil
}

func (h *Handler) previewAutomationBuilderCandidate(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate, definition *models.AutomationDefinition) (*models.AutomationDraftResult, error) {
	if h.automationCompiler == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "automation preview unavailable")
	}
	plan, normalized, err := h.automationCompiler.PreviewSave(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	result := h.automationDraftSvc.PreviewValidatedCandidate(normalized, definition)
	result.Candidate = normalized
	result.ValidationErrors = plan.Validation
	return result, nil
}

func decodeAutomationBuilderCandidate(c echo.Context) (models.AutomationDraftCandidate, error) {
	if raw, yamlSubmitted := automationDraftFormValue(c, "automation_yaml"); yamlSubmitted {
		return service.DecodeAutomationDraftYAML([]byte(raw))
	}
	return service.DecodeAutomationDraftCandidate([]byte(strings.TrimSpace(c.FormValue("candidate_json"))))
}

func automationBuilderInitialView(c echo.Context) string {
	if strings.TrimSpace(c.FormValue("initial_view")) == "details" {
		return "details"
	}
	return ""
}

func automationBuilderVisualActionRequested(c echo.Context) bool {
	return strings.TrimSpace(c.FormValue("builder_action")) != "" ||
		strings.TrimSpace(c.FormValue("remove_node")) != "" ||
		strings.TrimSpace(c.FormValue("remove_edge")) != ""
}

func automationBuilderSaveRequested(c echo.Context) bool {
	return c.FormValue("save_changes") == "true" && strings.TrimSpace(c.FormValue("builder_action")) == "" &&
		strings.TrimSpace(c.FormValue("remove_node")) == "" && strings.TrimSpace(c.FormValue("remove_edge")) == ""
}

func (h *Handler) saveAutomationBuilderCandidate(c echo.Context, projectID string, page models.AutomationBuilderPage, updateToLatestTemplate bool) error {
	if h.automationCompiler == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation save unavailable")
	}
	if len(page.Result.ValidationErrors) > 0 {
		page.Error = "Save did not apply. Resolve the setup items below and try again."
		return h.renderAutomationBuilder(c, page)
	}
	source := "manual"
	if page.Source == "template" {
		source = "template"
	}
	saved, err := h.automationCompiler.SaveValidatedCandidate(c.Request().Context(), service.AutomationSaveRequest{
		ProjectID: projectID, AutomationID: page.AutomationID, Source: source, CreatedVia: "web", Candidate: page.Result.Candidate,
		UpdateToLatestTemplate: updateToLatestTemplate,
	})
	if err != nil {
		page.Error = "Save did not apply: " + err.Error()
		return h.renderAutomationBuilder(c, page)
	}
	if saved == nil || saved.Definition == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "automation save failed")
	}
	return h.redirectToAutomation(c, projectID, saved.Definition.Automation.ID)
}

func (h *Handler) redirectToAutomation(c echo.Context, projectID, automationID string) error {
	url := "/automations/" + automationID + "?project_id=" + projectID
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", url)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, url)
}

func (h *Handler) discardStaleTemplateOnlyNodeConfig(candidate *models.AutomationDraftCandidate) {
	if candidate == nil || (candidate.AdapterKey != service.AutomationAdapterNativeSDLC && candidate.AdapterKey != service.AutomationAdapterGitHubSDLC) {
		return
	}
	template, err := h.automationDraftSvc.TemplateCandidate(candidate.AdapterKey)
	if err != nil {
		return
	}
	canonicalNodes := make(map[string]struct{}, len(template.Nodes))
	for _, node := range template.Nodes {
		canonicalNodes[node.Key] = struct{}{}
	}
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		if _, canonical := canonicalNodes[node.Key]; canonical {
			continue
		}
		delete(node.Config, "skills")
		delete(node.Config, "source_files")
	}
}

func applyAutomationDraftFormValues(c echo.Context, candidate *models.AutomationDraftCandidate) {
	if candidate == nil {
		return
	}
	if value, exists := automationDraftFormValue(c, "automation_name"); exists {
		candidate.Name = strings.TrimSpace(value)
	}
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		prefix := "node_" + node.Key + "_"
		if value, exists := automationDraftFormValue(c, prefix+"name"); exists {
			node.Name = strings.TrimSpace(value)
		}
		if _, ok := node.Config["prompt"]; ok {
			if value, exists := automationDraftFormValue(c, prefix+"model_config_id"); exists {
				node.Config["model_config_id"] = strings.TrimSpace(value)
			}
			if value, exists := automationDraftFormValue(c, prefix+"prompt"); exists {
				node.Config["prompt"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"category"); exists {
				node.Config["category"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"priority"); exists {
				if priority, parseErr := strconv.Atoi(value); parseErr == nil {
					node.Config["priority"] = priority
				} else {
					node.Config["priority"] = value
				}
			}
			if value, exists := automationDraftFormValue(c, prefix+"agent_ref"); exists {
				node.Config["agent_ref"] = strings.TrimSpace(value)
			}
		}
		if _, ok := node.Config["model_config_id"]; ok {
			if value, exists := automationDraftFormValue(c, prefix+"model_config_id"); exists {
				node.Config["model_config_id"] = strings.TrimSpace(value)
			}
		}
		if value, exists := automationDraftFormValue(c, prefix+"goal"); exists {
			node.Config["goal"] = value
		}
		if _, ok := node.Config["run_at"]; ok {
			node.Config["enabled"] = true
			if value, exists := automationDraftFormValue(c, prefix+"run_at"); exists {
				node.Config["run_at"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"repeat_type"); exists {
				node.Config["repeat_type"] = value
			}
			if value, exists := automationDraftFormValue(c, prefix+"repeat_interval"); exists {
				if interval, parseErr := strconv.Atoi(value); parseErr == nil {
					node.Config["repeat_interval"] = interval
				} else {
					node.Config["repeat_interval"] = value
				}
			}
			if _, exists := automationDraftFormValue(c, prefix+"clear_context_on_start"); exists || strings.TrimSpace(c.FormValue("builder_action")) == "" {
				node.Config["clear_context_on_start"] = c.FormValue(prefix+"clear_context_on_start") == "true"
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
		defaultAdapterKey := service.AutomationAdapterCustom
		allowedResources := map[string]bool{}
		switch nodeKind {
		case "schedule":
			nodeType, role = models.AutomationNodeTrigger, "fixed_schedule"
			allowedResources = map[string]bool{"task": true, "schedule": true}
		case "task", "agent_task":
			nodeType, role = models.AutomationNodeAgentTask, "task"
			allowedResources = map[string]bool{"task": true}
		case "create_notification":
			nodeType, role = models.AutomationNodeAction, "create_notification"
		case "human_approval":
			nodeType, role = models.AutomationNodeHumanGate, "native_approval"
		case "native_inbox":
			nodeType, role = models.AutomationNodeTrigger, "native_inbox"
			defaultAdapterKey = service.AutomationAdapterNativeSDLC
			allowedResources = map[string]bool{"task": true, "schedule": true}
		case "native_implementation":
			nodeType, role = models.AutomationNodeAgentTask, "implementation"
			defaultAdapterKey = service.AutomationAdapterNativeSDLC
		case "create_github_issue":
			nodeType, role = models.AutomationNodeAction, "create_github_issue"
		case "human_assignment":
			nodeType, role = models.AutomationNodeHumanGate, "github_assignment"
		case "github_inbox":
			nodeType, role = models.AutomationNodeTrigger, "github_inbox"
			defaultAdapterKey = service.AutomationAdapterGitHubSDLC
			allowedResources = map[string]bool{"task": true, "schedule": true}
		case "open_pull_request":
			nodeType, role = models.AutomationNodeAction, "open_pull_request"
		case "human_review":
			nodeType, role = models.AutomationNodeHumanGate, "pull_request_review"
		case "outcome":
			nodeType, role = models.AutomationNodeOutcome, "completed"
		default:
			return echo.NewHTTPError(http.StatusBadRequest, "unsupported automation node purpose")
		}
		key := automationDraftUniqueKey(candidate, automationDraftKey(name, "node"), false)
		config, err := service.DefaultAutomationDraftNodeConfig(defaultAdapterKey, key, nodeType, role, allowedResources)
		if err != nil {
			return err
		}
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
			Key: automationDraftUniqueKey(candidate, baseKey, true), From: fromKey, To: toKey,
			FromPort: "right", ToPort: "left", Condition: map[string]any{},
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

func (h *Handler) RunAutomationNow(c echo.Context) error {
	if h.automationLifecycleSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation lifecycle unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	invocations, _, err := h.automationLifecycleSvc.RunNow(c.Request().Context(), projectID, c.Param("automationId"))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if startedInvocationID := firstStartedAutomationInvocationID(invocations); isHTMX(c) && h.automationGraphSvc != nil && startedInvocationID != "" {
		definition, _, defErr := h.automationGraphSvc.GetDefinition(c.Request().Context(), projectID, c.Param("automationId"))
		if defErr == nil && definition != nil && strings.TrimSpace(definition.Automation.Name) != "" {
			clickURL := "/automations/" + c.Param("automationId") + "?project_id=" + url.QueryEscape(projectID)
			setHTMXToastWithOptions(c, strings.TrimSpace(definition.Automation.Name)+" is now running.", "info", "", "", "", "automation:"+startedInvocationID, clickURL)
		}
	}
	if c.FormValue("return_to") == "portfolio" {
		if isHTMX(c) {
			return h.ListAutomations(c)
		}
		return c.Redirect(http.StatusSeeOther, "/automations?project_id="+projectID)
	}
	if isHTMX(c) {
		return h.GetAutomationLive(c)
	}
	return c.Redirect(http.StatusSeeOther, "/automations/"+c.Param("automationId")+"?project_id="+projectID)
}

func firstStartedAutomationInvocationID(invocations []models.AutomationInvocation) string {
	for _, invocation := range invocations {
		switch invocation.Status {
		case models.AutomationInvocationClaimed, models.AutomationInvocationDispatched, models.AutomationInvocationRunning:
			return invocation.ID
		}
	}
	return ""
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
	if c.FormValue("return_to") == "portfolio" {
		if isHTMX(c) {
			return h.ListAutomations(c)
		}
		return c.Redirect(http.StatusSeeOther, "/automations?project_id="+projectID)
	}
	return c.Redirect(http.StatusSeeOther, "/automations/"+c.Param("automationId")+"?project_id="+projectID)
}

func (h *Handler) renderAutomationBuilder(c echo.Context, page models.AutomationBuilderPage) error {
	projectID, _ := h.getCurrentProjectID(c)
	if !page.YAMLProvided && page.YAML == "" {
		encodedYAML, err := service.EncodeAutomationDraftYAML(page.Result.Candidate)
		if err != nil {
			return err
		}
		page.YAML = encodedYAML
	}
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
	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())
	return render(c, http.StatusOK, pages.AutomationBuilder(projects, projectID, page))
}
