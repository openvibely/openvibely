package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentplugins"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

type llmCallerFunc func(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error)

func (f llmCallerFunc) CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	return f(ctx, prompt, attachments, agent, execID, workDir)
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
	if len(agents) != 1 {
		t.Fatalf("expected one agent, got %d", len(agents))
	}
	if agents[0].Model != "gpt-5.4" {
		t.Fatalf("expected configured OpenAI model override to persist, got %q", agents[0].Model)
	}
	if len(agents[0].Plugins) != 0 {
		t.Fatalf("expected no default plugins enabled, got %v", agents[0].Plugins)
	}
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
