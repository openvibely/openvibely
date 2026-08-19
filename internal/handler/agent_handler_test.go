package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentplugins"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

type llmCallerFunc func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error)

func (f llmCallerFunc) CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	return f(ctx, prompt, attachments, agent, execID, workDir)
}

func TestHandler_ListAgents_DeleteConfirmationDialog(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	// Create a non-protected user agent so the working Delete button is rendered.
	// Protected agents (e.g. the Skill Curator seeded by migrations) show a
	// disabled "Delete (protected)" button instead of openDeleteAgentConfirm.
	userAgent := &models.Agent{
		Name:  "Test User Agent",
		Key:   "test_user_agent",
		Scope: models.AgentScopeGlobal,
		Model: "inherit",
		Tools: []string{},
	}
	if err := agentRepo.Create(t.Context(), userAgent); err != nil {
		t.Fatalf("create test agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="delete_agent_confirm_modal" class="modal"`,
		`id="delete_agent_confirm_name"`,
		`onclick="delete_agent_confirm_modal.close()"`,
		`onclick="confirmDeleteAgent()"`,
		`class="btn btn-error"`,
		`onclick="openDeleteAgentConfirm(this)"`,
		`modal.showModal()`,
		`async function confirmDeleteAgent()`,
		`fetch(withCurrentProject('/agents/' + encodeURIComponent(id))`,
		`method: 'DELETE'`,
		`function withCurrentProject(url)`,
		`function currentProjectQueryString()`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected agents delete confirmation markup/script to contain %q", want)
		}
	}
	if strings.Contains(body, `hx-confirm="Delete this agent`) || strings.Contains(body, `hx-delete="/agents/`) {
		t.Fatal("expected agent delete button to open modal instead of deleting immediately")
	}
}

func TestHandler_ListAgents_NewAgentModalPreservesCurrentProject(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	projectA := &models.Project{Name: "Stored Project A", RepoPath: t.TempDir()}
	if err := h.projectSvc.Create(t.Context(), projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	projectB := &models.Project{Name: "Viewed Project B", RepoPath: t.TempDir()}
	if err := h.projectSvc.Create(t.Context(), projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}
	if err := h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, projectA.ID); err != nil {
		t.Fatalf("set selected project preference: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id="+url.QueryEscape(projectB.ID), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`function withCurrentProject(url)`,
		`form.action = withCurrentProject('/agents');`,
		`form.setAttribute('hx-post', withCurrentProject('/agents'));`,
		`form.action = withCurrentProject('/agents/' + id);`,
		`fetch(withCurrentProject('/agents/' + encodeURIComponent(id))`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected New/Edit/Delete agent paths to preserve current project with %q", want)
		}
	}
	if strings.Contains(body, `form.action = '/agents';`) || strings.Contains(body, `form.setAttribute('hx-post', '/agents');`) {
		t.Fatal("expected New Agent form to avoid posting to /agents without current project context")
	}
}

func TestHandler_ListAgents_IncludesGenerateUI(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if strings.Contains(body, "Describe what this agent should do") {
		t.Errorf("expected agents page to remove duplicate top-level generate prompt section")
	}
	if strings.Contains(body, "id=\"agent_generate_prompt\"") {
		t.Errorf("expected agents page to use the description input as the single generate prompt field")
	}
	if !strings.Contains(body, "generateAgentFromPrompt()") {
		t.Errorf("expected agents page to include generate action handler")
	}
	if !strings.Contains(body, "Generated using local template draft") {
		t.Errorf("expected agents page fallback toast copy to describe local template draft")
	}
	if !strings.Contains(body, "'cancelled'") {
		t.Errorf("expected agents page fallback toast to use warning status, not failed")
	}
	if !strings.Contains(body, "const promptInput = document.getElementById('agent_description');") {
		t.Errorf("expected generate handler to read from the description input")
	}
	if !strings.Contains(body, "id=\"agent_generate_btn\"") {
		t.Errorf("expected agents page to include generate button beside description input")
	}
	if !strings.Contains(body, "id=\"agent_generated_summary\"") {
		t.Errorf("expected agents page to include generated summary status text")
	}
	if strings.Count(body, "<span class=\"label-text\">Description</span>") != 1 {
		t.Errorf("expected agent details modal to contain exactly one Description label")
	}
	if strings.Count(body, "id=\"agent_description\"") != 1 {
		t.Errorf("expected agent details modal to contain exactly one description input")
	}
	if !strings.Contains(body, "id=\"agent_description\" name=\"description\" class=\"input input-bordered flex-1\"") {
		t.Errorf("expected description input to share row layout with generate action")
	}
	if !strings.Contains(body, "data-agent-section-tab=\"details\"") {
		t.Errorf("expected agents page to include agent details top-level tab")
	}
	if !strings.Contains(body, "data-agent-section-tab=\"plugins\"") {
		t.Errorf("expected agents page to include plugins top-level tab")
	}
	if !strings.Contains(body, "data-agent-section-tab=\"marketplace\"") {
		t.Errorf("expected agents page to include marketplace top-level tab")
	}
	if !strings.Contains(body, "data-agent-section-panel=\"details\"") {
		t.Errorf("expected agents page to include agent details top-level panel")
	}
	if !strings.Contains(body, "data-agent-section-panel=\"plugins\"") {
		t.Errorf("expected agents page to include plugins top-level panel")
	}
	if !strings.Contains(body, "data-agent-section-panel=\"marketplace\"") {
		t.Errorf("expected agents page to include marketplace top-level panel")
	}
	if !strings.Contains(body, "setAgentSection('details')") {
		t.Errorf("expected agents page to default modal to details section")
	}
	if !strings.Contains(body, "let activeAgentSection = 'details'") {
		t.Errorf("expected agents page to track active top-level modal section")
	}
	if !strings.Contains(body, "function setAgentSection(sectionName)") {
		t.Errorf("expected agents page to include top-level section tab helper")
	}
	if !strings.Contains(body, "function setAgentModelSelection(value)") {
		t.Errorf("expected agents page to include model selection fallback helper")
	}
	if !strings.Contains(body, "class=\"flex flex-col h-[78vh]\"") {
		t.Errorf("expected agents page to include fixed-height modal content container")
	}
	if !strings.Contains(body, "id=\"agent_modal\" class=\"modal\" onclose=\"if (typeof syncToastContainerHost === 'function') syncToastContainerHost()\"") {
		t.Errorf("expected agents modal to resync toast host on close for top-layer stacking")
	}
	if !strings.Contains(body, "function getTopMostOpenModal()") {
		t.Errorf("expected base layout to include top-most modal detection for toast host placement")
	}
	if !strings.Contains(body, "function syncToastContainerHost()") {
		t.Errorf("expected base layout to include toast host resync helper for modal stacking")
	}
	if !strings.Contains(body, "return ensureToastContainer(modal);") {
		t.Errorf("expected toast system to host container inside the active modal when open")
	}
	if !strings.Contains(body, "sticky bottom-0") {
		t.Errorf("expected agents page to include sticky modal action footer")
	}
	if !strings.Contains(body, "class=\"tabs tabs-bordered tabs-sm w-full overflow-x-auto flex-nowrap\"") {
		t.Errorf("expected agents page tabs to use bordered app tab styling")
	}
	if !strings.Contains(body, "plugin_selected_count") {
		t.Errorf("expected agents page to include plugin selection state")
	}
	if !strings.Contains(body, "plugin_search_input") {
		t.Errorf("expected agents page to include plugin search input")
	}
	if !strings.Contains(body, "id=\"plugin_catalog_loading\"") {
		t.Errorf("expected agents page to include plugin catalog loading marker")
	}
	if !strings.Contains(body, "id=\"plugin_marketplace_list\"") {
		t.Errorf("expected agents page to include plugin marketplace list container")
	}
	if strings.Contains(body, "id=\"plugin_runtime_status\"") {
		t.Errorf("expected agents page to omit standalone plugin runtime status container")
	}
	if strings.Contains(body, "Plugin MCP Runtime") {
		t.Errorf("expected agents page to omit standalone plugin runtime section heading")
	}
	if strings.Contains(body, "Installed and available plugins across marketplaces.") {
		t.Errorf("expected agents page to omit marketplace helper copy")
	}
	if !strings.Contains(body, "No plugins selected") {
		t.Errorf("expected create flow to default to no selected plugins")
	}
	if strings.Contains(body, "agent_color") {
		t.Errorf("expected agents page to omit agent color controls")
	}
	if strings.Contains(body, "data-agent-color") {
		t.Errorf("expected agents cards to omit color dataset attributes")
	}
	if !strings.Contains(body, "function setPluginCatalogLoading(isLoading)") {
		t.Errorf("expected agents page to include plugin catalog loading toggle helper")
	}
	if !strings.Contains(body, "setPluginCatalogLoading(true)") {
		t.Errorf("expected agents page to show loading state during plugin state fetch")
	}
	if !strings.Contains(body, "let installingPluginIDs = new Set()") {
		t.Errorf("expected agents page to track install in-progress plugin ids")
	}
	if !strings.Contains(body, "if (installingPluginIDs.has(id) || uninstallingPluginIDs.has(id) || pluginCatalogLoading || hasActivePluginMutation()) return") {
		t.Errorf("expected agents page to prevent duplicate install clicks")
	}
	if !strings.Contains(body, "data-install-plugin-id") {
		t.Errorf("expected agents page install buttons to include install state hook")
	}
	if !strings.Contains(body, "Installing...") {
		t.Errorf("expected agents page to include install in-progress button text")
	}
	if !strings.Contains(body, "let activeAgentID = ''") {
		t.Errorf("expected agents page to track current agent id for plugin auto-enable")
	}
	if !strings.Contains(body, "body.agent_id = currentAgentID") {
		t.Errorf("expected plugin install requests to include agent_id for edit flow")
	}
	if !strings.Contains(body, "Plugin installed and selected for this new agent") {
		t.Errorf("expected create flow install success copy to mention auto-selection")
	}
	if !strings.Contains(body, "Plugin installed and enabled for this agent") {
		t.Errorf("expected edit flow install success copy to mention auto-enable")
	}
	if !strings.Contains(body, "if (!response.ok)") || !strings.Contains(body, "readErrorMessage(response, 'install plugin failed')") {
		t.Errorf("expected install failure path to surface target install errors")
	}
	if !strings.Contains(body, "Plugin installed, but enabling for this agent failed. Retry install to try enabling again.") {
		t.Errorf("expected edit flow enable-failure helper text")
	}
	if !strings.Contains(body, "id=\"plugin_marketplace_add_btn\"") {
		t.Errorf("expected agents page to include marketplace add button id for loading state")
	}
	if !strings.Contains(body, "id=\"plugin_marketplace_action_status\"") {
		t.Errorf("expected agents page to include marketplace action status message container")
	}
	if !strings.Contains(body, "let addingMarketplace = false") {
		t.Errorf("expected agents page to track marketplace add in-progress state")
	}
	if !strings.Contains(body, "let removingMarketplaceNames = new Set()") {
		t.Errorf("expected agents page to track per-marketplace remove in-progress state")
	}
	if !strings.Contains(body, "let syncingMarketplaceNames = new Set()") {
		t.Errorf("expected agents page to track per-marketplace sync in-progress state")
	}
	if !strings.Contains(body, "let restoringDefaultMarketplaces = false") {
		t.Errorf("expected agents page to track marketplace restore defaults in-progress state")
	}
	if !strings.Contains(body, "function hasActiveMarketplaceAction()") {
		t.Errorf("expected agents page to include shared marketplace action guard")
	}
	if !strings.Contains(body, "function hasActivePluginMutation()") {
		t.Errorf("expected agents page to include shared plugin mutation guard")
	}
	if !strings.Contains(body, "addingMarketplace || restoringDefaultMarketplaces || syncingMarketplaceNames.size > 0 || removingMarketplaceNames.size > 0") {
		t.Errorf("expected agents page to block conflicting marketplace actions while requests are in flight")
	}
	if !strings.Contains(body, "const anyMarketplaceAction = hasActiveMarketplaceAction()") {
		t.Errorf("expected agents page to derive shared marketplace action state for button disabling")
	}
	if !strings.Contains(body, "const source = mp.url || mp.source || mp.repo || ''") {
		t.Errorf("expected marketplace cards to prefer full URL/source display over repo shorthand")
	}
	if !strings.Contains(body, "if (hasActiveMarketplaceAction()) return") {
		t.Errorf("expected agents page to prevent duplicate sync/remove/restore clicks")
	}
	if strings.Contains(body, "id=\"plugin_marketplace_refresh_btn\"") {
		t.Errorf("expected agents page to remove marketplace refresh button")
	}
	if !strings.Contains(body, "id=\"plugin_marketplace_restore_btn\"") {
		t.Errorf("expected agents page to include marketplace restore defaults button id for loading state")
	}
	if !strings.Contains(body, "data-sync-marketplace-name") {
		t.Errorf("expected agents page marketplace rows to include sync action hook")
	}
	if !strings.Contains(body, "class=\"btn btn-ghost btn-xs btn-square\"") {
		t.Errorf("expected agents page marketplace sync action to render as compact icon button")
	}
	if !strings.Contains(body, "class=\"btn btn-ghost btn-xs btn-square text-error\"") {
		t.Errorf("expected agents page marketplace remove action to render as compact icon button")
	}
	if !strings.Contains(body, "aria-label=\"${syncLabel}\"") {
		t.Errorf("expected agents page marketplace sync icon action to include accessible label")
	}
	if !strings.Contains(body, "aria-label=\"${removeLabel}\"") {
		t.Errorf("expected agents page marketplace remove icon action to include accessible label")
	}
	if !strings.Contains(body, "title=\"${syncLabel}\"") {
		t.Errorf("expected agents page marketplace sync icon action to include tooltip")
	}
	if !strings.Contains(body, "title=\"${removeLabel}\"") {
		t.Errorf("expected agents page marketplace remove icon action to include tooltip")
	}
	if !strings.Contains(body, "<span class=\"loading loading-spinner loading-xs\"></span>") {
		t.Errorf("expected agents page marketplace icon actions to keep loading spinner state")
	}
	if !strings.Contains(body, "Adding...") {
		t.Errorf("expected agents page to include marketplace add in-progress button text")
	}
	if strings.Contains(body, "Syncing...") {
		t.Errorf("expected agents page marketplace sync action to use icon-only loading state")
	}
	if strings.Contains(body, "Removing...") {
		t.Errorf("expected agents page marketplace remove action to use icon-only loading state")
	}
	if !strings.Contains(body, "Restoring...") {
		t.Errorf("expected agents page to include marketplace restore defaults in-progress button text")
	}
	if !strings.Contains(body, "if (uninstallingPluginIDs.has(id) || installingPluginIDs.has(id) || pluginCatalogLoading || hasActivePluginMutation()) return") {
		t.Errorf("expected agents page to prevent duplicate plugin uninstall clicks")
	}
	if !strings.Contains(body, "let pendingUninstalledPluginIDs = new Set()") {
		t.Errorf("expected agents page to track pending uninstalls to suppress stale state reinserts")
	}
	if !strings.Contains(body, "pluginCatalogLoading = Boolean(isLoading)") {
		t.Errorf("expected agents page to track plugin catalog loading state")
	}
	if !strings.Contains(body, "let pluginStateRequestToken = 0") {
		t.Errorf("expected agents page to track plugin state request sequencing token")
	}
	if !strings.Contains(body, "let pluginStateLoadInFlight = 0") {
		t.Errorf("expected agents page to track concurrent plugin state load count")
	}
	if !strings.Contains(body, "class=\"toggle toggle-sm toggle-primary agent-plugin-checkbox\"") {
		t.Errorf("expected installed plugin rows to use toggle switch controls")
	}
	if !strings.Contains(body, "aria-label=\"Uninstall ${escapeHtml(item.id)}\"") {
		t.Errorf("expected installed plugin rows to use icon-only uninstall action with accessible label")
	}
	if !strings.Contains(body, "controlsDisabled = isUninstalling || isInstalling || pluginCatalogLoading || pluginMutationInFlight") {
		t.Errorf("expected installed plugin controls to disable during loading/install/uninstall")
	}
	if !strings.Contains(body, "disabled aria-disabled=\"true\"") {
		t.Errorf("expected installed toggle controls to expose disabled state")
	}
	if !strings.Contains(body, "<div class=\"flex items-center gap-2 shrink-0\">") {
		t.Errorf("expected installed plugin rows to keep toggle and uninstall actions grouped at row end")
	}
	if !strings.Contains(body, "aria-label=\"Enable ${escapeHtml(item.id)}\"") {
		t.Errorf("expected installed plugin rows to keep accessible toggle labeling")
	}
	if !strings.Contains(body, "function buildRuntimeStatusLookup()") {
		t.Errorf("expected agents page to build runtime status lookup from plugin state runtime entries")
	}
	if !strings.Contains(body, "item.plugin_id") {
		t.Errorf("expected runtime status lookup to index by plugin_id for MCP server name mismatch")
	}
	if !strings.Contains(body, "const runtimePluginKey = String(item.id || '').trim().toLowerCase()") {
		t.Errorf("expected installed plugin rows to look up runtime by full plugin ID")
	}
	if !strings.Contains(body, "runtimeStatusLookup.get(runtimePluginKey) || runtimeStatusLookup.get(runtimeNameKey)") {
		t.Errorf("expected installed plugin rows to fall back to name prefix for runtime lookup")
	}
	if !strings.Contains(body, "if (runtimeStatus === 'running') runtimeDotClass = 'bg-success/80'") {
		t.Errorf("expected installed plugin rows to map running runtime status to green indicator")
	}
	if !strings.Contains(body, "if (runtimeStatus === 'failed') runtimeDotClass = 'bg-error/90'") {
		t.Errorf("expected installed plugin rows to map failed runtime status to red indicator")
	}
	if !strings.Contains(body, "if (requestToken !== pluginStateRequestToken)") {
		t.Errorf("expected plugin state loads to ignore stale request responses")
	}
	if !strings.Contains(body, "const suppressDiscoveryWarningToast = options && options.suppressDiscoveryWarningToast === true") {
		t.Errorf("expected plugin state loader to support suppressing discovery warning toasts for action-scoped flows")
	}
	if !strings.Contains(body, "if (showToasts && !suppressDiscoveryWarningToast && agentPluginState.error && window.showToast)") {
		t.Errorf("expected plugin state loader to gate discovery warning toasts by suppression flag")
	}
	if !strings.Contains(body, "pluginStateLoadInFlight = Math.max(0, pluginStateLoadInFlight - 1)") {
		t.Errorf("expected plugin state loads to clear loading state only when all requests complete")
	}
	if !strings.Contains(body, "const runtimeErrText = runtimeStatus === 'failed' && runtimeError") {
		t.Errorf("expected failed runtime status to generate error text for display")
	}
	if !strings.Contains(body, "'<p class=\"text-[11px] text-error mt-1\">Runtime: ' + escapeHtml(runtimeError) + '</p>'") {
		t.Errorf("expected failed runtime error message to be displayed below plugin name")
	}
	if !strings.Contains(body, "data-uninstall-plugin-id") {
		t.Errorf("expected agents page to include uninstall button loading state hook")
	}
	if !strings.Contains(body, "Could not remove marketplace") {
		t.Errorf("expected agents page marketplace errors to stay visible inline")
	}
	if !strings.Contains(body, "Install and enable for this agent in one click.") {
		t.Errorf("expected updated available-plugin helper copy for one-click install+enable")
	}
	if !strings.Contains(body, "agentPluginState.installed = Array.isArray(agentPluginState.installed)") {
		t.Errorf("expected install/uninstall flows to mutate local installed plugin state before refresh")
	}
	if !strings.Contains(body, "agentPluginState.available = Array.isArray(agentPluginState.available)") {
		t.Errorf("expected install/uninstall flows to mutate local available plugin state before refresh")
	}
	if !strings.Contains(body, "await loadPluginState(true, { throwOnError: true, suppressDiscoveryWarningToast: true });") {
		t.Errorf("expected install refresh to suppress unrelated discovery warning toasts")
	}
	if !strings.Contains(body, "await loadPluginState(false, { throwOnError: true });") {
		t.Errorf("expected uninstall refresh to avoid global discovery warning toasts")
	}
	if !strings.Contains(body, "pendingUninstalledPluginIDs.add(id)") {
		t.Errorf("expected uninstall flow to mark plugin id pending removal")
	}
	if !strings.Contains(body, "pendingUninstalledPluginIDs.delete(id)") {
		t.Errorf("expected uninstall flow to clear pending removal marker")
	}
	if !strings.Contains(body, "agentPluginState.installed.filter(p => !pendingUninstalledPluginIDs.has(normalizePluginID(p)))") {
		t.Errorf("expected plugin state refresh to suppress stale pending uninstalled plugins")
	}
}

func TestHandler_AgentsPage_IncludesRealtimeScopedDirectoryValidation(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `value="send_message"`) {
		t.Fatalf("expected agent tools dialog to include send_message checkbox")
	}
	if !strings.Contains(body, "id=\"scoped_files_directory_error\"") {
		t.Fatalf("expected scoped directory inline error element")
	}
	if !strings.Contains(body, "oninput=\"validateScopedFilesDirectoryInput()\"") {
		t.Fatalf("expected Directory field to validate while typing")
	}
	if !strings.Contains(body, "function getScopedFilesDirectoryError(value)") {
		t.Fatalf("expected client-side scoped directory validation helper")
	}
	if !strings.Contains(body, "addEventListener('submit', (event)") {
		t.Fatalf("expected submit handler to block invalid scoped directory before HTMX save")
	}
}

func TestHandler_CreateAgent_RejectsUnsafeScopedFilesDirectory(t *testing.T) {
	unsafeDirectories := []string{"../secrets", `..\\secrets`, "/tmp/secrets", "~/secrets", "C:/secrets"}
	for _, directory := range unsafeDirectories {
		t.Run(directory, func(t *testing.T) {
			h, e, _, db := setupTestHandlerWithDB(t)
			h.SetAgentRepo(repository.NewAgentRepo(db))

			config, err := json.Marshal(models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: directory, Permissions: []string{"read"}}}})
			if err != nil {
				t.Fatalf("marshal tool config: %v", err)
			}

			form := url.Values{}
			form.Set("name", "agent-a")
			form.Set("description", "first agent")
			form.Set("system_prompt", "do work")
			form.Set("model", "inherit")
			form.Set("tools_json", `["ScopedFiles"]`)
			form.Set("tool_config_json", string(config))
			form.Set("plugins_json", `[]`)
			form.Set("skills_json", `[]`)
			form.Set("mcp_servers_json", `[]`)

			req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), "project-relative directory") {
				t.Fatalf("expected scoped directory validation error, got %s", rec.Body.String())
			}
		})
	}
}

func TestHandler_GenerateAgent_FallbackWithoutLLM(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	h.llmSvc = nil // Force deterministic fallback generation path.

	form := url.Values{}
	form.Set("description", "Review pull requests and suggest safe fixes")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		Name            string                   `json:"name"`
		Description     string                   `json:"description"`
		SystemPrompt    string                   `json:"system_prompt"`
		Model           string                   `json:"model"`
		Tools           []string                 `json:"tools"`
		Skills          []models.SkillConfig     `json:"skills"`
		MCPServers      []models.MCPServerConfig `json:"mcp_servers"`
		GenerationMode  string                   `json:"generation_mode"`
		GenerationError string                   `json:"generation_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if strings.TrimSpace(generated.Name) == "" {
		t.Fatalf("expected generated name to be set")
	}
	if strings.TrimSpace(generated.SystemPrompt) == "" {
		t.Fatalf("expected generated system prompt to be set")
	}
	if generated.Model == "" {
		t.Fatalf("expected generated model to be set")
	}
	if len(generated.Tools) == 0 {
		t.Fatalf("expected generated tools to be set")
	}
	if len(generated.Skills) == 0 {
		t.Fatalf("expected generated skills to be set")
	}
	if generated.GenerationMode != "fallback" {
		t.Fatalf("expected fallback generation mode, got %q", generated.GenerationMode)
	}
	if strings.TrimSpace(generated.GenerationError) == "" {
		t.Fatalf("expected generation error reason for fallback mode")
	}
}

func TestHandler_GenerateAgent_FallbackUIUXProducesStructuredSkills(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	h.llmSvc = nil

	form := url.Values{}
	form.Set("description", "Expert UI and UX engineer that reviews components with Playwright screenshots and accessibility checks")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		Tools  []string             `json:"tools"`
		Skills []models.SkillConfig `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if len(generated.Skills) < 3 {
		t.Fatalf("expected at least 3 fallback skills for UI/UX prompt, got %d", len(generated.Skills))
	}
	if !strings.Contains(strings.ToLower(generated.Skills[1].Content), "accessibility") {
		t.Fatalf("expected accessibility guidance in generated skill content")
	}
	joinedTools := strings.ToLower(strings.Join(generated.Tools, ","))
	if !strings.Contains(joinedTools, "webfetch") || !strings.Contains(joinedTools, "websearch") {
		t.Fatalf("expected UI/UX fallback to include web tools, got %v", generated.Tools)
	}
}

func TestHandler_GenerateAgent_UsesOnlyPinnedDefaultModel(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	defaultCfg := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})
	secondaryCfg := &models.LLMConfig{
		Name:      "Secondary Generator",
		Provider:  models.ProviderTest,
		Model:     "secondary-model",
		MaxTokens: 4096,
		IsDefault: false,
	}
	if err := llmConfigRepo.Create(context.Background(), secondaryCfg); err != nil {
		t.Fatalf("create secondary model: %v", err)
	}

	mock := testutil.NewMockLLMCaller()
	mock.Err = errors.New("forced failure")
	h.llmSvc.SetLLMCaller(mock)

	form := url.Values{}
	form.Set("description", "Generate a coding agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if mock.CallCount() != 1 {
		t.Fatalf("expected exactly one generation call, got %d", mock.CallCount())
	}
	if generated.GenerationMode != "fallback" {
		t.Fatalf("expected fallback generation mode, got %q", generated.GenerationMode)
	}
	if !strings.Contains(generated.GenerationError, defaultCfg.Name) {
		t.Fatalf("expected generation error to reference pinned default model %q, got %q", defaultCfg.Name, generated.GenerationError)
	}
	if strings.Contains(generated.GenerationError, "||") {
		t.Fatalf("expected single-model generation error, got %q", generated.GenerationError)
	}
}

func TestHandler_GenerateAgent_RetriesTransientTimeoutThenSucceeds(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	_ = createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})

	mock := testutil.NewMockLLMCaller()
	mock.Response = `{"name":"Retry Agent","description":"test","system_prompt":"do work","model":"inherit","color":"cyan","tools":["Read"],"skills":[{"name":"s","description":"d","tools":"Read","content":"c"}]}`
	calls := 0
	mock.Err = nil
	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		calls++
		if calls == 1 {
			return "", "", 0, fmt.Errorf("openai API call: context deadline exceeded")
		}
		return mock.Response, mock.Response, 0, nil
	}))

	form := url.Values{}
	form.Set("description", "Generate a coding agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
		Name            string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected exactly two calls (retry path), got %d", calls)
	}
	if generated.GenerationMode != "llm" {
		t.Fatalf("expected llm generation mode after retry success, got %q", generated.GenerationMode)
	}
	if generated.GenerationError != "" {
		t.Fatalf("expected empty generation error on successful retry, got %q", generated.GenerationError)
	}
	if generated.Name != "Retry Agent" {
		t.Fatalf("expected LLM payload to be used, got %q", generated.Name)
	}
}

func TestHandler_GenerateAgent_TimeoutUsesClearFallbackError(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	defaultCfg := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})

	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		return "", "", 0, fmt.Errorf("openai API call: context deadline exceeded")
	}))

	form := url.Values{}
	form.Set("description", "Generate a coding agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if generated.GenerationMode != "fallback" {
		t.Fatalf("expected fallback generation mode on timeout, got %q", generated.GenerationMode)
	}
	if !strings.Contains(generated.GenerationError, defaultCfg.Name) {
		t.Fatalf("expected generation error to reference model %q, got %q", defaultCfg.Name, generated.GenerationError)
	}
	if !strings.Contains(strings.ToLower(generated.GenerationError), "timed out") {
		t.Fatalf("expected timeout-specific fallback message, got %q", generated.GenerationError)
	}
}

func TestHandler_GenerateAgent_RepairsMalformedJSONResponse(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	_ = createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})

	calls := 0
	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		calls++
		if calls == 1 {
			return "Update: here's a draft\n```json\n{\"name\":\"Broken\",\"description\":\"desc\",\"system_prompt\":\"bad\",\"model\":\"inherit\",\"tools\":[\"Read\"],}\n```", "", 0, nil
		}
		if !strings.Contains(prompt, "Rewrite it into strict JSON") {
			return "", "", 0, fmt.Errorf("expected repair prompt, got: %s", prompt)
		}
		return "{\"name\":\"Recovered Agent\",\"description\":\"desc\",\"system_prompt\":\"Do recovered work\",\"model\":\"inherit\",\"tools\":[\"Read\",\"Bash\"],\"skills\":[{\"name\":\"plan\",\"description\":\"d\",\"tools\":\"Read\",\"content\":\"c\"}],\"mcp_servers\":[]}", "", 0, nil
	}))

	form := url.Values{}
	form.Set("description", "Generate a coding agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
		Name            string `json:"name"`
		SystemPrompt    string `json:"system_prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected model + repair call, got %d", calls)
	}
	if generated.GenerationMode != "llm" {
		t.Fatalf("expected llm generation after successful repair, got %q", generated.GenerationMode)
	}
	if generated.GenerationError != "" {
		t.Fatalf("expected empty generation_error after successful repair, got %q", generated.GenerationError)
	}
	if generated.Name != "Recovered Agent" {
		t.Fatalf("expected repaired response payload, got %q", generated.Name)
	}
	if strings.TrimSpace(generated.SystemPrompt) == "" {
		t.Fatalf("expected repaired system prompt to be preserved")
	}
}

func TestHandler_GenerateAgent_IgnoresToolWrapperPrefixAndParsesJSON(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	_ = createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})

	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		return "\n[Using tool: ui-ux-playwright-reviewer]\n{\"name\":\"Recovered Agent\",\"description\":\"desc\",\"system_prompt\":\"Do recovered work\",\"model\":\"inherit\",\"tools\":[\"Read\",\"WebFetch\"],\"skills\":[{\"name\":\"plan\",\"description\":\"d\",\"tools\":\"Read\",\"content\":\"c\"}],\"mcp_servers\":[]}", "", 0, nil
	}))

	form := url.Values{}
	form.Set("description", "Generate a UI review agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
		Name            string `json:"name"`
		SystemPrompt    string `json:"system_prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if generated.GenerationMode != "llm" {
		t.Fatalf("expected llm generation mode, got %q", generated.GenerationMode)
	}
	if generated.GenerationError != "" {
		t.Fatalf("expected empty generation_error, got %q", generated.GenerationError)
	}
	if generated.Name != "Recovered Agent" {
		t.Fatalf("expected parsed payload from JSON after tool wrapper, got %q", generated.Name)
	}
	if strings.TrimSpace(generated.SystemPrompt) == "" {
		t.Fatalf("expected system prompt in parsed payload")
	}
}

func TestHandler_GenerateAgent_PluginToolOnlyOutputSucceedsOnFirstAttempt(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	_ = createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Codex Generator"
		a.Provider = models.ProviderTest
		a.Model = "gpt-5.3-codex"
		a.IsDefault = true
	})

	// Simulate the bug: first call returns valid JSON (no tool wrapper output).
	// Before the fix, the prompt included MCP tool names which caused models to
	// return tool-call output like "\n[Using tool: playwright-ui-ux-reviewer]\n"
	// on the first attempt, wasting a retry. With the fix, MCP tool names are
	// excluded from the prompt so the model generates JSON directly.
	calls := 0
	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		calls++
		// Verify the prompt does NOT contain MCP tool names or skill names
		if strings.Contains(prompt, "playwright-ui-ux-reviewer") {
			t.Errorf("generation prompt should not contain MCP tool names, but found playwright-ui-ux-reviewer")
		}
		if strings.Contains(prompt, "Introspected MCP tool names") {
			t.Errorf("generation prompt should not contain MCP tool names section")
		}
		if strings.Contains(prompt, "playwright-audit") {
			t.Errorf("generation prompt should not contain skill names — they trigger tool call hallucination")
		}
		if strings.Contains(prompt, "Plugin-derived skills") {
			t.Errorf("generation prompt should not contain skill hints section")
		}
		return `{"name":"Playwright Reviewer","description":"Reviews UI","system_prompt":"You review UI components using Playwright.","model":"inherit","tools":["Read","Bash"],"skills":[{"name":"review","description":"UI review","tools":"Read,Bash","content":"Review components"}],"mcp_servers":[]}`, "", 0, nil
	}))

	origDiscover := discoverPluginStateFn
	defer func() { discoverPluginStateFn = origDiscover }()
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{
			Installed: []models.InstalledPlugin{{ID: "playwright@claude-plugins-official", Enabled: true}},
		}, nil
	}

	originalResolve := resolvePluginBundleFn
	defer func() { resolvePluginBundleFn = originalResolve }()
	resolveCalls := 0
	resolvePluginBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		resolveCalls++
		return nil, nil
	}

	form := url.Values{}
	form.Set("description", "Review UI components with Playwright")
	form.Set("plugins_json", `["playwright@claude-plugins-official"]`)

	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
		Name            string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if generated.GenerationMode != "llm" {
		t.Fatalf("expected llm generation mode (first attempt success), got %q", generated.GenerationMode)
	}
	if generated.GenerationError != "" {
		t.Fatalf("expected no generation error, got %q", generated.GenerationError)
	}
	if generated.Name != "Playwright Reviewer" {
		t.Fatalf("expected parsed name from JSON, got %q", generated.Name)
	}
	// Should succeed on first attempt (1 call), not require retry
	if calls != 1 {
		t.Fatalf("expected exactly 1 LLM call (first attempt success), got %d", calls)
	}
	if resolveCalls != 0 {
		t.Fatalf("expected generation to skip plugin runtime resolution, got %d resolve calls", resolveCalls)
	}
}

func TestBuildAgentGenerationPrompt_DisallowsToolExecutionDuringGeneration(t *testing.T) {
	prompt := buildAgentGenerationPrompt("review React UI")
	if !strings.Contains(prompt, "Treat this as pure text-in/JSON-out generation") {
		t.Fatalf("expected prompt guardrail to enforce pure JSON generation mode")
	}
	if !strings.Contains(prompt, "Do not use or invoke tools, plugins, MCP servers, or shell commands") {
		t.Fatalf("expected prompt guardrail to forbid runtime tool execution during generation")
	}
}

func TestBuildAgentGenerationPromptsAvoidProductNameInjection(t *testing.T) {
	for name, prompt := range map[string]string{
		"generate": buildAgentGenerationPrompt("review React UI"),
		"repair":   buildGenerateAgentRepairPrompt("not json"),
	} {
		if strings.Contains(prompt, "OpenVibely agent definition") || strings.Contains(prompt, "Generate an OpenVibely") {
			t.Fatalf("%s prompt should use neutral agent-definition wording:\n%s", name, prompt)
		}
		if !strings.Contains(prompt, "agent definition") {
			t.Fatalf("%s prompt should still describe the schema target:\n%s", name, prompt)
		}
	}
}

func TestHandler_GenerateAgent_MalformedJSONFallsBackWithClearError(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	defaultCfg := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})

	calls := 0
	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		calls++
		if calls == 1 {
			return "Update: I will now provide JSON\n{\"name\":\"Broken\",\"description\":\"desc\",\"system_prompt\":\"bad\",\"model\":\"inherit\",\"tools\":[\"Read\"],}", "", 0, nil
		}
		return "{\"still\":\"bad\",}", "", 0, nil
	}))

	form := url.Values{}
	form.Set("description", "Generate a coding agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
		SystemPrompt    string `json:"system_prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if calls != 4 {
		t.Fatalf("expected two attempts each with repair call, got %d", calls)
	}
	if generated.GenerationMode != "fallback" {
		t.Fatalf("expected fallback mode when malformed output cannot be repaired, got %q", generated.GenerationMode)
	}
	if !strings.Contains(generated.GenerationError, defaultCfg.Name) {
		t.Fatalf("expected generation error to reference model %q, got %q", defaultCfg.Name, generated.GenerationError)
	}
	if !strings.Contains(strings.ToLower(generated.GenerationError), "malformed") || !strings.Contains(strings.ToLower(generated.GenerationError), "local template") {
		t.Fatalf("expected malformed-output fallback guidance, got %q", generated.GenerationError)
	}
	if strings.TrimSpace(generated.SystemPrompt) == "" {
		t.Fatalf("expected fallback response to still provide usable system prompt")
	}
}

func TestHandler_GenerateAgent_RecoversFromProviderMalformedJSONError(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	_ = createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})

	providerErr := "GPT 5.3 Codex: invalid JSON from model GPT 5.3 Codex: invalid character 'U' looking for beginning of value"
	calls := 0
	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		calls++
		if calls == 1 {
			return "", "", 0, errors.New(providerErr)
		}
		if !strings.Contains(prompt, "Rewrite it into strict JSON") {
			return "", "", 0, fmt.Errorf("expected repair prompt for provider malformed output, got: %s", prompt)
		}
		if !strings.Contains(prompt, "invalid JSON from model") {
			return "", "", 0, fmt.Errorf("expected repair prompt to include provider malformed error, got: %s", prompt)
		}
		return "{\"name\":\"Recovered Agent\",\"description\":\"desc\",\"system_prompt\":\"Do recovered work\",\"model\":\"inherit\",\"tools\":[\"Read\",\"Bash\"],\"skills\":[{\"name\":\"plan\",\"description\":\"d\",\"tools\":\"Read\",\"content\":\"c\"}],\"mcp_servers\":[]}", "", 0, nil
	}))

	form := url.Values{}
	form.Set("description", "Generate a coding agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
		Name            string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected provider call + repair call, got %d", calls)
	}
	if generated.GenerationMode != "llm" {
		t.Fatalf("expected llm generation mode after provider malformed recovery, got %q", generated.GenerationMode)
	}
	if generated.GenerationError != "" {
		t.Fatalf("expected empty generation_error after successful recovery, got %q", generated.GenerationError)
	}
	if generated.Name != "Recovered Agent" {
		t.Fatalf("expected repaired payload name, got %q", generated.Name)
	}
}

func TestHandler_GenerateAgent_ProviderMalformedJSONFallsBackWithActionableError(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	defaultCfg := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Pinned Generator"
		a.Provider = models.ProviderTest
		a.Model = "pinned-model"
		a.IsDefault = true
	})

	providerErr := "GPT 5.3 Codex: invalid JSON from model GPT 5.3 Codex: invalid character 'U' looking for beginning of value"
	calls := 0
	h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
		calls++
		switch calls {
		case 1:
			return "", "", 0, errors.New(providerErr)
		case 2:
			if !strings.Contains(prompt, "Rewrite it into strict JSON") {
				return "", "", 0, fmt.Errorf("expected repair prompt on first malformed provider error")
			}
			return "{\"still\":\"bad\",}", "", 0, nil
		case 3:
			if !strings.Contains(prompt, "IMPORTANT RETRY INSTRUCTION") {
				return "", "", 0, fmt.Errorf("expected strict retry prompt after malformed provider error")
			}
			return "", "", 0, errors.New(providerErr)
		default:
			return "{\"still\":\"bad\",}", "", 0, nil
		}
	}))

	form := url.Values{}
	form.Set("description", "Generate a coding agent")
	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		GenerationMode  string `json:"generation_mode"`
		GenerationError string `json:"generation_error"`
		SystemPrompt    string `json:"system_prompt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode json response: %v", err)
	}

	if calls != 4 {
		t.Fatalf("expected two malformed attempts each with repair call, got %d", calls)
	}
	if generated.GenerationMode != "fallback" {
		t.Fatalf("expected fallback mode when provider malformed output cannot be repaired, got %q", generated.GenerationMode)
	}
	if !strings.Contains(generated.GenerationError, defaultCfg.Name) {
		t.Fatalf("expected generation error to reference model %q, got %q", defaultCfg.Name, generated.GenerationError)
	}
	if !strings.Contains(strings.ToLower(generated.GenerationError), "try generate again") {
		t.Fatalf("expected actionable retry guidance in generation error, got %q", generated.GenerationError)
	}
	if strings.Contains(strings.ToLower(generated.GenerationError), "invalid character") {
		t.Fatalf("expected generation error to avoid raw parser internals, got %q", generated.GenerationError)
	}
	if strings.TrimSpace(generated.SystemPrompt) == "" {
		t.Fatalf("expected fallback response to still provide usable system prompt")
	}
}

func TestBuildAgentGenerationPrompt_NoPlugins(t *testing.T) {
	prompt := buildAgentGenerationPrompt("review React UI")
	if strings.Contains(prompt, "No plugins are selected") {
		t.Fatalf("generation prompt should not mention plugin state")
	}
	if strings.Contains(prompt, "Selected plugin") {
		t.Fatalf("generation prompt should not include selected plugin sections")
	}
	if strings.Contains(prompt, `"color"`) {
		t.Fatalf("did not expect color key in generation schema prompt")
	}
}

func TestBuildAgentGenerationPrompt_ExcludesPluginAndToolCatalogContext(t *testing.T) {
	prompt := buildAgentGenerationPrompt("review React UI")
	if strings.Contains(prompt, "playwright@claude-plugins-official") {
		t.Fatalf("generation prompt must not include plugin IDs")
	}
	if strings.Contains(prompt, "playwright__browser_take_screenshot") {
		t.Fatalf("generation prompt must not include MCP tool names")
	}
	if strings.Contains(prompt, "playwright-ui-ux-reviewer") {
		t.Fatalf("generation prompt must not include plugin tool names")
	}
	if strings.Contains(prompt, "Plugin-derived skills") {
		t.Fatalf("generation prompt must not include plugin skill hints")
	}
	if strings.Contains(prompt, "Introspected MCP tool names") {
		t.Fatalf("generation prompt must not include MCP tool name sections")
	}
	if !strings.Contains(prompt, "Do not reference plugin installation state, plugin IDs, or tool catalogs") {
		t.Fatalf("generation prompt should explicitly forbid plugin/tool catalog context")
	}
}

func TestHandler_GenerateAgent_FallbackIncludesSelectedPlugins(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	h.llmSvc = nil

	origDiscover := discoverPluginStateFn
	defer func() { discoverPluginStateFn = origDiscover }()
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{
			Installed: []models.InstalledPlugin{{ID: "playwright@claude-plugins-official", Enabled: true}},
		}, nil
	}

	originalResolve := resolvePluginBundleFn
	defer func() { resolvePluginBundleFn = originalResolve }()
	resolveCalls := 0
	resolvePluginBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		resolveCalls++
		return nil, nil
	}

	form := url.Values{}
	form.Set("description", "Review UI components with Playwright")
	form.Set("plugins_json", `["playwright@claude-plugins-official"]`)

	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var generated struct {
		Plugins        []string             `json:"plugins"`
		Skills         []models.SkillConfig `json:"skills"`
		GenerationMode string               `json:"generation_mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(generated.Plugins) != 1 || generated.Plugins[0] != "playwright@claude-plugins-official" {
		t.Fatalf("expected selected plugin id in response, got %v", generated.Plugins)
	}
	if len(generated.Skills) == 0 {
		t.Fatalf("expected generated skills in fallback response")
	}
	if generated.GenerationMode != "fallback" {
		t.Fatalf("expected fallback mode, got %q", generated.GenerationMode)
	}
	if resolveCalls != 0 {
		t.Fatalf("expected fallback generation to skip plugin runtime resolution, got %d resolve calls", resolveCalls)
	}
}

func TestHandler_CreateAgent_DefaultsPluginsOffWhenNotSelected(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	if err := llmConfigRepo.Create(context.Background(), &models.LLMConfig{
		Name:       "GPT 5.4",
		Provider:   models.ProviderOpenAI,
		Model:      "gpt-5.4",
		MaxTokens:  4096,
		IsDefault:  false,
		AuthMethod: models.AuthMethodAPIKey,
	}); err != nil {
		t.Fatalf("create openai model: %v", err)
	}

	origDiscover := discoverPluginStateFn
	defer func() { discoverPluginStateFn = origDiscover }()
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{
			Installed: []models.InstalledPlugin{{ID: "playwright@claude-plugins-official", Enabled: true}},
		}, nil
	}

	form := url.Values{}
	form.Set("name", "agent-a")
	form.Set("description", "first agent")
	form.Set("system_prompt", "do work")
	form.Set("model", "gpt-5.4")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)

	req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	agents, err := agentRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	// Filter out built-in system agents seeded by migration 078 so this test
	// continues to assert against the user-created agent only.
	user := []models.Agent{}
	for _, a := range agents {
		if a.SystemKind == "" {
			user = append(user, a)
		}
	}
	if len(user) != 1 {
		t.Fatalf("expected one user-created agent, got %d (total=%d)", len(user), len(agents))
	}
	if user[0].Model != "gpt-5.4" {
		t.Fatalf("expected configured OpenAI model override to persist, got %q", user[0].Model)
	}
	if len(user[0].Plugins) != 0 {
		t.Fatalf("expected no default plugins enabled, got %v", user[0].Plugins)
	}
}

func TestHandler_CreateAgent_PersistsSendMessageToolGrant(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	form := url.Values{}
	form.Set("name", "channel-agent")
	form.Set("description", "send outbound updates")
	form.Set("system_prompt", "send updates when asked")
	form.Set("model", "inherit")
	form.Set("tools_json", `["send_message"]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)

	req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	agents, err := agentRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	for _, agent := range agents {
		if agent.SystemKind == "" && agent.Name == "channel-agent" {
			if !agentToolsInclude(agent.Tools, "send_message") {
				t.Fatalf("stored agent tools = %#v, missing send_message", agent.Tools)
			}
			return
		}
	}
	t.Fatalf("created channel-agent not found in %#v", agents)
}

func TestHandler_CreateAgent_RejectsUninstalledPluginSelection(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	origDiscover := discoverPluginStateFn
	defer func() { discoverPluginStateFn = origDiscover }()
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{Installed: []models.InstalledPlugin{}}, nil
	}

	form := url.Values{}
	form.Set("name", "agent-a")
	form.Set("description", "first agent")
	form.Set("system_prompt", "do work")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `["playwright@claude-plugins-official"]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)

	req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "not installed") {
		t.Fatalf("expected installed validation error, got %s", rec.Body.String())
	}
}

func TestHandler_UpdateAgent_IsolatesPluginSelectionPerAgent(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	if err := llmConfigRepo.Create(context.Background(), &models.LLMConfig{
		Name:       "GPT 5.3 Codex",
		Provider:   models.ProviderOpenAI,
		Model:      "gpt-5.3-codex",
		MaxTokens:  4096,
		IsDefault:  false,
		AuthMethod: models.AuthMethodAPIKey,
	}); err != nil {
		t.Fatalf("create openai model: %v", err)
	}

	origDiscover := discoverPluginStateFn
	defer func() { discoverPluginStateFn = origDiscover }()
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{
			Installed: []models.InstalledPlugin{{ID: "playwright@claude-plugins-official", Enabled: true}},
		}, nil
	}

	agentA := &models.Agent{Name: "agent-a", Description: "a", SystemPrompt: "a", Model: "inherit", Tools: []string{"Read"}}
	agentB := &models.Agent{Name: "agent-b", Description: "b", SystemPrompt: "b", Model: "inherit", Tools: []string{"Read"}, Plugins: []string{"playwright@claude-plugins-official"}}
	if err := agentRepo.Create(context.Background(), agentA); err != nil {
		t.Fatalf("create agent a: %v", err)
	}
	if err := agentRepo.Create(context.Background(), agentB); err != nil {
		t.Fatalf("create agent b: %v", err)
	}

	form := url.Values{}
	form.Set("name", agentA.Name)
	form.Set("description", agentA.Description)
	form.Set("system_prompt", agentA.SystemPrompt)
	form.Set("model", "gpt-5.3-codex")
	form.Set("tools_json", `["Read"]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)

	req := httptest.NewRequest(http.MethodPut, "/agents/"+agentA.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updatedA, err := agentRepo.GetByID(context.Background(), agentA.ID)
	if err != nil {
		t.Fatalf("get updated agent a: %v", err)
	}
	updatedB, err := agentRepo.GetByID(context.Background(), agentB.ID)
	if err != nil {
		t.Fatalf("get updated agent b: %v", err)
	}
	if updatedA.Model != "gpt-5.3-codex" {
		t.Fatalf("expected agent A model to persist configured override, got %q", updatedA.Model)
	}
	if len(updatedA.Plugins) != 0 {
		t.Fatalf("expected agent A plugins cleared, got %v", updatedA.Plugins)
	}
	if len(updatedB.Plugins) != 1 || updatedB.Plugins[0] != "playwright@claude-plugins-official" {
		t.Fatalf("expected agent B plugin unchanged, got %v", updatedB.Plugins)
	}
}

func TestHandler_ListAgents_IncludesDefaultOffPluginText(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	if err := llmConfigRepo.Create(context.Background(), &models.LLMConfig{
		Name:       "GPT 5.4",
		Provider:   models.ProviderOpenAI,
		Model:      "gpt-5.4",
		MaxTokens:  4096,
		IsDefault:  false,
		AuthMethod: models.AuthMethodAPIKey,
	}); err != nil {
		t.Fatalf("create openai model: %v", err)
	}

	if err := llmConfigRepo.Create(context.Background(), &models.LLMConfig{
		Name:       "Claude Sonnet 4.5",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		IsDefault:  false,
		AuthMethod: models.AuthMethodCLI,
	}); err != nil {
		t.Fatalf("create anthropic model: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No plugins selected") {
		t.Fatalf("expected default-off plugin copy in agent modal")
	}
	if !strings.Contains(body, `<option value="gpt-5.4">GPT 5.4</option>`) {
		t.Fatalf("expected configured OpenAI model option in agent modal, body=%s", body)
	}
	if !strings.Contains(body, `option value="claude-sonnet-4-5-20250929"`) {
		t.Fatalf("expected configured Anthropic model value in agent modal, body=%s", body)
	}
	if strings.Contains(body, `<option value="sonnet">Sonnet</option>`) {
		t.Fatalf("expected hardcoded legacy sonnet option to be removed")
	}
}

func TestHandler_AgentModelPickerRoutesUseCompactProjection(t *testing.T) {
	_, e, _, agentRepo, counter := setupAgentModelPickerProjectionTest(t)

	agent := &models.Agent{Name: "Projection Existing", Key: "projection_existing", Model: "inherit", Tools: []string{"Read"}, Enabled: true}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create existing agent: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	e.ServeHTTP(rec, req)
	counter.SetEnabled(false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	assertCompactAgentPickerQueries(t, counter.Statements(), 1)
	body := rec.Body.String()
	inheritIndex := strings.Index(body, `<option value="inherit">Inherit (from task)</option>`)
	defaultIndex := strings.Index(body, `<option value="agent-picker-default">Agent Picker Default</option>`)
	firstCustomIndex := strings.Index(body, `<option value="agent-picker-model-00">Agent Picker 00</option>`)
	if inheritIndex < 0 || defaultIndex < 0 || firstCustomIndex < 0 || !(inheritIndex < defaultIndex && defaultIndex < firstCustomIndex) {
		t.Fatalf("agent model dropdown order/labels not preserved: inherit=%d default=%d firstCustom=%d", inheritIndex, defaultIndex, firstCustomIndex)
	}

	createForm := agentDialogModelForm("Projection Create", "agent-picker-model-17")
	counter.Reset()
	counter.SetEnabled(true)
	rec = performAgentDialogRequest(t, e, http.MethodPost, "/agents", createForm)
	counter.SetEnabled(false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected create status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	assertCompactAgentPickerQueries(t, counter.Statements(), 2)
	created := requireUserAgentByName(t, agentRepo, "Projection Create")
	if created.Model != "agent-picker-model-17" {
		t.Fatalf("known create model normalized to %q", created.Model)
	}

	updateForm := agentDialogModelForm("Projection Existing Updated", "agent-picker-model-23")
	counter.Reset()
	counter.SetEnabled(true)
	rec = performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+agent.ID, updateForm)
	counter.SetEnabled(false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected update status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	assertCompactAgentPickerQueries(t, counter.Statements(), 2)
	updated, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("reload updated agent: %v", err)
	}
	if updated.Model != "agent-picker-model-23" {
		t.Fatalf("known update model normalized to %q", updated.Model)
	}
}

func TestHandler_AgentModelNormalizationPreservesBlankInheritUnknownAndKnown(t *testing.T) {
	_, e, _, agentRepo, counter := setupAgentModelPickerProjectionTest(t)

	createCases := []struct {
		name      string
		input     string
		wantModel string
	}{
		{name: "Create Blank Model", input: "", wantModel: "inherit"},
		{name: "Create Inherit Model", input: " inherit ", wantModel: "inherit"},
		{name: "Create Unknown Model", input: "missing-model", wantModel: "inherit"},
		{name: "Create Known Model", input: "agent-picker-model-31", wantModel: "agent-picker-model-31"},
	}
	for _, tc := range createCases {
		counter.Reset()
		counter.SetEnabled(true)
		rec := performAgentDialogRequest(t, e, http.MethodPost, "/agents", agentDialogModelForm(tc.name, tc.input))
		counter.SetEnabled(false)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected status 200, got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
		assertCompactAgentPickerQueries(t, counter.Statements(), 2)
		created := requireUserAgentByName(t, agentRepo, tc.name)
		if created.Model != tc.wantModel {
			t.Fatalf("%s: model = %q, want %q", tc.name, created.Model, tc.wantModel)
		}
	}

	updateCases := []struct {
		name      string
		input     string
		wantModel string
	}{
		{name: "Update Blank Model", input: "", wantModel: "inherit"},
		{name: "Update Inherit Model", input: "inherit", wantModel: "inherit"},
		{name: "Update Unknown Model", input: "missing-model", wantModel: "inherit"},
		{name: "Update Known Model", input: "agent-picker-model-32", wantModel: "agent-picker-model-32"},
	}
	for _, tc := range updateCases {
		agent := &models.Agent{Name: tc.name, Key: strings.ReplaceAll(strings.ToLower(tc.name), " ", "_"), Model: "agent-picker-model-01", Tools: []string{"Read"}, Enabled: true}
		if err := agentRepo.Create(t.Context(), agent); err != nil {
			t.Fatalf("create %s: %v", tc.name, err)
		}
		counter.Reset()
		counter.SetEnabled(true)
		rec := performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+agent.ID, agentDialogModelForm(tc.name, tc.input))
		counter.SetEnabled(false)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected status 200, got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
		assertCompactAgentPickerQueries(t, counter.Statements(), 2)
		updated, err := agentRepo.GetByID(t.Context(), agent.ID)
		if err != nil {
			t.Fatalf("reload %s: %v", tc.name, err)
		}
		if updated.Model != tc.wantModel {
			t.Fatalf("%s: model = %q, want %q", tc.name, updated.Model, tc.wantModel)
		}
	}
}

func TestHandler_GenerateAgentNormalizesModelsWithCompactPickerProjection(t *testing.T) {
	h, e, _, _, counter := setupAgentModelPickerProjectionTest(t)

	for _, tc := range []struct {
		name      string
		model     string
		wantModel string
	}{
		{name: "known", model: "agent-picker-model-17", wantModel: "agent-picker-model-17"},
		{name: "unknown", model: "missing-generated-model", wantModel: "inherit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.llmSvc.SetLLMCaller(llmCallerFunc(func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
				if agent.APIKey == "" || agent.ExtraBodyJSON == "" {
					return "", "", 0, fmt.Errorf("default model was not hydrated with full execution fields: %#v", agent)
				}
				response := fmt.Sprintf(`{"name":"Generated %s","description":"desc","system_prompt":"Do work","model":%q,"tools":["Read"],"skills":[{"name":"Plan","description":"d","tools":"Read","content":"c"}],"mcp_servers":[]}`, tc.name, tc.model)
				return response, response, 0, nil
			}))

			form := url.Values{}
			form.Set("description", "Generate a coding agent")
			counter.Reset()
			counter.SetEnabled(true)
			req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			counter.SetEnabled(false)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}
			assertCompactAgentPickerQueries(t, counter.Statements(), 1)
			assertGenerateAgentLoadedFullDefaultModel(t, counter.Statements())

			var generated struct {
				Model          string `json:"model"`
				GenerationMode string `json:"generation_mode"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &generated); err != nil {
				t.Fatalf("decode generated response: %v", err)
			}
			if generated.GenerationMode != "llm" {
				t.Fatalf("generation mode = %q, want llm", generated.GenerationMode)
			}
			if generated.Model != tc.wantModel {
				t.Fatalf("generated model = %q, want %q", generated.Model, tc.wantModel)
			}
		})
	}
}

func setupAgentModelPickerProjectionTest(t *testing.T) (*Handler, *echo.Echo, *repository.LLMConfigRepo, *repository.AgentRepo, *testutil.SQLStatementCounter) {
	t.Helper()
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	largeBody := strings.Repeat("x", 64*1024)
	extraBodyJSON := `{"large":"` + largeBody + `"}`
	defaultCfg := &models.LLMConfig{
		Name:          "Agent Picker Default",
		Provider:      models.ProviderTest,
		Model:         "agent-picker-default",
		APIKey:        "secret-default-key",
		ExtraBodyJSON: extraBodyJSON,
		IsDefault:     true,
	}
	if err := llmConfigRepo.Create(t.Context(), defaultCfg); err != nil {
		t.Fatalf("create default model config: %v", err)
	}
	for i := 0; i < 50; i++ {
		cfg := &models.LLMConfig{
			Name:                 fmt.Sprintf("Agent Picker %02d", i),
			Provider:             models.ProviderOpenAICompatible,
			AuthMethod:           models.AuthMethodOAuth,
			Model:                fmt.Sprintf("agent-picker-model-%02d", i),
			APIKey:               fmt.Sprintf("secret-key-%02d", i),
			OAuthAccessToken:     "secret-token",
			OAuthRefreshToken:    "secret-refresh",
			OAuthClientSecret:    "secret-client",
			BaseURL:              "https://example.com/v1/",
			Transport:            "chat_completions",
			PresetSlug:           "custom",
			ExtraHeadersJSON:     `{"secret":"header"}`,
			ExtraBodyJSON:        extraBodyJSON,
			CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
			CustomAuthStateJSON:  `{"token":"secret"}`,
			MixtureConfigJSON:    `{"large":"` + largeBody + `"}`,
		}
		if err := llmConfigRepo.Create(t.Context(), cfg); err != nil {
			t.Fatalf("create large model config %d: %v", i, err)
		}
	}
	return h, e, llmConfigRepo, agentRepo, counter
}

func agentDialogModelForm(name, model string) url.Values {
	form := url.Values{}
	form.Set("name", name)
	form.Set("description", "model normalization")
	form.Set("system_prompt", "do work")
	form.Set("model", model)
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("enabled", "true")
	form.Set("selectable_as_primary", "true")
	return form
}

func requireUserAgentByName(t *testing.T, repo *repository.AgentRepo, name string) models.Agent {
	t.Helper()
	agents, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	for _, agent := range agents {
		if agent.SystemKind == "" && agent.Name == name {
			return agent
		}
	}
	t.Fatalf("user agent %q not found in %+v", name, agents)
	return models.Agent{}
}

func assertCompactAgentPickerQueries(t *testing.T, statements []string, minQueries int) {
	t.Helper()
	queries := 0
	for _, statement := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if !strings.Contains(stmt, " from agent_configs order by is_default desc, name asc") {
			continue
		}
		queries++
		projection := strings.Split(stmt, " from agent_configs ")[0]
		if !strings.Contains(projection, "select id, name, model") {
			t.Fatalf("agent picker projection = %q, want id/name/model in %s", projection, statement)
		}
		for _, forbidden := range []string{"provider", "api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url", "auth_header_name", "auth_header_value_prefix", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "oauth_config_revision", "mixture_config_json", "created_at", "updated_at", "max_workers", "worker_timeout", "auto_start_tasks"} {
			if strings.Contains(projection, forbidden) {
				t.Fatalf("agent picker query selected forbidden column %q: %s", forbidden, statement)
			}
		}
	}
	if queries < minQueries {
		t.Fatalf("found %d compact agent picker queries, want at least %d; statements: %#v", queries, minQueries, statements)
	}
}

func assertGenerateAgentLoadedFullDefaultModel(t *testing.T, statements []string) {
	t.Helper()
	for _, statement := range statements {
		stmt := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(stmt, " from agent_configs where is_default = 1 limit 1") {
			projection := strings.Split(stmt, " from agent_configs ")[0]
			if strings.Contains(projection, "api_key") && strings.Contains(projection, "extra_body_json") {
				return
			}
			t.Fatalf("GenerateAgent default-model query did not use full execution projection: %s", statement)
		}
	}
	t.Fatalf("GenerateAgent did not load the full default model; statements: %#v", statements)
}

func TestHandler_GenerateAgent_RejectsUninstalledPluginSelection(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	h.llmSvc = nil

	origDiscover := discoverPluginStateFn
	defer func() { discoverPluginStateFn = origDiscover }()
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{Installed: []models.InstalledPlugin{}}, nil
	}

	form := url.Values{}
	form.Set("description", "Review UI components with Playwright")
	form.Set("plugins_json", `["playwright@claude-plugins-official"]`)

	req := httptest.NewRequest(http.MethodPost, "/agents/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "not installed") {
		t.Fatalf("expected installed validation error, got %s", rec.Body.String())
	}
}

func TestHandler_GetPluginState_IncludesRuntimeStatus(t *testing.T) {
	_, e, _, _ := setupTestHandlerWithDB(t)

	origDiscover := discoverPluginStateFn
	origRuntime := pluginMCPRuntimeStateFn
	defer func() {
		discoverPluginStateFn = origDiscover
		pluginMCPRuntimeStateFn = origRuntime
	}()

	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{
			Marketplaces: []models.PluginMarketplace{{Name: "official", Source: "anthropics/claude-plugins-official"}},
		}, nil
	}
	pluginMCPRuntimeStateFn = func() []models.PluginRuntimeMCP {
		return []models.PluginRuntimeMCP{
			{Name: "playwright", Status: "failed", Error: "exec: npx not found"},
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/agents/plugins/state", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var state struct {
		Runtime []models.PluginRuntimeMCP `json:"runtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(state.Runtime) != 1 {
		t.Fatalf("expected runtime entry, got %d", len(state.Runtime))
	}
	if state.Runtime[0].Name != "playwright" || state.Runtime[0].Status != "failed" {
		t.Fatalf("unexpected runtime payload: %#v", state.Runtime[0])
	}
}

func TestHandler_GetPluginState_RuntimePluginIDEnriched(t *testing.T) {
	_, e, _, _ := setupTestHandlerWithDB(t)

	origDiscover := discoverPluginStateFn
	origRuntime := pluginMCPRuntimeStateFn
	origMapping := pluginServerNameMappingFn
	defer func() {
		discoverPluginStateFn = origDiscover
		pluginMCPRuntimeStateFn = origRuntime
		pluginServerNameMappingFn = origMapping
	}()

	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{
			Installed: []models.InstalledPlugin{
				{ID: "adspirer-ads-agent@claude-plugins-official", Enabled: true},
				{ID: "github@claude-plugins-official", Enabled: true},
			},
		}, nil
	}
	pluginMCPRuntimeStateFn = func() []models.PluginRuntimeMCP {
		return []models.PluginRuntimeMCP{
			{Name: "adspirer", Status: "failed", Error: "MCP HTTP 401: unauthorized"},
			{Name: "github", Status: "running"},
		}
	}
	// adspirer MCP server name != plugin ID prefix (adspirer-ads-agent)
	pluginServerNameMappingFn = func(ctx context.Context, installed []models.InstalledPlugin) map[string]string {
		return map[string]string{
			"adspirer": "adspirer-ads-agent@claude-plugins-official",
			"github":   "github@claude-plugins-official",
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/agents/plugins/state", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var state struct {
		Runtime []models.PluginRuntimeMCP `json:"runtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(state.Runtime) != 2 {
		t.Fatalf("expected 2 runtime entries, got %d", len(state.Runtime))
	}
	// Verify plugin_id is populated on runtime entries
	for _, rt := range state.Runtime {
		if rt.Name == "adspirer" {
			if rt.PluginID != "adspirer-ads-agent@claude-plugins-official" {
				t.Errorf("expected adspirer runtime entry to have plugin_id 'adspirer-ads-agent@claude-plugins-official', got %q", rt.PluginID)
			}
			if rt.Status != "failed" {
				t.Errorf("expected adspirer runtime status 'failed', got %q", rt.Status)
			}
		}
		if rt.Name == "github" {
			if rt.PluginID != "github@claude-plugins-official" {
				t.Errorf("expected github runtime entry to have plugin_id 'github@claude-plugins-official', got %q", rt.PluginID)
			}
		}
	}
}

func TestHandler_InstallPlugin_ReturnsMCPStartupWarning(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)

	origInstall := installPluginFn
	origEnsure := ensurePluginMCPRunningFn
	origDiscover := discoverPluginStateFn
	defer func() {
		installPluginFn = origInstall
		ensurePluginMCPRunningFn = origEnsure
		discoverPluginStateFn = origDiscover
	}()

	installed := false
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		state := models.PluginState{Installed: []models.InstalledPlugin{}}
		if installed {
			state.Installed = append(state.Installed, models.InstalledPlugin{ID: "playwright@claude-plugins-official", Enabled: true})
		}
		return state, nil
	}
	installPluginFn = func(ctx context.Context, pluginID, scope string) error {
		installed = true
		return nil
	}
	ensurePluginMCPRunningFn = func(ctx context.Context, pluginIDs []string, workDir string) error {
		return errors.New("playwright: exec: npx not found")
	}

	req := httptest.NewRequest(http.MethodPost, "/agents/plugins/install", strings.NewReader(`{"plugin_id":"playwright@claude-plugins-official","scope":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("expected ok=true payload, got %#v", payload)
	}
	warning, _ := payload["warning"].(string)
	if !strings.Contains(strings.ToLower(warning), "npx") {
		t.Fatalf("expected warning to mention npx failure, got %q", warning)
	}

	projects, err := h.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects: %v", err)
	}
	if len(projects) == 0 {
		t.Fatalf("expected at least one project")
	}
	alerts, err := h.alertSvc.ListByProject(context.Background(), projects[0].ID, 100)
	if err != nil {
		t.Fatalf("listing alerts: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatalf("expected plugin startup failure alert to be created")
	}
	if !strings.Contains(strings.ToLower(alerts[0].Title), "plugin mcp startup failed") {
		t.Fatalf("unexpected alert title: %q", alerts[0].Title)
	}
}

func TestHandler_InstallPlugin_TargetInstallFailureReturnsBadRequest(t *testing.T) {
	_, e, _, _ := setupTestHandlerWithDB(t)

	origInstall := installPluginFn
	origEnsure := ensurePluginMCPRunningFn
	origDiscover := discoverPluginStateFn
	defer func() {
		installPluginFn = origInstall
		ensurePluginMCPRunningFn = origEnsure
		discoverPluginStateFn = origDiscover
	}()

	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{Installed: []models.InstalledPlugin{}}, nil
	}
	installPluginFn = func(ctx context.Context, pluginID, scope string) error {
		return errors.New("plugin install failed: unauthorized")
	}
	ensurePluginMCPRunningFn = func(ctx context.Context, pluginIDs []string, workDir string) error {
		t.Fatalf("ensure should not be called when install fails")
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/agents/plugins/install", strings.NewReader(`{"plugin_id":"playwright@claude-plugins-official","scope":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := payload["ok"].(bool); ok {
		t.Fatalf("expected ok=false payload, got %#v", payload)
	}
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "install") {
		t.Fatalf("expected install failure error text, got %#v", payload)
	}
}

func TestHandler_InstallPlugin_EditAgentAutoEnablesPlugin(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agent := &models.Agent{Name: "agent-a", Description: "a", SystemPrompt: "a", Model: "inherit", Tools: []string{"Read"}}
	if err := agentRepo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	origDiscover := discoverPluginStateFn
	origInstall := installPluginFn
	origEnsure := ensurePluginMCPRunningFn
	defer func() {
		discoverPluginStateFn = origDiscover
		installPluginFn = origInstall
		ensurePluginMCPRunningFn = origEnsure
	}()

	installed := false
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		state := models.PluginState{Installed: []models.InstalledPlugin{}}
		if installed {
			state.Installed = append(state.Installed, models.InstalledPlugin{ID: "playwright@claude-plugins-official", Enabled: true})
		}
		return state, nil
	}
	installPluginFn = func(ctx context.Context, pluginID, scope string) error {
		installed = true
		return nil
	}
	ensurePluginMCPRunningFn = func(ctx context.Context, pluginIDs []string, workDir string) error {
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/agents/plugins/install", strings.NewReader(`{"plugin_id":"playwright@claude-plugins-official","scope":"user","agent_id":"`+agent.ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("expected ok=true payload, got %#v", payload)
	}
	enabled, _ := payload["enabled_for_agent"].(bool)
	if !enabled {
		t.Fatalf("expected enabled_for_agent=true payload, got %#v", payload)
	}

	updated, err := agentRepo.GetByID(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("load updated agent: %v", err)
	}
	if updated == nil || len(updated.Plugins) != 1 || updated.Plugins[0] != "playwright@claude-plugins-official" {
		t.Fatalf("expected plugin enabled for agent, got %#v", updated)
	}
}

func TestHandler_InstallPlugin_EditAgentEnableFailureReturnsRetryableError(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agent := &models.Agent{Name: "agent-a", Description: "a", SystemPrompt: "a", Model: "inherit", Tools: []string{"Read"}}
	if err := agentRepo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	origDiscover := discoverPluginStateFn
	origInstall := installPluginFn
	origEnsure := ensurePluginMCPRunningFn
	defer func() {
		discoverPluginStateFn = origDiscover
		installPluginFn = origInstall
		ensurePluginMCPRunningFn = origEnsure
	}()

	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{Installed: []models.InstalledPlugin{{ID: "playwright@claude-plugins-official", Enabled: true}}}, nil
	}
	installPluginFn = func(ctx context.Context, pluginID, scope string) error {
		t.Fatalf("install should not be called when plugin already installed")
		return nil
	}
	ensurePluginMCPRunningFn = func(ctx context.Context, pluginIDs []string, workDir string) error {
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/agents/plugins/install", strings.NewReader(`{"plugin_id":"playwright@claude-plugins-official","scope":"user","agent_id":"missing-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	enabled, _ := payload["enabled_for_agent"].(bool)
	if enabled {
		t.Fatalf("expected enabled_for_agent=false payload, got %#v", payload)
	}
	enableErr, _ := payload["enable_error"].(string)
	if !strings.Contains(strings.ToLower(enableErr), "agent not found") {
		t.Fatalf("expected enable_error to mention missing agent, got %#v", payload)
	}

	updated, err := agentRepo.GetByID(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("load updated agent: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected existing agent to remain present")
	}
	if len(updated.Plugins) != 0 {
		t.Fatalf("expected plugin list unchanged after enable failure, got %v", updated.Plugins)
	}
}

func TestHandler_UninstallPlugin_SuccessSuppressesReconcileWarningPayload(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)

	origUninstall := uninstallPluginFn
	origReconcile := reconcilePluginMCPRunningFn
	defer func() {
		uninstallPluginFn = origUninstall
		reconcilePluginMCPRunningFn = origReconcile
	}()

	uninstallPluginFn = func(ctx context.Context, pluginID string) error {
		if pluginID != "playwright@claude-plugins-official" {
			t.Fatalf("unexpected plugin id: %q", pluginID)
		}
		return nil
	}
	reconcilePluginMCPRunningFn = func(ctx context.Context, workDir string) error {
		return errors.New("partial persistent MCP reconcile: adspirer: MCP HTTP 401")
	}

	req := httptest.NewRequest(http.MethodPost, "/agents/plugins/uninstall", strings.NewReader(`{"plugin_id":"playwright@claude-plugins-official"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("expected ok=true payload, got %#v", payload)
	}
	if _, exists := payload["warning"]; exists {
		t.Fatalf("expected uninstall success payload to omit reconcile warning, got %#v", payload)
	}

	projects, err := h.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects: %v", err)
	}
	if len(projects) == 0 {
		t.Fatalf("expected at least one project")
	}
	alerts, err := h.alertSvc.ListByProject(context.Background(), projects[0].ID, 100)
	if err != nil {
		t.Fatalf("listing alerts: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatalf("expected plugin reconcile warning alert to be created")
	}
	if !strings.Contains(strings.ToLower(alerts[0].Title), "plugin mcp reconcile warning") {
		t.Fatalf("unexpected alert title: %q", alerts[0].Title)
	}
}

func TestHandler_UninstallPlugin_ErrorReturnsBadRequest(t *testing.T) {
	_, e, _, _ := setupTestHandlerWithDB(t)

	origUninstall := uninstallPluginFn
	defer func() {
		uninstallPluginFn = origUninstall
	}()

	uninstallPluginFn = func(ctx context.Context, pluginID string) error {
		return errors.New("plugin not installed")
	}

	req := httptest.NewRequest(http.MethodPost, "/agents/plugins/uninstall", strings.NewReader(`{"plugin_id":"playwright@claude-plugins-official"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "not installed") {
		t.Fatalf("expected not installed error, got %s", rec.Body.String())
	}
}

func TestHandler_CreateAgent_RejectsInvalidScopedFilesDirectory(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	cases := []string{"/tmp/outside", "../outside", ".."}
	for _, dir := range cases {
		t.Run(dir, func(t *testing.T) {
			form := url.Values{}
			form.Set("name", "scoped-agent")
			form.Set("description", "scoped")
			form.Set("system_prompt", "work")
			form.Set("model", "inherit")
			form.Set("tools_json", `["ScopedFiles"]`)
			form.Set("tool_config_json", fmt.Sprintf(`{"scoped_files":[{"directory":%q,"permissions":["read"]}]}`, dir))
			form.Set("plugins_json", `[]`)
			form.Set("skills_json", `[]`)
			form.Set("mcp_servers_json", `[]`)

			req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
			}
			body := strings.ToLower(rec.Body.String())
			if !strings.Contains(body, "project-relative") && !strings.Contains(body, "inside the project") {
				t.Fatalf("expected scoped directory validation error, got %s", rec.Body.String())
			}
		})
	}
}

func TestHandler_AgentDialogFormParsing_CreateAndUpdatePersistSharedFields(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   func(agentID, projectID string) string
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path: func(agentID, projectID string) string {
				return "/agents?project_id=" + projectID
			},
		},
		{
			name:   "update",
			method: http.MethodPut,
			path: func(agentID, projectID string) string {
				return "/agents/" + agentID + "?project_id=" + projectID
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
			agentRepo := repository.NewAgentRepo(db)
			h.SetAgentRepo(agentRepo)

			model := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
				a.Name = "Configured Dialog Model"
				a.Model = "dialog-configured-model"
				a.IsDefault = false
			})
			project := &models.Project{Name: "Dialog Project", RepoPath: t.TempDir()}
			if err := h.projectSvc.Create(t.Context(), project); err != nil {
				t.Fatalf("create project: %v", err)
			}

			origDiscover := discoverPluginStateFn
			defer func() { discoverPluginStateFn = origDiscover }()
			discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
				return models.PluginState{Installed: []models.InstalledPlugin{{ID: "playwright@claude-plugins-official", Enabled: true}}}, nil
			}

			var existingID string
			if tc.method == http.MethodPut {
				existing := &models.Agent{
					Name:                "Before Dialog Agent",
					Key:                 "before_dialog_agent",
					Description:         "before",
					SystemPrompt:        "before prompt",
					Model:               "inherit",
					Tools:               []string{"Read"},
					SelectableAsPrimary: true,
					Enabled:             true,
				}
				if err := agentRepo.Create(t.Context(), existing); err != nil {
					t.Fatalf("create existing agent: %v", err)
				}
				existingID = existing.ID
			}

			form := url.Values{}
			form.Set("name", "Dialog Agent "+tc.name)
			form.Set("description", "shared dialog payload")
			form.Set("system_prompt", "Use the shared parser")
			form.Set("model", model.Model)
			form.Set("tools_json", `["read","ScopedFiles","send_message","unknown-tool"]`)
			form.Set("tool_config_json", `{"scoped_files":[{"directory":"configs/secrets","permissions":["read","write"]}],"skip_default_tools":true,"disable_runtime_worktree":true}`)
			form.Set("plugins_json", `["playwright@claude-plugins-official"]`)
			form.Set("skills_json", `[{"name":"Generated Skill","description":"from dialog","tools":"Read,Grep","content":"Follow the dialog workflow."}]`)
			form.Set("mcp_servers_json", `[{"name":"local-docs","command":["npx","docs-mcp"],"env":{"TOKEN":"test"}}]`)
			form.Set("key", "dialog_agent_"+tc.name)
			form.Set("scope", "project")
			form.Set("selectable_as_primary", "false")
			form.Set("enabled", "false")
			form.Set("permission_defaults_json", `{"read_agents":true,"write_skills":true,"use_shell_or_tools":true}`)
			form.Set("source_refs_json", `["https://example.test/source"]`)

			rec := performAgentDialogRequest(t, e, tc.method, tc.path(existingID, project.ID), form)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}

			stored, err := agentRepo.GetByKey(t.Context(), "dialog_agent_"+tc.name)
			if err != nil {
				t.Fatalf("load saved agent: %v", err)
			}
			if stored == nil {
				t.Fatal("saved agent not found")
			}
			if stored.Model != model.Model {
				t.Fatalf("configured model not persisted: got %q want %q", stored.Model, model.Model)
			}
			if !agentToolsInclude(stored.Tools, "Read") || !agentToolsInclude(stored.Tools, models.AgentToolScopedFiles) || !agentToolsInclude(stored.Tools, "send_message") || agentToolsInclude(stored.Tools, "unknown-tool") {
				t.Fatalf("tools not normalized as expected: %+v", stored.Tools)
			}
			wantToolConfig := models.AgentToolConfig{
				ScopedFiles:            []models.ScopedFilesConfig{{Directory: "configs/secrets", Permissions: []string{"read", "write"}}},
				SkipDefaultTools:       true,
				DisableRuntimeWorktree: true,
			}
			if !reflect.DeepEqual(stored.ToolConfig, wantToolConfig) {
				t.Fatalf("tool config mismatch: got %+v want %+v", stored.ToolConfig, wantToolConfig)
			}
			if !reflect.DeepEqual(stored.Plugins, []string{"playwright@claude-plugins-official"}) {
				t.Fatalf("plugins not normalized/persisted: %+v", stored.Plugins)
			}
			if len(stored.Skills) != 1 || stored.Skills[0].Name != "Generated Skill" || stored.Skills[0].Content != "Follow the dialog workflow." {
				t.Fatalf("skills not persisted: %+v", stored.Skills)
			}
			if len(stored.MCPServers) != 1 || stored.MCPServers[0].Name != "local-docs" || !reflect.DeepEqual(stored.MCPServers[0].Command, []string{"npx", "docs-mcp"}) || stored.MCPServers[0].Env["TOKEN"] != "test" {
				t.Fatalf("MCP servers not persisted: %+v", stored.MCPServers)
			}
			if stored.Scope != models.AgentScopeProject || stored.ProjectID != project.ID || stored.Enabled || stored.SelectableAsPrimary {
				t.Fatalf("scope/enabled/project fallback fields mismatch: %+v", stored)
			}
			if !stored.PermissionDefaults.ReadAgents || !stored.PermissionDefaults.WriteSkills || !stored.PermissionDefaults.UseShellOrTools {
				t.Fatalf("permission defaults not persisted: %+v", stored.PermissionDefaults)
			}
			if !reflect.DeepEqual(stored.SourceRefs, []string{"https://example.test/source"}) {
				t.Fatalf("source refs not persisted: %+v", stored.SourceRefs)
			}
		})
	}
}

func TestHandler_UpdateAgent_OmittedOptionalJSONFieldsPreserveExistingValues(t *testing.T) {
	h, e, llmConfigRepo, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	model := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Model = "omitted-json-model"
		a.IsDefault = false
	})

	origDiscover := discoverPluginStateFn
	defer func() { discoverPluginStateFn = origDiscover }()
	discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
		return models.PluginState{Installed: []models.InstalledPlugin{{ID: "playwright@claude-plugins-official", Enabled: true}}}, nil
	}

	agent := &models.Agent{
		Name:         "Omitted JSON Agent",
		Key:          "omitted_json_agent",
		Description:  "before",
		SystemPrompt: "before prompt",
		Model:        "inherit",
		Tools:        []string{models.AgentToolScopedFiles, "Read"},
		ToolConfig: models.AgentToolConfig{
			ScopedFiles:            []models.ScopedFilesConfig{{Directory: "configs/secrets", Permissions: []string{"read"}}},
			SkipDefaultTools:       true,
			DisableRuntimeWorktree: true,
		},
		Plugins: []string{"playwright@claude-plugins-official"},
		Skills:  []models.SkillConfig{{Name: "Keep Skill", Description: "existing", Content: "Do not replace."}},
		MCPServers: []models.MCPServerConfig{{
			Name:    "keep-mcp",
			Command: []string{"node", "server.js"},
			Env:     map[string]string{"KEEP": "1"},
		}},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create existing agent: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Omitted JSON Agent Updated")
	form.Set("description", "after")
	form.Set("system_prompt", "after prompt")
	form.Set("model", model.Model)
	form.Set("key", "omitted_json_agent")
	form.Set("scope", "global")
	form.Set("enabled", "true")
	form.Set("selectable_as_primary", "true")

	rec := performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+agent.ID, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if stored.Model != model.Model || stored.Name != "Omitted JSON Agent Updated" {
		t.Fatalf("basic fields not updated: %+v", stored)
	}
	if !reflect.DeepEqual(stored.Tools, []string{models.AgentToolScopedFiles, "Read"}) {
		t.Fatalf("tools changed when tools_json omitted: %+v", stored.Tools)
	}
	if !reflect.DeepEqual(stored.ToolConfig, agent.ToolConfig) {
		t.Fatalf("tool config changed when tool_config_json omitted: %+v", stored.ToolConfig)
	}
	if !reflect.DeepEqual(stored.Plugins, agent.Plugins) {
		t.Fatalf("plugins changed when plugins_json omitted: %+v", stored.Plugins)
	}
	if !reflect.DeepEqual(stored.Skills, agent.Skills) {
		t.Fatalf("skills changed when skills_json omitted: %+v", stored.Skills)
	}
	if !reflect.DeepEqual(stored.MCPServers, agent.MCPServers) {
		t.Fatalf("MCP servers changed when mcp_servers_json omitted: %+v", stored.MCPServers)
	}
}

func TestHandler_AgentDialogFormParsing_RejectsInvalidToolConfigForCreateAndUpdate(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   func(agentID string) string
	}{
		{name: "create", method: http.MethodPost, path: func(agentID string) string { return "/agents" }},
		{name: "update", method: http.MethodPut, path: func(agentID string) string { return "/agents/" + agentID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _, db := setupTestHandlerWithDB(t)
			agentRepo := repository.NewAgentRepo(db)
			h.SetAgentRepo(agentRepo)
			var agentID string
			if tc.method == http.MethodPut {
				agent := &models.Agent{Name: "Invalid Tool Config", Key: "invalid_tool_config", Model: "inherit", Tools: []string{"Read"}, Enabled: true}
				if err := agentRepo.Create(t.Context(), agent); err != nil {
					t.Fatalf("create agent: %v", err)
				}
				agentID = agent.ID
			}

			form := url.Values{}
			form.Set("name", "Invalid Tool Config")
			form.Set("description", "bad config")
			form.Set("system_prompt", "work")
			form.Set("model", "inherit")
			form.Set("tools_json", `["ScopedFiles"]`)
			form.Set("tool_config_json", `{"scoped_files":`)
			form.Set("plugins_json", `[]`)
			form.Set("skills_json", `[]`)
			form.Set("mcp_servers_json", `[]`)

			rec := performAgentDialogRequest(t, e, tc.method, tc.path(agentID), form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), "invalid tool configuration") {
				t.Fatalf("expected invalid tool configuration error, got %s", rec.Body.String())
			}
		})
	}
}

func TestHandler_AgentDialogFormParsing_RejectsInvalidPluginIDsForCreateAndUpdate(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   func(agentID string) string
	}{
		{name: "create", method: http.MethodPost, path: func(agentID string) string { return "/agents" }},
		{name: "update", method: http.MethodPut, path: func(agentID string) string { return "/agents/" + agentID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _, db := setupTestHandlerWithDB(t)
			agentRepo := repository.NewAgentRepo(db)
			h.SetAgentRepo(agentRepo)
			origDiscover := discoverPluginStateFn
			defer func() { discoverPluginStateFn = origDiscover }()
			discoverPluginStateFn = func(ctx context.Context) (models.PluginState, error) {
				return models.PluginState{Installed: []models.InstalledPlugin{}}, nil
			}

			var agentID string
			if tc.method == http.MethodPut {
				agent := &models.Agent{Name: "Invalid Plugin", Key: "invalid_plugin", Model: "inherit", Tools: []string{"Read"}, Enabled: true}
				if err := agentRepo.Create(t.Context(), agent); err != nil {
					t.Fatalf("create agent: %v", err)
				}
				agentID = agent.ID
			}

			form := url.Values{}
			form.Set("name", "Invalid Plugin")
			form.Set("description", "bad plugin")
			form.Set("system_prompt", "work")
			form.Set("model", "inherit")
			form.Set("tools_json", `[]`)
			form.Set("plugins_json", `["playwright@claude-plugins-official"]`)
			form.Set("skills_json", `[]`)
			form.Set("mcp_servers_json", `[]`)

			rec := performAgentDialogRequest(t, e, tc.method, tc.path(agentID), form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), "not installed") {
				t.Fatalf("expected plugin validation error, got %s", rec.Body.String())
			}
		})
	}
}

func performAgentDialogRequest(t *testing.T, e *echo.Echo, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func agentNameValidationForm(name string) url.Values {
	form := url.Values{}
	form.Set("name", name)
	form.Set("description", "agent name validation")
	form.Set("system_prompt", "work")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("enabled", "true")
	form.Set("selectable_as_primary", "true")
	return form
}

func TestHandler_CreateAgent_RejectsBlankAndDuplicateSelectableNames(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	blank := agentNameValidationForm("   \t  ")
	rec := performAgentDialogRequest(t, e, http.MethodPost, "/agents", blank)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected blank name status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "agent name is required") {
		t.Fatalf("expected controlled blank-name validation error, got %s", rec.Body.String())
	}

	agents, err := agentRepo.List(t.Context())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	for _, agent := range agents {
		if agent.SystemKind == "" {
			t.Fatalf("blank-name create persisted user agent: %+v", agent)
		}
	}

	create := agentNameValidationForm(" Reviewer ")
	rec = performAgentDialogRequest(t, e, http.MethodPost, "/agents", create)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected trimmed create status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := agentRepo.GetUniqueSelectableByName(t.Context(), "Reviewer")
	if err != nil {
		t.Fatalf("resolve created reviewer: %v", err)
	}
	if stored == nil || stored.Name != "Reviewer" {
		t.Fatalf("expected trimmed stored Reviewer, got %+v", stored)
	}

	dup := agentNameValidationForm(" reviewer ")
	rec = performAgentDialogRequest(t, e, http.MethodPost, "/agents", dup)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate name status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "already exists") {
		t.Fatalf("expected duplicate-name validation error, got %s", rec.Body.String())
	}
	matches, err := agentRepo.ListSelectableByName(t.Context(), "Reviewer")
	if err != nil {
		t.Fatalf("list reviewer matches: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != stored.ID {
		t.Fatalf("duplicate create changed selectable reviewer rows: %+v", matches)
	}
}

func TestHandler_UpdateAgent_RejectsBlankAndDuplicateSelectableNamesWithoutMutating(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	reviewer := &models.Agent{Name: "Reviewer", Key: "reviewer", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	other := &models.Agent{Name: "Other", Key: "other", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	if err := agentRepo.Create(t.Context(), reviewer); err != nil {
		t.Fatalf("create reviewer: %v", err)
	}
	if err := agentRepo.Create(t.Context(), other); err != nil {
		t.Fatalf("create other: %v", err)
	}

	blank := agentNameValidationForm("   ")
	rec := performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+other.ID, blank)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected blank update status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	reloaded, err := agentRepo.GetByID(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("reload after blank update: %v", err)
	}
	if reloaded.Name != "Other" {
		t.Fatalf("blank update mutated name to %q", reloaded.Name)
	}

	dup := agentNameValidationForm(" REVIEWER ")
	rec = performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+other.ID, dup)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected duplicate update status 400, got %d: %s", rec.Code, rec.Body.String())
	}
	reloaded, err = agentRepo.GetByID(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("reload after duplicate update: %v", err)
	}
	if reloaded.Name != "Other" {
		t.Fatalf("duplicate update mutated name to %q", reloaded.Name)
	}

	trimmed := agentNameValidationForm(" Other Updated ")
	rec = performAgentDialogRequest(t, e, http.MethodPut, "/agents/"+other.ID, trimmed)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected trimmed update status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	reloaded, err = agentRepo.GetByID(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("reload after trimmed update: %v", err)
	}
	if reloaded.Name != "Other Updated" {
		t.Fatalf("expected trimmed update name, got %q", reloaded.Name)
	}
}

func TestHandler_AgentNameValidation_AllowsDisabledAndNonPrimaryDuplicates(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	primary := &models.Agent{Name: "Reviewer", Key: "reviewer", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	if err := agentRepo.Create(t.Context(), primary); err != nil {
		t.Fatalf("create primary reviewer: %v", err)
	}

	disabled := agentNameValidationForm(" reviewer ")
	disabled.Set("enabled", "false")
	rec := performAgentDialogRequest(t, e, http.MethodPost, "/agents", disabled)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected disabled duplicate create status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	nonPrimary := agentNameValidationForm(" REVIEWER ")
	nonPrimary.Set("selectable_as_primary", "false")
	rec = performAgentDialogRequest(t, e, http.MethodPost, "/agents", nonPrimary)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected non-primary duplicate create status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	matches, err := agentRepo.ListSelectableByName(t.Context(), "Reviewer")
	if err != nil {
		t.Fatalf("list selectable reviewer matches: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != primary.ID {
		t.Fatalf("disabled/non-primary duplicates should not affect selectable name resolution: %+v", matches)
	}
}

func TestHandler_AgentsPage_AdvancedTabsAreReachableAndSubmitted(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, tab := range []string{"skills", "lifecycle", "advanced"} {
		if !strings.Contains(body, tab+": '"+tab+"'") {
			t.Fatalf("expected setAgentSection to allow %q tab", tab)
		}
	}
	if strings.Contains(body, "permissions: 'permissions'") || strings.Contains(body, `data-agent-section-tab="permissions"`) || strings.Contains(body, `data-agent-section-panel="permissions"`) {
		t.Fatalf("expected permissions tab to be folded into lifecycle")
	}
	if strings.Contains(body, "routing: 'routing'") || strings.Contains(body, `data-agent-section-tab="routing"`) || strings.Contains(body, `data-agent-section-panel="routing"`) {
		t.Fatalf("expected routing tab and section to be absent")
	}
	if !strings.Contains(body, `id="agent_lifecycle_hooks_json" name="lifecycle_hooks_json"`) {
		t.Fatalf("expected lifecycle hooks hidden form field")
	}
	if !strings.Contains(body, "agent_lifecycle_hooks_json').value = JSON.stringify(collectLifecycleHooksFromDOM())") {
		t.Fatalf("expected lifecycle hooks to serialize with the main agent form")
	}
}

func TestHandler_CreateAgent_PersistsLifecycleTabs(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	form := url.Values{}
	form.Set("name", "agent-tabs")
	form.Set("description", "tab fields")
	form.Set("system_prompt", "do work")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[{"name":"Draft Skill","description":"from tab","content":"body"}]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("permission_defaults_json", `{"read_agents":true,"write_skills":true}`)
	form.Set("source_refs_json", `["https://example.test/runbook"]`)
	form.Set("key", "agent_tabs")
	form.Set("scope", "project")
	form.Set("enabled", "true")
	form.Set("lifecycle_hooks_json", `[{"when":"before_run","skill_key":"load_context","blocking":true,"enabled":true,"output_contract":"context_block"}]`)

	req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	agents, err := agentRepo.List(t.Context())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	var created *models.Agent
	for i := range agents {
		if agents[i].Key == "agent_tabs" {
			created = &agents[i]
			break
		}
	}
	if created == nil {
		t.Fatalf("created agent not found: %+v", agents)
	}
	if created.Scope != models.AgentScopeProject || !created.SelectableAsPrimary || !created.Enabled {
		t.Fatalf("advanced fields/defaults not persisted: %+v", created)
	}
	if !created.PermissionDefaults.ReadAgents || !created.PermissionDefaults.WriteSkills {
		t.Fatalf("permissions not persisted: %+v", created.PermissionDefaults)
	}
	if len(created.Skills) != 1 || created.Skills[0].Name != "Draft Skill" {
		t.Fatalf("skills not persisted: %+v", created.Skills)
	}
	if len(created.SourceRefs) != 1 || created.SourceRefs[0] != "https://example.test/runbook" {
		t.Fatalf("source refs not persisted: %+v", created.SourceRefs)
	}
	hooks, err := lifecycleRepo.HooksByAgent(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	if len(hooks) != 1 || hooks[0].SkillKey != "load_context" || hooks[0].OutputContract != models.OutputContractContextBlock {
		t.Fatalf("lifecycle hooks not persisted: %+v", hooks)
	}
}

func TestHandler_CreateAgent_ConvertsFormSkillsToAgentOwnedSkillFiles(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	form := url.Values{}
	form.Set("name", "skill agent")
	form.Set("description", "agent with skills")
	form.Set("system_prompt", "do work")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[{"name":"Draft Skill","description":"from tab","tools":"Read,Grep","content":"body"}]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("key", "skill_agent")
	form.Set("scope", "global")
	form.Set("enabled", "true")

	req := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	agent, err := agentRepo.GetByKey(t.Context(), "skill_agent")
	if err != nil {
		t.Fatal(err)
	}
	if agent == nil {
		t.Fatal("created agent not found")
	}
	if len(agent.Skills) != 0 {
		t.Fatalf("form skills should be converted off the DB record, got %+v", agent.Skills)
	}
	data, err := os.ReadFile(filepath.Join(root, "agents", "skill_agent", "skills", "draft_skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(data), "Draft Skill", "Allowed legacy tools: Read,Grep", "body") {
		t.Fatalf("converted skill file mismatch: %s", data)
	}
	index, err := os.ReadFile(filepath.Join(root, "agents", "skill_agent", "SKILLS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "## skill_agent/draft_skill") {
		t.Fatalf("converted skill not indexed: %s", index)
	}
	agentsIndex, err := os.ReadFile(filepath.Join(root, "agents", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agentsIndex), "## skill_agent") || !strings.Contains(string(agentsIndex), "skill_agent/SKILLS.md") {
		t.Fatalf("created agent not indexed on disk: %s", agentsIndex)
	}
}

func TestHandler_ListAgents_MaterializesLegacyDBAgentsToDisk(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	legacy := &models.Agent{
		Name:         "Cindy",
		Key:          "Legacy Cindy!",
		Description:  "Legacy helper",
		SystemPrompt: "help users",
		Model:        "inherit",
		Tools:        []string{"Read"},
		Skills: []models.SkillConfig{{
			Name:        "Draft Skill",
			Description: "converted legacy skill",
			Content:     "Use the repo conventions.",
		}},
		SelectableAsPrimary: true,
		Enabled:             true,
	}
	if err := agentRepo.Create(t.Context(), legacy); err != nil {
		t.Fatalf("create legacy agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := agentRepo.GetByID(t.Context(), legacy.ID)
	if err != nil {
		t.Fatalf("reload legacy agent: %v", err)
	}
	if stored.Key != "cindy" {
		t.Fatalf("expected generated key cindy, got %+v", stored)
	}
	rootDecl, err := os.ReadFile(filepath.Join(root, "agents", "cindy", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read materialized root declaration: %v", err)
	}
	if !containsAll(string(rootDecl), "key: cindy", "name: Cindy", "help users") {
		t.Fatalf("materialized declaration mismatch:\n%s", rootDecl)
	}
	agentsIndex, err := os.ReadFile(filepath.Join(root, "agents", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read agents index: %v", err)
	}
	if !strings.Contains(string(agentsIndex), "## cindy") {
		t.Fatalf("legacy agent not indexed on disk:\n%s", agentsIndex)
	}
	convertedSkill, err := os.ReadFile(filepath.Join(root, "agents", "cindy", "skills", "draft_skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read converted legacy skill under normalized key: %v", err)
	}
	if !containsAll(string(convertedSkill), "key: draft_skill", "Use the repo conventions.") {
		t.Fatalf("converted legacy skill mismatch:\n%s", convertedSkill)
	}
}

func TestHandler_UpdateAgent_RefreshesMaterializedAgentRootFile(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	agent := &models.Agent{Name: "Claudia", Key: "claudia", SystemPrompt: "old prompt", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := h.materializeAgentToDisk(e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder()), agent, ""); err != nil {
		t.Fatalf("initial materialize: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Claudia Updated")
	form.Set("description", "updated description")
	form.Set("system_prompt", "new prompt")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("key", "claudia")
	form.Set("scope", "global")
	form.Set("enabled", "true")

	req := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "agents", "claudia", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read materialized agent: %v", err)
	}
	if !containsAll(string(data), "name: Claudia Updated", "system_prompt: new prompt", "updated description") {
		t.Fatalf("materialized agent root not refreshed:\n%s", data)
	}
}

func TestHandler_UpdateAgent_RejectsProtectedAgentEdits(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agent := &models.Agent{
		Name:            "Protected Agent",
		Key:             "protected_agent",
		SystemPrompt:    "original prompt",
		Model:           "inherit",
		Tools:           []string{"Read"},
		GeneratedStatus: models.AgentStatusProtected,
		Enabled:         true,
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create protected agent: %v", err)
	}

	form := url.Values{}
	form.Set("name", "mutated")
	form.Set("description", "changed")
	form.Set("system_prompt", "mutated prompt")
	form.Set("model", "inherit")
	form.Set("tools_json", `["Write"]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)

	req := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("reload protected agent: %v", err)
	}
	if stored.Name != "Protected Agent" || stored.SystemPrompt != "original prompt" || len(stored.Tools) != 1 || stored.Tools[0] != "Read" {
		t.Fatalf("protected agent was mutated: %+v", stored)
	}
}

func TestHandler_UpdateAgent_ReconcilesLifecycleHooksFromMainForm(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)

	agent := &models.Agent{Name: "agent-tabs", SystemPrompt: "x", Model: "inherit", Tools: []string{}}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	oldHook := &models.AgentLifecycleHook{AgentID: agent.ID, When: models.LifecycleAfterComplete, SkillKey: "old", Enabled: true}
	if err := lifecycleRepo.CreateHook(t.Context(), oldHook); err != nil {
		t.Fatalf("create old hook: %v", err)
	}

	form := url.Values{}
	form.Set("name", "agent-tabs")
	form.Set("description", "updated")
	form.Set("system_prompt", "x")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("lifecycle_hooks_json", `[{"when":"before_run","skill_key":"new_mode","blocking":false,"enabled":true,"output_contract":"context_block"}]`)

	req := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	hooks, err := lifecycleRepo.HooksByAgent(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	if len(hooks) != 1 || hooks[0].SkillKey != "new_mode" || hooks[0].When != models.LifecycleBeforeRun {
		t.Fatalf("expected update to reconcile lifecycle hooks, got %+v", hooks)
	}
}

func TestHandler_UpdateAgent_PersistsDisabledAdvancedFields(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agent := &models.Agent{
		Name:                "Advanced Agent",
		Key:                 "advanced_agent",
		Scope:               models.AgentScopeGlobal,
		Description:         "before",
		SystemPrompt:        "work",
		Model:               "inherit",
		Tools:               []string{"Read"},
		SelectableAsPrimary: true,
		Enabled:             true,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, WriteSkills: true},
		SourceRefs:          []string{"https://before.example.test"},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Advanced Agent")
	form.Set("description", "after")
	form.Set("system_prompt", "work")
	form.Set("model", "inherit")
	form.Set("tools_json", `["Read"]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("key", "advanced_agent")
	form.Set("scope", "global")
	form.Set("selectable_as_primary", "false")
	form.Set("enabled", "false")
	form.Set("permission_defaults_json", `{"read_agents":false,"write_skills":false,"read_skills":true}`)
	form.Set("source_refs_json", `["https://after.example.test"]`)

	req := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if stored == nil {
		t.Fatal("expected updated agent to exist")
	}
	if stored.Enabled || stored.SelectableAsPrimary {
		t.Fatalf("expected disabled/non-selectable advanced fields to persist, got enabled=%v selectable=%v", stored.Enabled, stored.SelectableAsPrimary)
	}
	if stored.PermissionDefaults.ReadAgents || stored.PermissionDefaults.WriteSkills || !stored.PermissionDefaults.ReadSkills {
		t.Fatalf("permission defaults not updated: %+v", stored.PermissionDefaults)
	}
	if len(stored.SourceRefs) != 1 || stored.SourceRefs[0] != "https://after.example.test" {
		t.Fatalf("source refs not updated: %+v", stored.SourceRefs)
	}
}

func TestHandler_CreateAgent_ProjectScopedUsesExplicitURLProjectOverStoredSelection(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	globalRoot := t.TempDir()
	maintenanceSvc := service.NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	maintenanceSvc.SetLifecycleRepo(lifecycleRepo)
	maintenanceSvc.SetAgentsRootPath(globalRoot)

	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)
	h.SetAgentSkillRoot(globalRoot)
	h.SetAgentLibraryMaintenanceService(maintenanceSvc)

	proj1RepoDir := t.TempDir()
	proj1 := &models.Project{Name: "Stored Project A", RepoPath: proj1RepoDir}
	if err := h.projectSvc.Create(t.Context(), proj1); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	proj2RepoDir := t.TempDir()
	proj2 := &models.Project{Name: "Viewed Project B", RepoPath: proj2RepoDir}
	if err := h.projectSvc.Create(t.Context(), proj2); err != nil {
		t.Fatalf("create project B: %v", err)
	}
	if err := h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, proj1.ID); err != nil {
		t.Fatalf("set selected project preference: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Project B Browser Agent")
	form.Set("description", "created from the Project B agents page")
	form.Set("system_prompt", "work in project B")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("key", "project_b_browser_agent")
	form.Set("scope", "project")
	form.Set("selectable_as_primary", "true")
	form.Set("enabled", "true")
	form.Set("permission_defaults_json", `{}`)
	form.Set("source_refs_json", `[]`)

	rec := performAgentDialogRequest(t, e, http.MethodPost, "/agents?project_id="+url.QueryEscape(proj2.ID), form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := agentRepo.GetByKey(t.Context(), "project_b_browser_agent")
	if err != nil {
		t.Fatalf("load saved agent: %v", err)
	}
	if stored == nil {
		t.Fatal("expected saved project-scoped agent")
	}
	if stored.Scope != models.AgentScopeProject || stored.ProjectID != proj2.ID {
		t.Fatalf("expected Project B scoped agent, got scope=%q project_id=%q want %q", stored.Scope, stored.ProjectID, proj2.ID)
	}

	proj2AgentPath := filepath.Join(proj2RepoDir, ".openvibely", "agents", "project_b_browser_agent", "SKILLS.md")
	data, err := os.ReadFile(proj2AgentPath)
	if err != nil {
		t.Fatalf("read Project B agent declaration: %v", err)
	}
	if !strings.Contains(string(data), `project_id: `+proj2.ID) {
		t.Fatalf("expected Project B project_id in declaration, got:\n%s", data)
	}
	proj1AgentDir := filepath.Join(proj1RepoDir, ".openvibely", "agents", "project_b_browser_agent")
	if _, err := os.Stat(proj1AgentDir); !os.IsNotExist(err) {
		t.Fatalf("expected Project A to stay untouched, stat err=%v", err)
	}
}

// TestHandler_UpdateAgent_ProjectScopedWritesCorrectProjectDirectory is the
// runbook-required "Project Save Writes Correct Project Directory" test.
// It creates two projects, saves a project-scoped agent in project 2 with
// enabled=false, and asserts:
//   - proj2 SKILLS.md contains "enabled: false"
//   - proj1 does not get a copied or rewritten agent directory
//   - DB row remains disabled after a fresh ListAgents call
func TestHandler_UpdateAgent_ProjectScopedWritesCorrectProjectDirectory(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	globalRoot := t.TempDir()

	maintenanceSvc := service.NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	maintenanceSvc.SetLifecycleRepo(lifecycleRepo)
	maintenanceSvc.SetAgentsRootPath(globalRoot)

	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)
	h.SetAgentSkillRoot(globalRoot)
	h.SetAgentLibraryMaintenanceService(maintenanceSvc)

	// Two projects with distinct temp directories so cross-project isolation
	// can be asserted.
	proj1RepoDir := t.TempDir()
	proj1 := &models.Project{Name: "Project One", RepoPath: proj1RepoDir}
	if err := h.projectSvc.Create(t.Context(), proj1); err != nil {
		t.Fatalf("create proj1: %v", err)
	}

	proj2RepoDir := t.TempDir()
	proj2 := &models.Project{Name: "Project Two", RepoPath: proj2RepoDir}
	if err := h.projectSvc.Create(t.Context(), proj2); err != nil {
		t.Fatalf("create proj2: %v", err)
	}

	// Create a project-scoped agent in proj2, initially enabled.
	agent := &models.Agent{
		Name:                "Proj2 Agent",
		Key:                 "proj2-agent",
		Scope:               models.AgentScopeProject,
		ProjectID:           proj2.ID,
		Model:               "inherit",
		Enabled:             true,
		SelectableAsPrimary: true,
		Tools:               []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// PUT /agents/:id?project_id=<proj2-id> with enabled=false and scope=project.
	form := url.Values{}
	form.Set("name", "Proj2 Agent")
	form.Set("description", "project two agent")
	form.Set("system_prompt", "do something")
	form.Set("model", "inherit")
	form.Set("tools_json", `[]`)
	form.Set("plugins_json", `[]`)
	form.Set("skills_json", `[]`)
	form.Set("mcp_servers_json", `[]`)
	form.Set("key", "proj2-agent")
	form.Set("scope", "project")
	form.Set("project_id", proj2.ID)
	form.Set("selectable_as_primary", "false")
	form.Set("enabled", "false")
	form.Set("permission_defaults_json", `{}`)
	form.Set("source_refs_json", `[]`)

	req := httptest.NewRequest(http.MethodPut, "/agents/"+agent.ID+"?project_id="+proj2.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// DB row must now be disabled.
	stored, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("reload agent after PUT: %v", err)
	}
	if stored == nil {
		t.Fatal("expected updated agent to exist")
	}
	if stored.Enabled {
		t.Fatalf("expected Enabled=false after PUT, got Enabled=true")
	}

	// proj2 SKILLS.md must contain "enabled: false".
	proj2SkillsPath := filepath.Join(proj2RepoDir, ".openvibely", "agents", "proj2-agent", "SKILLS.md")
	data, readErr := os.ReadFile(proj2SkillsPath)
	if readErr != nil {
		t.Fatalf("read proj2 SKILLS.md after PUT: %v", readErr)
	}
	if !strings.Contains(string(data), "enabled: false") {
		t.Fatalf("expected proj2 SKILLS.md to contain 'enabled: false', got:\n%s", data)
	}

	// proj1 must NOT have gained an agent directory — cross-project isolation.
	proj1AgentDir := filepath.Join(proj1RepoDir, ".openvibely", "agents", "proj2-agent")
	if _, err := os.Stat(proj1AgentDir); !os.IsNotExist(err) {
		t.Fatalf("expected proj1 to NOT have a proj2-agent directory, but found one: stat err=%v", err)
	}

	// DB row must remain disabled after a fresh ListAgents call.
	// GET /agents?project_id=<proj2-id> triggers SyncRootDeclarations on the
	// proj2 root. Before the Bug 2 fix, SyncRootDeclarations would re-read
	// proj2's SKILLS.md and silently flip Enabled back to true.
	req2 := httptest.NewRequest(http.MethodGet, "/agents?project_id="+proj2.ID, nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /agents expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	stored2, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("reload agent after ListAgents: %v", err)
	}
	if stored2 == nil {
		t.Fatal("expected agent to still exist after ListAgents")
	}
	if stored2.Enabled {
		t.Fatalf("expected agent to remain Enabled=false after ListAgents (SyncRootDeclarations), but got Enabled=true")
	}
}

func TestHandler_ListAgents_RendersAdvancedStateForEditModal(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agent := &models.Agent{
		Name:                "Disabled Agent",
		Key:                 "disabled_agent",
		Scope:               models.AgentScopeProject,
		SystemPrompt:        "work",
		Model:               "inherit",
		Tools:               []string{},
		SelectableAsPrimary: false,
		Enabled:             false,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadSkills: true},
		SourceRefs:          []string{"https://source.example.test"},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-agent-key="disabled_agent"`,
		`data-agent-scope="project"`,
		`data-agent-selectable-as-primary="false"`,
		`data-agent-enabled="false"`,
		`data-agent-permission-defaults=`,
		`read_skills`,
		`data-agent-source-refs=`,
		`https://source.example.test`,
		`populateLifecycleFormFromCardData(el)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected agents page to contain %q for advanced edit hydration; body:\n%s", want, body)
		}
	}
}

func TestHandler_ListAgents_DeleteUsesDurableDeleteRequest(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agent := &models.Agent{Name: "Delete Me", Key: "delete_me", Scope: models.AgentScopeGlobal, Model: "inherit", Enabled: true}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`onclick="openDeleteAgentConfirm(this)"`,
		`async function confirmDeleteAgent()`,
		`fetch(withCurrentProject('/agents/' + encodeURIComponent(id))`,
		`method: 'DELETE'`,
		`function withCurrentProject(url)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected durable delete flow markup to contain %q; body:\n%s", want, body)
		}
	}
}

func TestParseMCPServersFromSettingsFilePreservesNestedRuntimeFields(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	data := `{
  "mcpServers": {
    "runtime-http": {
      "type": "http",
      "command": "node",
      "args": ["server.js"],
      "url": "https://example.test/mcp",
      "env": {"TOKEN": "secret"},
      "headers": {"Authorization": "Bearer secret"}
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	servers, err := parseMCPServersFromSettingsFile(path)
	if err != nil {
		t.Fatalf("parseMCPServersFromSettingsFile: %v", err)
	}
	want := []models.MCPServerConfig{{
		Name:    "runtime-http",
		Type:    "http",
		Command: []string{"node", "server.js"},
		URL:     "https://example.test/mcp",
		Env:     map[string]string{"TOKEN": "secret"},
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("servers = %#v, want %#v", servers, want)
	}
}
