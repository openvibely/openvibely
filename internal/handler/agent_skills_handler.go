package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/lifecycle"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

type agentSkillView struct {
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Source      string `json:"source,omitempty"`
	Path        string `json:"path,omitempty"`
	Content     string `json:"content,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
}

type agentSkillsResponse struct {
	AgentKey       string           `json:"agent_key"`
	AgentScope     string           `json:"agent_scope,omitempty"`
	RouterIndex    string           `json:"router_index,omitempty"`
	RoutedSkills   []agentSkillView `json:"routed_skills"`
	CanManage      bool             `json:"can_manage"`
	Protected      bool             `json:"protected"`
	GlobalRootSet  bool             `json:"global_root_set"`
	ProjectRootSet bool             `json:"project_root_set"`
}

type agentSkillSaveRequest struct {
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Body        string `json:"body"`
}

type agentSkillArchiveRequest struct {
	Reason string `json:"reason"`
}

// SetAgentSkillRoot configures the global on-disk root that contains agents/ and
// skills/. Project roots are resolved from the current project repo path.
func (h *Handler) SetAgentSkillRoot(root string) {
	h.agentSkillRoot = strings.TrimSpace(root)
}

// GetAgentSkills returns the routed on-disk skills owned by one agent. Legacy
// embedded skills are converted to on-disk agent-owned skills before listing so
// the assigned-agent router sees the same skills users see in the dialog.
func (h *Handler) GetAgentSkills(c echo.Context) error {
	agent, err := h.agentFromParam(c)
	if err != nil {
		return err
	}
	projectRoot, err := h.agentOwnedSkillProjectRoot(c, agent)
	if err != nil {
		return err
	}
	if err := h.migrateLegacyAgentSkills(c, agent, projectRoot); err != nil {
		return err
	}
	catalog, err := agentskills.BuildAgentCatalog("agent-dialog", h.agentSkillRoot, projectRoot, agentStableKey(agent))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := agentSkillsResponse{
		AgentKey:       agentStableKey(agent),
		AgentScope:     string(agent.Scope),
		RouterIndex:    agentskills.RenderAvailableAgentSkillsMarkdown(h.agentSkillRoot, projectRoot, agentStableKey(agent)),
		RoutedSkills:   make([]agentSkillView, 0, len(catalog.Entries())),
		CanManage:      agent.GeneratedStatus != models.AgentStatusProtected && h.agentSkillRoot != "",
		Protected:      agent.GeneratedStatus == models.AgentStatusProtected,
		GlobalRootSet:  h.agentSkillRoot != "",
		ProjectRootSet: projectRoot != "",
	}
	for _, entry := range catalog.Entries() {
		view := agentSkillView{
			Handle: entry.Handle,
			Name:   entry.Skill,
			Scope:  h.scopeForAgentSkillPathWithProjectRoot(entry.AbsolutePath, projectRoot),
			Source: string(entry.Source),
			Path:   entry.AbsolutePath,
		}
		if data, readErr := os.ReadFile(entry.AbsolutePath); readErr == nil {
			view.Content = string(data)
			if decl, body, parseErr := agentlibrary.ParseDeclaration(string(data)); parseErr == nil && decl != nil {
				view.Name = firstDialogNonEmpty(decl.Skill.Name, decl.Skill.Key, entry.Skill)
				view.Description = firstNonEmpty(decl.Skill.Description, decl.Routing.Description)
				view.Archived = decl.Skill.Archived
				view.Content, _ = agentlibrary.RenderSkillMarkdown(decl, body)
			}
		}
		out.RoutedSkills = append(out.RoutedSkills, view)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) CreateAgentOwnedSkill(c echo.Context) error {
	agent, err := h.agentFromParam(c)
	if err != nil {
		return err
	}
	if err := h.ensureAgentSkillManageAllowed(agent); err != nil {
		return err
	}
	var req agentSkillSaveRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
	}
	res, err := h.writeAgentOwnedSkillFromDialog(c, agent, req, false)
	if err != nil {
		return err
	}
	eventType := models.SkillEventEdited
	if res != nil && len(res.Created) > 0 {
		eventType = models.SkillEventCreated
	}
	h.recordManualSkillEvent(c, eventType, req.Handle, models.SkillScopeAgentOwned, agent.ID)
	return h.GetAgentSkills(c)
}

func (h *Handler) UpdateAgentOwnedSkill(c echo.Context) error {
	agent, err := h.agentFromParam(c)
	if err != nil {
		return err
	}
	if err := h.ensureAgentSkillManageAllowed(agent); err != nil {
		return err
	}
	handle := strings.TrimSpace(c.Param("skill"))
	var req agentSkillSaveRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
	}
	if req.Handle == "" {
		req.Handle = handle
	}
	if req.Handle != handle {
		return echo.NewHTTPError(http.StatusBadRequest, "skill handle mismatch")
	}
	if _, err := h.writeAgentOwnedSkillFromDialog(c, agent, req, true); err != nil {
		return err
	}
	h.recordManualSkillEvent(c, models.SkillEventEdited, req.Handle, models.SkillScopeAgentOwned, agent.ID)
	return h.GetAgentSkills(c)
}

func (h *Handler) ArchiveAgentOwnedSkill(c echo.Context) error {
	agent, err := h.agentFromParam(c)
	if err != nil {
		return err
	}
	if err := h.ensureAgentSkillManageAllowed(agent); err != nil {
		return err
	}
	handle := strings.TrimSpace(c.Param("skill"))
	if !validDialogSkillKey(handle) {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid skill handle")
	}
	var req agentSkillArchiveRequest
	_ = json.NewDecoder(c.Request().Body).Decode(&req)
	scope := h.dialogSkillScope(c, agent, c.QueryParam("scope"))
	projectRoot, err := h.agentOwnedSkillProjectRoot(c, agent)
	if err != nil {
		return err
	}
	root, err := h.rootForDialogScopeWithProjectRoot(scope, projectRoot)
	if err != nil {
		return err
	}
	path := filepath.Join(root, "agents", agentStableKey(agent), "skills", handle, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return echo.NewHTTPError(http.StatusNotFound, "skill not found at selected scope")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	decl, body, err := agentlibrary.ParseDeclaration(string(data))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	falseValue := false
	decl.Skill.Enabled = &falseValue
	decl.Skill.Archived = true
	decl.Skill.ArchiveReason = strings.TrimSpace(req.Reason)
	if decl.Skill.ArchiveReason == "" {
		decl.Skill.ArchiveReason = "Archived from the agent dialog"
	}
	rendered, err := agentlibrary.RenderSkillMarkdown(decl, body)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	h.recordManualSkillEvent(c, models.SkillEventEdited, handle, models.SkillScopeAgentOwned, agent.ID)
	return h.GetAgentSkills(c)
}

func (h *Handler) writeAgentOwnedSkillFromDialog(c echo.Context, agent *models.Agent, req agentSkillSaveRequest, requireExisting bool) (*agentlibrary.ImportResult, error) {
	handle := strings.TrimSpace(req.Handle)
	if !validDialogSkillKey(handle) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "skill handle must be a slug and must not include an agent prefix")
	}
	scope := h.dialogSkillScope(c, agent, req.Scope)
	projectRoot, err := h.agentOwnedSkillProjectRoot(c, agent)
	if err != nil {
		return nil, err
	}
	root, err := h.rootForDialogScopeWithProjectRoot(scope, projectRoot)
	if err != nil {
		return nil, err
	}
	if requireExisting {
		if _, statErr := os.Stat(filepath.Join(root, "agents", agentStableKey(agent), "skills", handle, "SKILL.md")); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, echo.NewHTTPError(http.StatusNotFound, "skill not found at selected scope")
			}
			return nil, echo.NewHTTPError(http.StatusInternalServerError, statErr.Error())
		}
	}
	decl, body, err := normalizeSkillDialogDeclaration(skillDialogNormalizationRequest{
		Handle:      handle,
		Scope:       scope,
		Name:        req.Name,
		Description: req.Description,
		Body:        req.Body,
	})
	if err != nil {
		return nil, err
	}
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: projectRoot}, agentlibrary.NewRepoApplier(h.agentRepo, h.lifecycleRepo))
	res, err := importer.WriteAgentOwnedSkill(c.Request().Context(), scope, agentStableKey(agent), decl, body)
	if err != nil {
		return res, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return res, nil
}

type skillDialogNormalizationRequest struct {
	Handle               string
	Scope                string
	Name                 string
	Description          string
	Body                 string
	Enabled              *bool
	RejectAgentOwnership bool
}

func normalizeSkillDialogDeclaration(req skillDialogNormalizationRequest) (*agentlibrary.SkillDeclaration, string, error) {
	body := strings.TrimSpace(req.Body)
	parsedFrontmatter := strings.HasPrefix(body, "---")
	var decl *agentlibrary.SkillDeclaration
	if parsedFrontmatter {
		parsed, parsedBody, parseErr := agentlibrary.ParseDeclaration(body)
		if parseErr != nil {
			return nil, "", echo.NewHTTPError(http.StatusBadRequest, parseErr.Error())
		}
		if req.RejectAgentOwnership && (parsed.IsAgentRootDeclaration() || strings.TrimSpace(parsed.Agent.Key) != "") {
			return nil, "", echo.NewHTTPError(http.StatusBadRequest, "standalone skills must not set agent.key")
		}
		if parsed.Skill.Key != req.Handle {
			return nil, "", echo.NewHTTPError(http.StatusBadRequest, "body frontmatter skill.key must match handle")
		}
		decl = parsed
		body = parsedBody
	} else {
		decl = &agentlibrary.SkillDeclaration{
			Kind:    "openvibely.agent_skill",
			Version: 1,
			Skill: agentlibrary.SkillBlock{
				Key: req.Handle,
				// Enabled left nil: absence = enabled, keeps frontmatter clean.
			},
		}
	}
	if req.RejectAgentOwnership {
		decl.Agent.Key = ""
	}
	decl.Skill.Key = req.Handle
	decl.Skill.Scope = req.Scope
	decl.Skill.Name = strings.TrimSpace(req.Name)
	if decl.Skill.Name == "" && !parsedFrontmatter {
		decl.Skill.Name = req.Handle
	}
	decl.Skill.Description = strings.TrimSpace(req.Description)
	if req.Enabled != nil {
		if !*req.Enabled {
			decl.Skill.Enabled = req.Enabled
		} else {
			decl.Skill.Enabled = nil
		}
	}
	return decl, body, nil
}

func (h *Handler) agentFromParam(c echo.Context) (*models.Agent, error) {
	if h.agentRepo == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "agent repo not configured")
	}
	agentID := strings.TrimSpace(c.Param("id"))
	if agentID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "agent id is required")
	}
	agent, err := h.agentRepo.GetByID(c.Request().Context(), agentID)
	if err != nil || agent == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}
	if agentStableKey(agent) == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "agent key is required before managing routed skills")
	}
	return agent, nil
}

func (h *Handler) ensureAgentSkillManageAllowed(agent *models.Agent) error {
	if agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}
	if agent.GeneratedStatus == models.AgentStatusProtected {
		return echo.NewHTTPError(http.StatusForbidden, "protected system agents are read-only in the dialog")
	}
	if h.agentSkillRoot == "" {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "agent skill root not configured")
	}
	return nil
}

func (h *Handler) currentProjectSkillRoot(c echo.Context) string {
	if h == nil || h.projectRepo == nil {
		return ""
	}
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" && h.projectSvc != nil {
		projectID, _ = h.getCurrentProjectID(c)
	}
	if projectID == "" {
		return ""
	}
	return service.ProjectSkillRootForResolver(c.Request().Context(), h.projectRepo, projectID)
}

// projectSkillRootForAgent resolves the correct project skill root for an
// agent-scoped operation. It prefers the project recorded on the agent row so
// that requests lacking a query-string project_id still target the right
// directory (covers non-browser callers and older UI requests). Falls back to
// the request-derived current project when the agent has no ProjectID set.
func (h *Handler) projectSkillRootForAgent(c echo.Context, agent *models.Agent) string {
	if agent != nil && strings.TrimSpace(agent.ProjectID) != "" {
		if h.projectRepo == nil {
			return ""
		}
		return service.ProjectSkillRootForResolver(c.Request().Context(), h.projectRepo, strings.TrimSpace(agent.ProjectID))
	}
	return h.currentProjectSkillRoot(c)
}

func (h *Handler) agentOwnedSkillProjectRoot(c echo.Context, agent *models.Agent) (string, error) {
	if agent != nil && agent.Scope == models.AgentScopeProject {
		agentProjectID := strings.TrimSpace(agent.ProjectID)
		if agentProjectID != "" {
			if requestedProjectID := strings.TrimSpace(c.QueryParam("project_id")); requestedProjectID != "" && requestedProjectID != agentProjectID {
				return "", echo.NewHTTPError(http.StatusNotFound, "agent not found")
			}
			return h.projectSkillRootForAgent(c, agent), nil
		}
	}
	return h.currentProjectSkillRoot(c), nil
}

func (h *Handler) rootForDialogScope(c echo.Context, scope string) (string, error) {
	return h.rootForDialogScopeWithProjectRoot(scope, h.currentProjectSkillRoot(c))
}

func (h *Handler) rootForDialogScopeWithProjectRoot(scope, projectRoot string) (string, error) {
	switch scope {
	case "global":
		if h.agentSkillRoot == "" {
			return "", echo.NewHTTPError(http.StatusServiceUnavailable, "global skill root not configured")
		}
		return h.agentSkillRoot, nil
	case "project":
		if projectRoot == "" {
			return "", echo.NewHTTPError(http.StatusServiceUnavailable, "project skill root not configured")
		}
		return projectRoot, nil
	case "":
		if projectRoot != "" {
			return projectRoot, nil
		}
		if h.agentSkillRoot != "" {
			return h.agentSkillRoot, nil
		}
		return "", echo.NewHTTPError(http.StatusServiceUnavailable, "no writable skill root configured")
	default:
		return "", echo.NewHTTPError(http.StatusBadRequest, "scope must be project or global")
	}
}

func (h *Handler) dialogSkillScope(c echo.Context, agent *models.Agent, requested string) string {
	scope := strings.ToLower(strings.TrimSpace(requested))
	if scope == "global" || scope == "project" {
		return scope
	}
	if agent != nil && agent.Scope == models.AgentScopeGlobal {
		return "global"
	}
	return "project"
}

func (h *Handler) materializeDBAgentsToDisk(c echo.Context, agents []models.Agent) (bool, error) {
	if h == nil || h.agentRepo == nil || h.agentSkillRoot == "" {
		return false, nil
	}
	projectRoot := h.currentProjectSkillRoot(c)
	usedKeys := h.usedAgentKeys(agents, projectRoot)
	persistenceChanged := false
	for i := range agents {
		agent := agents[i]
		agentProjectRoot := h.projectRootForAgentMaterialization(c, &agent)
		originalKey := agent.Key
		originalSkillCount := len(agent.Skills)
		originalSelectable := agent.SelectableAsPrimary
		if err := h.materializeAgentToDiskWithUsedKeys(c, &agent, agentProjectRoot, usedKeys, false); err != nil {
			return persistenceChanged, err
		}
		if err := h.migrateLegacyAgentSkills(c, &agent, agentProjectRoot); err != nil {
			return persistenceChanged, err
		}
		if agent.Key != originalKey || len(agent.Skills) != originalSkillCount || agent.SelectableAsPrimary != originalSelectable {
			persistenceChanged = true
		}
	}
	return persistenceChanged, nil
}

func (h *Handler) projectRootForAgentMaterialization(c echo.Context, agent *models.Agent) string {
	if agent != nil && agent.Scope == models.AgentScopeProject && strings.TrimSpace(agent.ProjectID) != "" {
		return h.projectSkillRootForAgent(c, agent)
	}
	return h.currentProjectSkillRoot(c)
}

func (h *Handler) materializeAgentToDisk(c echo.Context, agent *models.Agent, projectRoot string) error {
	return h.materializeAgentToDiskWithUsedKeys(c, agent, projectRoot, h.usedAgentKeys(nil, projectRoot), true)
}

func (h *Handler) materializeAgentToDiskWithUsedKeys(c echo.Context, agent *models.Agent, projectRoot string, usedKeys map[string]bool, overwriteExisting bool) error {
	if agent == nil || h == nil || h.agentRepo == nil || h.agentSkillRoot == "" {
		return nil
	}
	if agent.GeneratedStatus == models.AgentStatusProtected || agent.GeneratedStatus == models.AgentStatusArchived || agent.CreatedBy == models.AgentCreatedBySystem || agent.SystemKind != "" {
		return nil
	}
	scope := string(agent.Scope)
	if scope != "project" {
		scope = "global"
	}
	if scope == "project" && projectRoot == "" {
		// A project-owned agent must never fall back to the global root when its
		// recorded project has no writable repository root. Legacy project rows
		// without ProjectID retain the historical current-project/global fallback.
		if strings.TrimSpace(agent.ProjectID) != "" {
			return nil
		}
		scope = "global"
	}
	root, err := h.rootForDialogScopeWithProjectRoot(scope, projectRoot)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(agent.Key)
	if key == "" || !validDialogSkillKey(key) {
		base := slugifyLegacyAgentSkillName(firstDialogNonEmpty(agent.Name, agent.ID, "agent"))
		key = uniqueAgentSkillKey(base, usedKeys)
		agent.Key = key
		usedKeys[key] = true
	} else {
		usedKeys[key] = true
	}
	if _, err := os.Stat(filepath.Join(root, "agents", key, "SKILLS.md")); err == nil && !overwriteExisting {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	decl := h.agentRootDeclarationFromModel(c, agent, scope)
	body := h.agentRootBodyFromModel(agent)
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: projectRoot}, nil)
	if _, err := importer.WriteAgentRootDeclaration(c.Request().Context(), decl, body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "write agent declaration "+key+": "+err.Error())
	}
	if err := h.agentRepo.Update(c.Request().Context(), agent); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return nil
}

func (h *Handler) usedAgentKeys(agents []models.Agent, projectRoot string) map[string]bool {
	used := map[string]bool{}
	for _, agent := range agents {
		if key := strings.TrimSpace(agent.Key); key != "" {
			used[key] = true
		}
	}
	for _, root := range []string{h.agentSkillRoot, projectRoot} {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, "agents"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				used[entry.Name()] = true
			}
		}
	}
	return used
}

func (h *Handler) agentRootDeclarationFromModel(c echo.Context, agent *models.Agent, scope string) *agentlibrary.SkillDeclaration {
	enabled := agent.Enabled
	decl := &agentlibrary.SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Agent: agentlibrary.AgentDeclaration{
			Key:                 agentStableKey(agent),
			Name:                strings.TrimSpace(agent.Name),
			Description:         strings.TrimSpace(agent.Description),
			Enabled:             &enabled,
			SelectableAsPrimary: agent.SelectableAsPrimary,
			Scope:               scope,
			ProjectID:           strings.TrimSpace(agent.ProjectID),
			SystemPrompt:        strings.TrimSpace(agent.SystemPrompt),
		},
		EvidenceRefs:   append([]string(nil), agent.SourceRefs...),
		LifecycleHooks: h.lifecycleHookDeclsForAgent(contextFromEcho(c), agent.ID),
	}
	return decl
}

func contextFromEcho(c echo.Context) context.Context {
	if c == nil || c.Request() == nil || c.Request().Context() == nil {
		return context.Background()
	}
	return c.Request().Context()
}

func (h *Handler) lifecycleHookDeclsForAgent(ctx context.Context, agentID string) map[string]agentlibrary.HookDecl {
	out := map[string]agentlibrary.HookDecl{}
	if h == nil || h.lifecycleRepo == nil || strings.TrimSpace(agentID) == "" {
		return out
	}
	hooks, err := h.lifecycleRepo.HooksByAgent(ctx, agentID)
	if err != nil {
		return out
	}
	for _, hook := range hooks {
		when := string(hook.When)
		if when == "" || when == "task_mode" {
			continue
		}
		enabled := hook.Enabled
		decl := agentlibrary.HookDecl{
			Enabled:        &enabled,
			Skill:          strings.TrimSpace(hook.SkillKey),
			Blocking:       hook.Blocking,
			OutputContract: strings.TrimSpace(string(hook.OutputContract)),
			PromptOverride: strings.TrimSpace(hook.PromptOverride),
		}
		if strings.TrimSpace(hook.PermissionsJSON) != "" {
			perms := map[string]bool{}
			if err := json.Unmarshal([]byte(hook.PermissionsJSON), &perms); err == nil && len(perms) > 0 {
				decl.Permissions = perms
			}
		}
		if strings.TrimSpace(hook.RunPolicyJSON) != "" {
			var runPolicy map[string]string
			if err := json.Unmarshal([]byte(hook.RunPolicyJSON), &runPolicy); err == nil {
				decl.RunPolicy = strings.TrimSpace(runPolicy["when"])
			}
		}
		if strings.TrimSpace(hook.ScheduleJSON) != "" {
			var schedule map[string]string
			if err := json.Unmarshal([]byte(hook.ScheduleJSON), &schedule); err == nil {
				decl.ScheduleCron = strings.TrimSpace(schedule["cron"])
			}
		}
		if payload := lifecycle.ParseHookPayload(hook.PayloadJSON); !payload.SelectsAllBlocks() {
			decl.Payload = payload.Blocks
		}
		out[when] = decl
	}
	return out
}

func (h *Handler) agentRootBodyFromModel(agent *models.Agent) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstDialogNonEmpty(agent.Name, agentStableKey(agent), "Agent"))
	b.WriteString("\n")
	if strings.TrimSpace(agent.Description) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(agent.Description))
		b.WriteString("\n")
	}
	return b.String()
}

func (h *Handler) migrateLegacyAgentSkills(c echo.Context, agent *models.Agent, projectRoot string) error {
	if agent == nil || len(agent.Skills) == 0 {
		return nil
	}
	if h.agentSkillRoot == "" {
		return nil
	}
	if agent.GeneratedStatus == models.AgentStatusProtected {
		return nil
	}
	if h.agentRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "agent repo not configured")
	}
	if strings.TrimSpace(agent.Key) == "" || !validDialogSkillKey(agent.Key) {
		if err := h.materializeAgentToDisk(c, agent, projectRoot); err != nil {
			return err
		}
	}
	agentKey := agentStableKey(agent)
	scope := string(agent.Scope)
	if scope != "project" {
		scope = "global"
	}
	if scope == "project" && projectRoot == "" {
		// A project-owned agent must never fall back to the global root when its
		// recorded project has no writable repository root. Legacy project rows
		// without ProjectID retain the historical current-project/global fallback.
		if strings.TrimSpace(agent.ProjectID) != "" {
			return nil
		}
		scope = "global"
	}
	root, err := h.rootForDialogScopeWithProjectRoot(scope, projectRoot)
	if err != nil {
		return err
	}
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: projectRoot}, agentlibrary.NewRepoApplier(h.agentRepo, h.lifecycleRepo))
	used := h.existingAgentSkillKeys(root, agentKey)
	remaining := make([]models.SkillConfig, 0)
	converted := false
	for _, legacy := range agent.Skills {
		name := strings.TrimSpace(legacy.Name)
		body := strings.TrimSpace(legacy.Content)
		if name == "" || body == "" {
			remaining = append(remaining, legacy)
			continue
		}
		handle := uniqueAgentSkillKey(slugifyLegacyAgentSkillName(name), used)
		used[handle] = true
		enabled := true
		decl := &agentlibrary.SkillDeclaration{
			Kind:    "openvibely.agent_skill",
			Version: 1,
			Skill: agentlibrary.SkillBlock{
				Key:         handle,
				Name:        name,
				Description: strings.TrimSpace(legacy.Description),
				Enabled:     &enabled,
			},
		}
		if strings.TrimSpace(legacy.Tools) != "" {
			body = fmt.Sprintf("Allowed legacy tools: %s\n\n%s", strings.TrimSpace(legacy.Tools), body)
		}
		if _, err := importer.WriteAgentOwnedSkill(c.Request().Context(), scope, agentKey, decl, body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "migrate legacy agent skill "+name+": "+err.Error())
		}
		converted = true
	}
	if !converted {
		return nil
	}
	agent.Skills = remaining
	agent.SelectableAsPrimary = true
	if err := h.agentRepo.Update(c.Request().Context(), agent); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return nil
}

func (h *Handler) existingAgentSkillKeys(root, agentKey string) map[string]bool {
	used := map[string]bool{}
	if root == "" || agentKey == "" {
		return used
	}
	entries, err := os.ReadDir(filepath.Join(root, "agents", agentKey, "skills"))
	if err != nil {
		return used
	}
	for _, entry := range entries {
		if entry.IsDir() {
			used[entry.Name()] = true
		}
	}
	return used
}

func slugifyLegacyAgentSkillName(name string) string {
	var b strings.Builder
	lastSep := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		case r == '_' || r == '-' || r == '.':
			if b.Len() > 0 && !lastSep {
				b.WriteRune('_')
				lastSep = true
			}
		default:
			if b.Len() > 0 && !lastSep {
				b.WriteRune('_')
				lastSep = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "skill"
	}
	return out
}

func uniqueAgentSkillKey(base string, used map[string]bool) string {
	if base == "" {
		base = "skill"
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", base, i)
		if !used[candidate] {
			return candidate
		}
	}
}

func agentStableKey(agent *models.Agent) string {
	if agent == nil {
		return ""
	}
	if strings.TrimSpace(agent.Key) != "" {
		return strings.TrimSpace(agent.Key)
	}
	return strings.TrimSpace(agent.ID)
}

func (h *Handler) scopeForAgentSkillPath(c echo.Context, path string) string {
	return h.scopeForAgentSkillPathWithProjectRoot(path, h.currentProjectSkillRoot(c))
}

func (h *Handler) scopeForAgentSkillPathWithProjectRoot(path, projectRoot string) string {
	clean := filepath.Clean(path)
	if projectRoot != "" {
		if rel, err := filepath.Rel(filepath.Clean(projectRoot), clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "project"
		}
	}
	if h.agentSkillRoot != "" {
		if rel, err := filepath.Rel(filepath.Clean(h.agentSkillRoot), clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "global"
		}
	}
	return "project"
}

func validDialogSkillKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || key == "." || key == ".." || strings.Contains(key, "/") || strings.HasPrefix(key, ".") {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
		if i == 0 && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func firstDialogNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
