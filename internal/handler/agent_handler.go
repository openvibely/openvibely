package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentplugins"
	llmretry "github.com/openvibely/openvibely/internal/llm/retry"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/util"
	"github.com/openvibely/openvibely/web/templates/pages"
)

var (
	discoverPluginStateFn       = agentplugins.DiscoverState
	addMarketplaceFn            = agentplugins.AddMarketplace
	updateMarketplaceFn         = agentplugins.UpdateMarketplace
	removeMarketplaceFn         = agentplugins.RemoveMarketplace
	installPluginFn             = agentplugins.InstallPlugin
	uninstallPluginFn           = agentplugins.UninstallPlugin
	resetMarketplacesFn         = agentplugins.ResetDefaultMarketplaces
	resolvePluginBundleFn       = agentplugins.ResolveRuntimeBundle
	ensurePluginMCPRunningFn    = agentplugins.EnsurePluginMCPRunning
	reconcilePluginMCPRunningFn = agentplugins.ReconcileInstalledPluginMCPRunning
	pluginMCPRuntimeStateFn     = agentplugins.PluginMCPRuntimeState
	pluginServerNameMappingFn   = agentplugins.PluginServerNameMapping
)

type generatedAgentResponse struct {
	Name            string                   `json:"name"`
	Description     string                   `json:"description"`
	SystemPrompt    string                   `json:"system_prompt"`
	Model           string                   `json:"model"`
	Tools           []string                 `json:"tools"`
	Plugins         []string                 `json:"plugins,omitempty"`
	Skills          []models.SkillConfig     `json:"skills"`
	MCPServers      []models.MCPServerConfig `json:"mcp_servers,omitempty"`
	GenerationMode  string                   `json:"generation_mode,omitempty"` // llm | fallback
	GenerationError string                   `json:"generation_error,omitempty"`
}

var titleWordPattern = regexp.MustCompile(`[A-Za-z0-9]+`)

func defaultAgentTools() []string {
	return []string{"Read", "Grep", "Glob", "Bash"}
}

func containsAnyTerm(text string, terms ...string) bool {
	lower := strings.ToLower(text)
	for _, term := range terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func defaultAgentToolsForDescription(description string) []string {
	tools := defaultAgentTools()
	if containsAnyTerm(description, "ui", "ux", "design", "frontend", "accessibility", "playwright", "screenshot", "browser") {
		tools = append(tools, "WebFetch", "WebSearch")
	}
	if containsAnyTerm(description, "implement", "fix", "refactor", "code", "component") {
		tools = append(tools, "Edit", "Write")
	}
	return normalizeAgentTools(tools)
}

func normalizeMCPServers(servers []models.MCPServerConfig) []models.MCPServerConfig {
	seen := map[string]struct{}{}
	normalized := make([]models.MCPServerConfig, 0, len(servers))
	for _, server := range servers {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}

		cmd := make([]string, 0, len(server.Command))
		for _, part := range server.Command {
			part = strings.TrimSpace(part)
			if part != "" {
				cmd = append(cmd, part)
			}
		}

		env := map[string]string{}
		for k, v := range server.Env {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k != "" {
				env[k] = v
			}
		}

		normalized = append(normalized, models.MCPServerConfig{
			Name:    name,
			Command: cmd,
			URL:     strings.TrimSpace(server.URL),
			Env:     env,
		})
		seen[key] = struct{}{}
	}

	sort.Slice(normalized, func(i, j int) bool {
		return strings.ToLower(normalized[i].Name) < strings.ToLower(normalized[j].Name)
	})
	return normalized
}

func pickRelevantMCPServers(description string, discovered []models.MCPServerConfig) []models.MCPServerConfig {
	if len(discovered) == 0 {
		return nil
	}
	desc := strings.ToLower(description)
	keywords := []string{"playwright", "browser", "ui", "ux", "accessibility", "frontend", "screenshot"}

	var relevant []models.MCPServerConfig
	for _, server := range discovered {
		haystack := strings.ToLower(server.Name + " " + strings.Join(server.Command, " ") + " " + server.URL)
		for _, keyword := range keywords {
			if strings.Contains(desc, keyword) && strings.Contains(haystack, keyword) {
				relevant = append(relevant, server)
				break
			}
		}
	}
	if len(relevant) > 0 {
		return normalizeMCPServers(relevant)
	}
	return nil
}

func parseMCPServersFromSettingsFile(path string) ([]models.MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var root struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	servers := make([]models.MCPServerConfig, 0, len(root.MCPServers))
	for name, raw := range root.MCPServers {
		var payload map[string]interface{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			continue
		}

		var command []string
		if cmdValue, ok := payload["command"]; ok {
			switch cmd := cmdValue.(type) {
			case string:
				cmd = strings.TrimSpace(cmd)
				if cmd != "" {
					command = append(command, cmd)
				}
			case []interface{}:
				for _, item := range cmd {
					if value, ok := item.(string); ok {
						value = strings.TrimSpace(value)
						if value != "" {
							command = append(command, value)
						}
					}
				}
			}
		}
		if argsValue, ok := payload["args"]; ok {
			if args, ok := argsValue.([]interface{}); ok {
				for _, item := range args {
					if value, ok := item.(string); ok {
						value = strings.TrimSpace(value)
						if value != "" {
							command = append(command, value)
						}
					}
				}
			}
		}

		env := map[string]string{}
		if envValue, ok := payload["env"]; ok {
			if envMap, ok := envValue.(map[string]interface{}); ok {
				for k, v := range envMap {
					if str, ok := v.(string); ok {
						env[strings.TrimSpace(k)] = strings.TrimSpace(str)
					}
				}
			}
		}

		server := models.MCPServerConfig{
			Name:    strings.TrimSpace(name),
			Command: command,
			Env:     env,
		}
		if urlValue, ok := payload["url"].(string); ok {
			server.URL = strings.TrimSpace(urlValue)
		}
		servers = append(servers, server)
	}

	return normalizeMCPServers(servers), nil
}

func discoverLocalMCPServers(workDir string) []models.MCPServerConfig {
	paths := make([]string, 0, 2)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".claude", "settings.json"))
	}
	if workDir != "" {
		paths = append(paths, filepath.Join(workDir, ".claude", "settings.json"))
	}

	var combined []models.MCPServerConfig
	for _, path := range paths {
		servers, err := parseMCPServersFromSettingsFile(path)
		if err != nil {
			continue
		}
		combined = append(combined, servers...)
	}
	return normalizeMCPServers(combined)
}

func discoverLocalSkillNames(workDir string) []string {
	dirs := make([]string, 0, 2)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
	}
	if workDir != "" {
		dirs = append(dirs, filepath.Join(workDir, ".claude", "skills"))
	}

	seen := map[string]struct{}{}
	var names []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" {
				continue
			}
			skillFile := filepath.Join(dir, name, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}
			key := strings.ToLower(name)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func normalizeAgentModel(model string, allowed map[string]struct{}) string {
	normalized := strings.TrimSpace(model)
	if normalized == "" {
		return "inherit"
	}
	if strings.EqualFold(normalized, "inherit") {
		return "inherit"
	}
	if _, ok := allowed[normalized]; ok {
		return normalized
	}
	return "inherit"
}

func normalizeAgentTools(tools []string) []string {
	toolNameMap := make(map[string]string, len(models.AllAgentTools))
	for _, tool := range models.AllAgentTools {
		toolNameMap[strings.ToLower(tool)] = tool
	}

	seen := make(map[string]struct{}, len(tools))
	normalized := make([]string, 0, len(tools))
	for _, tool := range tools {
		trimmed := strings.TrimSpace(tool)
		if trimmed == "" {
			continue
		}
		canonical, ok := toolNameMap[strings.ToLower(trimmed)]
		if !ok {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}

	if len(normalized) == 0 {
		return defaultAgentTools()
	}
	return normalized
}

func normalizeAgentPlugins(plugins []string) []string {
	return agentplugins.NormalizePluginIDs(plugins)
}

func normalizeGeneratedSkills(skills []models.SkillConfig, description string) []models.SkillConfig {
	normalized := make([]models.SkillConfig, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		content := strings.TrimSpace(skill.Content)
		if name == "" || content == "" {
			continue
		}
		normalized = append(normalized, models.SkillConfig{
			Name:        name,
			Description: strings.TrimSpace(skill.Description),
			Tools:       strings.TrimSpace(skill.Tools),
			Content:     content,
		})
		if len(normalized) >= 5 {
			break
		}
	}

	if len(normalized) == 0 {
		return buildFallbackSkills(description)
	}
	return normalized
}

func titleCaseWords(text string, maxWords int) string {
	parts := titleWordPattern.FindAllString(text, -1)
	if len(parts) == 0 {
		return "Custom Agent"
	}
	if len(parts) > maxWords {
		parts = parts[:maxWords]
	}
	for i := range parts {
		lower := strings.ToLower(parts[i])
		parts[i] = strings.ToUpper(lower[:1]) + lower[1:]
	}
	return strings.Join(parts, " ")
}

func buildFallbackSkills(description string) []models.SkillConfig {
	if containsAnyTerm(description, "ui", "ux", "design", "frontend", "accessibility", "playwright", "screenshot", "browser") {
		return []models.SkillConfig{
			{
				Name:        "ui-component-recon",
				Description: "Inspect target components and usage context before reviewing.",
				Tools:       "Read, Grep, Glob, Bash, WebFetch",
				Content: `Map the component's variants, states, and surrounding layout context first.
- Identify entry points and props.
- Capture constraints from design system tokens, spacing scale, and typography.
- Note interaction flows and error/empty/loading states before judging visuals.`,
			},
			{
				Name:        "playwright-visual-a11y-audit",
				Description: "Run screenshot-driven and accessibility-focused checks with browser tooling.",
				Tools:       "Bash, WebSearch, WebFetch",
				Content: `When browser automation is available (e.g., Playwright MCP), run a structured audit:
- Capture screenshots for default, hover, focus, active, disabled, and responsive breakpoints.
- Check keyboard navigation, focus order, focus visibility, and semantic roles.
- Flag contrast, touch target size, and motion/animation accessibility risks.
- Record concrete evidence tied to specific UI states.`,
			},
			{
				Name:        "actionable-design-feedback",
				Description: "Convert findings into prioritized, implementable recommendations.",
				Tools:       "Read, Edit, Write",
				Content: `Deliver feedback in priority order with concrete fixes:
- Severity: critical / high / medium / low.
- Problem: what users experience.
- Why: UX or accessibility principle being violated.
- Fix: exact implementation guidance (layout, copy, affordance, color, spacing, semantics).
- Validation: how to verify the fix after changes.`,
			},
		}
	}

	return []models.SkillConfig{
		{
			Name:        "scope-and-plan",
			Description: "Clarify scope and constraints before execution.",
			Tools:       "Read, Grep, Glob, Bash",
			Content:     "Identify objective, constraints, and key files first. Produce a concise plan before making changes.",
		},
		{
			Name:        "execute-and-verify",
			Description: "Perform focused execution with clear validation.",
			Tools:       "Read, Edit, Write, Bash",
			Content:     "Apply minimal, targeted changes and validate outcomes. Summarize results, risks, and next steps clearly.",
		},
	}
}

func fallbackGeneratedAgent(description string, discoveredMCP []models.MCPServerConfig) generatedAgentResponse {
	tools := defaultAgentToolsForDescription(description)
	relevantMCP := pickRelevantMCPServers(description, discoveredMCP)
	systemPrompt := fmt.Sprintf(`You are a specialist execution agent for: %s

Core behavior:
- Start by understanding context and constraints before acting.
- Be explicit about assumptions, tradeoffs, and risk.
- Prefer concrete, high-signal output over generic advice.
- If browser or MCP tools are available, use them to gather evidence.
- Provide actionable recommendations with clear priority and validation steps.`, description)

	return generatedAgentResponse{
		Name:         titleCaseWords(description, 3),
		Description:  description,
		SystemPrompt: systemPrompt,
		Model:        "inherit",
		Tools:        tools,
		Skills:       buildFallbackSkills(description),
		MCPServers:   relevantMCP,
	}
}

func normalizeGeneratedAgent(input generatedAgentResponse, description string, discoveredMCP []models.MCPServerConfig, allowedModels map[string]struct{}) generatedAgentResponse {
	normalized := input
	if strings.TrimSpace(normalized.Name) == "" {
		normalized.Name = titleCaseWords(description, 3)
	}
	if strings.TrimSpace(normalized.Description) == "" {
		normalized.Description = description
	}
	if strings.TrimSpace(normalized.SystemPrompt) == "" {
		normalized.SystemPrompt = fmt.Sprintf("You are a specialized agent focused on: %s.", description)
	}
	normalized.Model = normalizeAgentModel(normalized.Model, allowedModels)
	normalized.Tools = normalizeAgentTools(normalized.Tools)
	normalized.Skills = normalizeGeneratedSkills(normalized.Skills, description)
	normalized.MCPServers = normalizeMCPServers(normalized.MCPServers)
	if len(normalized.MCPServers) == 0 {
		normalized.MCPServers = pickRelevantMCPServers(description, discoveredMCP)
	}
	return normalized
}

func buildAgentGenerationPrompt(description string) string {
	return fmt.Sprintf(`Generate an OpenVibely agent definition.

User request:
"%s"

Return ONLY one JSON object with exactly these keys:
{
  "name": "string",
  "description": "string",
  "system_prompt": "string",
  "model": "inherit|configured model id from /models (e.g., claude-sonnet-4-5-20250929, gpt-5.4)",
  "tools": ["Read","Write","Edit","Bash","Glob","Grep","WebFetch","WebSearch","NotebookEdit"],
  "skills": [
    {"name":"string","description":"string","tools":"comma-separated tools","content":"string"}
  ],
  "mcp_servers": [
    {"name":"string","command":["binary","arg1"],"url":"","env":{"KEY":"VALUE"}}
  ]
}

Requirements:
- Produce 2-4 high-quality skills with concrete, actionable content.
- Avoid generic rewording; include practical workflow steps.
- If UI/UX or browser testing is requested, include at least one skill dedicated to screenshot-based and accessibility review.
- Treat this as pure text-in/JSON-out generation. Do not use or invoke tools, plugins, MCP servers, or shell commands.
- Do not reference plugin installation state, plugin IDs, or tool catalogs.
- No markdown, no explanations, no trailing text.`, description)
}

func shortGenerationError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	msg = strings.ReplaceAll(msg, "\n", " | ")
	if len(msg) > 280 {
		return msg[:280] + "..."
	}
	return msg
}

func parseSelectedPlugins(pluginsJSON string) []string {
	var selected []string
	pluginsJSON = strings.TrimSpace(pluginsJSON)
	if pluginsJSON == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(pluginsJSON), &selected); err != nil {
		return nil
	}
	return normalizeAgentPlugins(selected)
}

func validateInstalledPluginSelection(state models.PluginState, selected []string) error {
	if len(selected) == 0 {
		return nil
	}
	installed := map[string]struct{}{}
	for _, p := range state.Installed {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		if id != "" {
			installed[id] = struct{}{}
		}
	}
	if len(installed) == 0 {
		return fmt.Errorf("selected plugins are not installed: %s", strings.Join(selected, ", "))
	}
	var invalid []string
	for _, id := range selected {
		key := strings.ToLower(strings.TrimSpace(id))
		if key == "" {
			continue
		}
		if _, ok := installed[key]; !ok {
			invalid = append(invalid, id)
		}
	}
	if len(invalid) > 0 {
		sort.Strings(invalid)
		return fmt.Errorf("selected plugins are not installed: %s", strings.Join(invalid, ", "))
	}
	return nil
}

func isPluginInstalled(state models.PluginState, pluginID string) bool {
	key := strings.ToLower(strings.TrimSpace(pluginID))
	if key == "" {
		return false
	}
	for _, p := range state.Installed {
		if strings.ToLower(strings.TrimSpace(p.ID)) == key {
			return true
		}
	}
	return false
}

func containsPluginID(ids []string, pluginID string) bool {
	key := strings.ToLower(strings.TrimSpace(pluginID))
	if key == "" {
		return false
	}
	for _, id := range ids {
		if strings.ToLower(strings.TrimSpace(id)) == key {
			return true
		}
	}
	return false
}

func (h *Handler) enablePluginForAgent(ctx context.Context, agentID, pluginID string) error {
	agentID = strings.TrimSpace(agentID)
	pluginID = strings.TrimSpace(pluginID)
	if agentID == "" {
		return fmt.Errorf("agent_id is required")
	}
	if pluginID == "" {
		return fmt.Errorf("plugin_id is required")
	}
	if h.agentRepo == nil {
		return fmt.Errorf("agent repository unavailable")
	}

	agent, err := h.agentRepo.GetByID(ctx, agentID)
	if err != nil {
		return fmt.Errorf("load agent: %w", err)
	}
	if agent == nil {
		return fmt.Errorf("agent not found")
	}
	if containsPluginID(agent.Plugins, pluginID) {
		return nil
	}

	nextPlugins := append(append([]string{}, agent.Plugins...), pluginID)
	validated, err := h.normalizeAndValidateSelectedPlugins(ctx, nextPlugins)
	if err != nil {
		return err
	}
	agent.Plugins = validated
	if err := h.agentRepo.Update(ctx, agent); err != nil {
		return fmt.Errorf("update agent plugins: %w", err)
	}
	return nil
}

func (h *Handler) installPluginForModalAgent(c echo.Context, pluginID, scope, agentID string) (warning string, installErr error, enableErr error) {
	pluginID = strings.TrimSpace(pluginID)
	agentID = strings.TrimSpace(agentID)
	if pluginID == "" {
		return "", fmt.Errorf("plugin_id is required"), nil
	}

	alreadyInstalled := false
	if state, err := discoverPluginStateFn(c.Request().Context()); err == nil {
		alreadyInstalled = isPluginInstalled(state, pluginID)
	}
	if !alreadyInstalled {
		if err := installPluginFn(c.Request().Context(), pluginID, scope); err != nil {
			log.Printf("[handler] InstallPlugin error: %v", err)
			projectID, _ := h.getCurrentProjectID(c)
			h.createPluginRuntimeAlert(projectID, fmt.Sprintf("Plugin install failed: %s", pluginID), shortGenerationError(err))
			return "", err, nil
		}
	}

	workDir, err := os.Getwd()
	if err != nil || strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	warning = ""
	if err := ensurePluginMCPRunningFn(startCtx, []string{pluginID}, workDir); err != nil {
		warning = shortGenerationError(err)
		log.Printf("[handler] InstallPlugin MCP startup warning plugin=%q: %v", pluginID, err)
		projectID, _ := h.getCurrentProjectID(c)
		h.createPluginRuntimeAlert(projectID, fmt.Sprintf("Plugin MCP startup failed: %s", pluginID), warning)
	}

	if agentID == "" {
		return warning, nil, nil
	}
	if err := h.enablePluginForAgent(c.Request().Context(), agentID, pluginID); err != nil {
		log.Printf("[handler] InstallPlugin enable warning plugin=%q agent=%q: %v", pluginID, agentID, err)
		projectID, _ := h.getCurrentProjectID(c)
		h.createPluginRuntimeAlert(projectID, fmt.Sprintf("Plugin enable failed: %s", pluginID), shortGenerationError(err))
		return warning, nil, fmt.Errorf("could not enable plugin for agent: %w", err)
	}
	return warning, nil, nil
}

func (h *Handler) normalizeAndValidateSelectedPlugins(ctx context.Context, selected []string) ([]string, error) {
	normalized := normalizeAgentPlugins(selected)
	if len(normalized) == 0 {
		return []string{}, nil
	}
	state, err := discoverPluginStateFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not validate installed plugins: %w", err)
	}
	if err := validateInstalledPluginSelection(state, normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

const (
	generateAgentTimeout               = 90 * time.Second
	generateAgentTransientRetryCount   = 2
	generateAgentTransientRetryDelay   = 750 * time.Millisecond
	generateAgentRepairOutputMaxLength = 8000
)

type generateAgentMalformedResponseError struct {
	modelName string
	cause     error
}

func (e *generateAgentMalformedResponseError) Error() string {
	if e == nil {
		return "model returned malformed JSON output"
	}
	if strings.TrimSpace(e.modelName) == "" {
		return "model returned malformed JSON output"
	}
	return fmt.Sprintf("model %s returned malformed JSON output", e.modelName)
}

func (e *generateAgentMalformedResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func isGenerateAgentRequestCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "request canceled") || strings.Contains(msg, "context canceled")
}

func isGenerateAgentTransientTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "context deadline exceeded") {
		return true
	}
	return llmretry.IsRetryable(err)
}

func isGenerateAgentMalformedModelOutputError(err error) bool {
	if err == nil {
		return false
	}
	var malformedErr *generateAgentMalformedResponseError
	if errors.As(err, &malformedErr) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "invalid json from model") {
		return true
	}
	if strings.Contains(msg, "subtype=invalid_json") {
		return true
	}
	if strings.Contains(msg, "malformed json") || strings.Contains(msg, "malformed output") {
		return true
	}
	if strings.Contains(msg, "invalid character") && strings.Contains(msg, "looking for beginning of value") {
		return true
	}
	return false
}

func buildGenerateAgentStrictRetryPrompt(basePrompt string, parseErr error) string {
	hint := "malformed or non-JSON output"
	if parseErr != nil {
		hint = shortGenerationError(parseErr)
	}
	return basePrompt + fmt.Sprintf(`

IMPORTANT RETRY INSTRUCTION:
- The previous attempt failed because the response was %s.
- Return ONLY strict JSON (no prose, no markdown fences, no prefix/suffix).
- The first non-whitespace character must be '{' and the last must be '}'.
- Do not execute tools, plugins, or MCP actions during this retry; return JSON directly.
- Include exactly these top-level keys: "name", "description", "system_prompt", "model", "tools", "skills", "mcp_servers".`, hint)
}

func buildGenerateAgentUserError(modelName string, err error) string {
	if isGenerateAgentRequestCanceled(err) {
		return fmt.Sprintf("%s: generation canceled because the request ended", modelName)
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
		return fmt.Sprintf("%s: generation timed out after %s (default model request exceeded deadline)", modelName, generateAgentTimeout)
	}
	if isGenerateAgentMalformedModelOutputError(err) {
		return fmt.Sprintf("%s: model output was malformed, so a local template draft was used (try Generate again; switch the default model if this repeats)", modelName)
	}
	return fmt.Sprintf("%s: %s", modelName, shortGenerationError(err))
}

func extractBalancedJSONObjectCandidates(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	const maxObjects = 8
	objects := make([]string, 0, maxObjects)
	depth := 0
	start := -1
	inString := false
	escaped := false

	for i := 0; i < len(input); i++ {
		ch := input[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				objects = append(objects, input[start:i+1])
				start = -1
				if len(objects) >= maxObjects {
					return objects
				}
			}
		}
	}

	return objects
}

func isGeneratedAgentPayloadShape(payload map[string]json.RawMessage) bool {
	if len(payload) == 0 {
		return false
	}
	expectedKeys := []string{"name", "description", "system_prompt", "model", "tools", "skills", "mcp_servers"}
	matches := 0
	hasSystemPrompt := false
	for _, key := range expectedKeys {
		raw, ok := payload[key]
		if !ok || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
			continue
		}
		matches++
		if key == "system_prompt" {
			hasSystemPrompt = true
		}
	}
	if hasSystemPrompt {
		return true
	}
	return matches >= 2
}

func parseGeneratedAgentJSONPayload(payload string) (generatedAgentResponse, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return generatedAgentResponse{}, fmt.Errorf("empty JSON payload")
	}

	var shape map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &shape); err != nil {
		return generatedAgentResponse{}, err
	}
	if !isGeneratedAgentPayloadShape(shape) {
		return generatedAgentResponse{}, fmt.Errorf("JSON payload does not match generated agent schema")
	}

	var generated generatedAgentResponse
	if err := json.Unmarshal([]byte(payload), &generated); err != nil {
		return generatedAgentResponse{}, err
	}
	return generated, nil
}

func stripGenerateAgentToolWrappers(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}

	lines := strings.Split(trimmed, "\n")
	start := 0
	for start < len(lines) {
		line := strings.TrimSpace(lines[start])
		if line == "" {
			start++
			continue
		}
		if strings.HasPrefix(line, "[Using tool:") || strings.HasPrefix(line, "[Tool ") {
			start++
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

func parseGeneratedAgentOutput(output string) (generatedAgentResponse, error) {
	cleaned := strings.TrimSpace(util.StripMarkdownFences(output))
	cleaned = stripGenerateAgentToolWrappers(cleaned)
	if cleaned == "" {
		return generatedAgentResponse{}, fmt.Errorf("empty model output")
	}

	candidates := make([]string, 0, 16)
	seen := map[string]struct{}{}
	addCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	addCandidate(cleaned)
	addCandidate(util.ExtractJSONObject(cleaned))
	for _, candidate := range extractBalancedJSONObjectCandidates(cleaned) {
		addCandidate(candidate)
	}

	var lastErr error
	for _, candidate := range candidates {
		if generated, err := parseGeneratedAgentJSONPayload(candidate); err == nil {
			return generated, nil
		} else {
			lastErr = err
		}

		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal([]byte(candidate), &wrapper); err != nil {
			continue
		}
		for _, key := range []string{"agent", "data", "result", "output"} {
			raw, ok := wrapper[key]
			if !ok {
				continue
			}
			nested := strings.TrimSpace(string(raw))
			if nested == "" {
				continue
			}
			if strings.HasPrefix(nested, "\"") {
				var nestedStr string
				if err := json.Unmarshal(raw, &nestedStr); err == nil {
					nested = strings.TrimSpace(util.ExtractJSONObject(nestedStr))
					if nested == "" {
						nested = strings.TrimSpace(util.StripMarkdownFences(nestedStr))
					}
				}
			} else {
				extracted := util.ExtractJSONObject(nested)
				if extracted != "" {
					nested = extracted
				}
			}
			if nested == "" {
				continue
			}
			if generated, err := parseGeneratedAgentJSONPayload(nested); err == nil {
				return generated, nil
			} else {
				lastErr = err
			}
		}
	}

	if lastErr != nil {
		return generatedAgentResponse{}, lastErr
	}
	return generatedAgentResponse{}, fmt.Errorf("no JSON object found in model output")
}

func buildGenerateAgentRepairPrompt(rawOutput string) string {
	rawOutput = strings.TrimSpace(rawOutput)
	if rawOutput == "" {
		rawOutput = "(empty model output)"
	}
	rawOutput = util.TruncateWithSuffix(rawOutput, generateAgentRepairOutputMaxLength, "...[truncated]")

	return fmt.Sprintf(`The previous response was not valid JSON for an OpenVibely agent definition.

Rewrite it into strict JSON.
Return ONLY one JSON object with these keys:
- "name"
- "description"
- "system_prompt"
- "model"
- "tools"
- "skills"
- "mcp_servers"

Requirements:
- No markdown fences
- No prefix or suffix text
- "tools" must be an array of strings
- Do not execute tools, plugins, or MCP actions while rewriting.
- "skills" must be an array of objects with keys "name", "description", "tools", "content"
- "mcp_servers" must be an array of objects with keys "name", "command", "url", "env"
- Use "inherit" when model is unknown

Malformed source output:
<<<SOURCE
%s
SOURCE`, rawOutput)
}

func (h *Handler) repairGenerateAgentJSON(ctx context.Context, rawOutput string, cfg models.LLMConfig, workDir string) (generatedAgentResponse, error) {
	repairPrompt := buildGenerateAgentRepairPrompt(rawOutput)
	repairedOutput, _, err := h.llmSvc.CallAgentDirectNoTools(ctx, repairPrompt, nil, cfg, workDir)
	if err != nil {
		return generatedAgentResponse{}, fmt.Errorf("repair call failed: %w", err)
	}
	generated, err := parseGeneratedAgentOutput(repairedOutput)
	if err != nil {
		// Log the first 500 chars of the repaired output that still failed
		outputSample := repairedOutput
		if len(outputSample) > 500 {
			outputSample = outputSample[:500] + "..."
		}
		log.Printf("[handler] GenerateAgent repair returned malformed JSON model=%s err=%v output=%q", cfg.Name, err, outputSample)
		return generatedAgentResponse{}, fmt.Errorf("repair parse failed: %w", err)
	}
	return generated, nil
}

func (h *Handler) generateAgentWithLLM(ctx context.Context, prompt string, cfg models.LLMConfig, workDir string) (generatedAgentResponse, error) {
	generateCtx, cancel := context.WithTimeout(ctx, generateAgentTimeout)
	defer cancel()

	activePrompt := prompt
	var lastErr error
	for attempt := 1; attempt <= generateAgentTransientRetryCount; attempt++ {
		output, _, err := h.llmSvc.CallAgentDirectNoTools(generateCtx, activePrompt, nil, cfg, workDir)
		if err == nil {
			generated, parseErr := parseGeneratedAgentOutput(output)
			if parseErr == nil {
				if attempt > 1 {
					log.Printf("[handler] GenerateAgent default model generation succeeded after retry attempt=%d/%d model=%s", attempt, generateAgentTransientRetryCount, cfg.Name)
				}
				return generated, nil
			}

			// Log the first 500 chars of the malformed output to diagnose the issue
			outputSample := output
			if len(outputSample) > 500 {
				outputSample = outputSample[:500] + "..."
			}
			log.Printf("[handler] GenerateAgent malformed JSON output attempt=%d/%d model=%s err=%v output=%q", attempt, generateAgentTransientRetryCount, cfg.Name, parseErr, outputSample)
			repaired, repairErr := h.repairGenerateAgentJSON(generateCtx, output, cfg, workDir)
			if repairErr == nil {
				log.Printf("[handler] GenerateAgent JSON repair succeeded attempt=%d/%d model=%s", attempt, generateAgentTransientRetryCount, cfg.Name)
				return repaired, nil
			}

			log.Printf("[handler] GenerateAgent JSON repair failed attempt=%d/%d model=%s err=%v", attempt, generateAgentTransientRetryCount, cfg.Name, repairErr)
			lastErr = &generateAgentMalformedResponseError{modelName: cfg.Name, cause: parseErr}
			if generateCtx.Err() != nil {
				return generatedAgentResponse{}, generateCtx.Err()
			}
			if attempt >= generateAgentTransientRetryCount {
				return generatedAgentResponse{}, lastErr
			}
			activePrompt = buildGenerateAgentStrictRetryPrompt(prompt, parseErr)
			select {
			case <-generateCtx.Done():
				return generatedAgentResponse{}, generateCtx.Err()
			case <-time.After(generateAgentTransientRetryDelay):
			}
			continue
		}

		if isGenerateAgentMalformedModelOutputError(err) {
			log.Printf("[handler] GenerateAgent provider returned malformed JSON error attempt=%d/%d model=%s err=%v", attempt, generateAgentTransientRetryCount, cfg.Name, err)
			repaired, repairErr := h.repairGenerateAgentJSON(generateCtx, err.Error(), cfg, workDir)
			if repairErr == nil {
				log.Printf("[handler] GenerateAgent provider-error JSON repair succeeded attempt=%d/%d model=%s", attempt, generateAgentTransientRetryCount, cfg.Name)
				return repaired, nil
			}
			log.Printf("[handler] GenerateAgent provider-error JSON repair failed attempt=%d/%d model=%s err=%v", attempt, generateAgentTransientRetryCount, cfg.Name, repairErr)
			lastErr = &generateAgentMalformedResponseError{modelName: cfg.Name, cause: err}
			if generateCtx.Err() != nil {
				return generatedAgentResponse{}, generateCtx.Err()
			}
			if attempt >= generateAgentTransientRetryCount {
				return generatedAgentResponse{}, lastErr
			}
			activePrompt = buildGenerateAgentStrictRetryPrompt(prompt, err)
			select {
			case <-generateCtx.Done():
				return generatedAgentResponse{}, generateCtx.Err()
			case <-time.After(generateAgentTransientRetryDelay):
			}
			continue
		}

		lastErr = err
		if generateCtx.Err() != nil {
			return generatedAgentResponse{}, generateCtx.Err()
		}
		if isGenerateAgentRequestCanceled(err) {
			return generatedAgentResponse{}, err
		}
		if attempt >= generateAgentTransientRetryCount || !isGenerateAgentTransientTimeoutError(err) {
			return generatedAgentResponse{}, err
		}

		log.Printf("[handler] GenerateAgent transient generation failure attempt=%d/%d model=%s prompt_len=%d err=%v", attempt, generateAgentTransientRetryCount, cfg.Name, len(activePrompt), err)
		select {
		case <-generateCtx.Done():
			return generatedAgentResponse{}, generateCtx.Err()
		case <-time.After(generateAgentTransientRetryDelay):
		}
	}

	if lastErr != nil {
		return generatedAgentResponse{}, lastErr
	}
	return generatedAgentResponse{}, fmt.Errorf("generation failed for model %s", cfg.Name)
}

func (h *Handler) GenerateAgent(c echo.Context) error {
	description := strings.TrimSpace(c.FormValue("description"))
	selectedPlugins := parseSelectedPlugins(c.FormValue("plugins_json"))
	if description == "" {
		var body struct {
			Description string   `json:"description"`
			Plugins     []string `json:"plugins"`
		}
		if err := json.NewDecoder(c.Request().Body).Decode(&body); err == nil {
			description = strings.TrimSpace(body.Description)
			selectedPlugins = normalizeAgentPlugins(body.Plugins)
		}
	}
	if description == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "description is required")
	}

	validatedPlugins, err := h.normalizeAndValidateSelectedPlugins(c.Request().Context(), selectedPlugins)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	selectedPlugins = validatedPlugins

	workDir, _ := os.Getwd()
	discoveredMCP := discoverLocalMCPServers(workDir)
	generated := fallbackGeneratedAgent(description, discoveredMCP)
	generated.GenerationMode = "fallback"
	generated.Plugins = selectedPlugins
	allowedModels := map[string]struct{}{}

	if h.llmSvc == nil {
		generated.GenerationError = "LLM service unavailable"
		return c.JSON(http.StatusOK, normalizeGeneratedAgent(generated, description, discoveredMCP, allowedModels))
	}

	if h.llmConfigRepo == nil {
		generated.GenerationError = "Model configuration repository unavailable"
		return c.JSON(http.StatusOK, normalizeGeneratedAgent(generated, description, discoveredMCP, allowedModels))
	}

	modelConfigs, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		generated.GenerationError = fmt.Sprintf("Could not load model configurations: %s", shortGenerationError(err))
		return c.JSON(http.StatusOK, normalizeGeneratedAgent(generated, description, discoveredMCP, allowedModels))
	}
	for _, cfg := range modelConfigs {
		modelID := strings.TrimSpace(cfg.Model)
		if modelID == "" {
			continue
		}
		allowedModels[modelID] = struct{}{}
	}

	defaultModel, err := h.llmConfigRepo.GetDefault(c.Request().Context())
	if err != nil {
		generated.GenerationError = fmt.Sprintf("Could not load default model configuration: %s", shortGenerationError(err))
		return c.JSON(http.StatusOK, normalizeGeneratedAgent(generated, description, discoveredMCP, allowedModels))
	}
	if defaultModel == nil {
		generated.GenerationError = "No default model configuration available"
		return c.JSON(http.StatusOK, normalizeGeneratedAgent(generated, description, discoveredMCP, allowedModels))
	}

	prompt := buildAgentGenerationPrompt(description)
	startedAt := time.Now()
	llmGenerated, err := h.generateAgentWithLLM(c.Request().Context(), prompt, *defaultModel, workDir)
	duration := time.Since(startedAt)
	if err != nil {
		generated.GenerationError = buildGenerateAgentUserError(defaultModel.Name, err)
		if isGenerateAgentRequestCanceled(err) {
			log.Printf("[handler] GenerateAgent default model generation canceled model=%s provider=%s prompt_len=%d duration=%s err=%v", defaultModel.Name, defaultModel.Provider, len(prompt), duration, err)
		} else if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
			log.Printf("[handler] GenerateAgent default model generation timed out model=%s provider=%s prompt_len=%d timeout=%s duration=%s err=%v", defaultModel.Name, defaultModel.Provider, len(prompt), generateAgentTimeout, duration, err)
		} else {
			log.Printf("[handler] GenerateAgent default model generation failed model=%s provider=%s prompt_len=%d duration=%s err=%v", defaultModel.Name, defaultModel.Provider, len(prompt), duration, err)
		}
		return c.JSON(http.StatusOK, normalizeGeneratedAgent(generated, description, discoveredMCP, allowedModels))
	}

	normalized := normalizeGeneratedAgent(llmGenerated, description, discoveredMCP, allowedModels)
	normalized.Plugins = selectedPlugins
	normalized.GenerationMode = "llm"
	normalized.GenerationError = ""
	return c.JSON(http.StatusOK, normalized)
}

func writePluginJSONError(c echo.Context, err error) error {
	return c.JSON(http.StatusBadRequest, map[string]interface{}{
		"ok":    false,
		"error": shortGenerationError(err),
	})
}

func (h *Handler) createPluginRuntimeAlert(projectID, title, message string) {
	if h.alertSvc == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	if title == "" || message == "" {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	alert := &models.Alert{
		ProjectID: projectID,
		Type:      models.AlertCustom,
		Severity:  models.SeverityError,
		Title:     util.TruncateWithSuffix(title, 140, "..."),
		Message:   util.TruncateWithSuffix(message, 1000, "..."),
	}
	if err := h.alertSvc.Create(bgCtx, alert); err != nil {
		log.Printf("[handler] createPluginRuntimeAlert error: %v", err)
	}
}

func decodePluginRequestBody(r io.Reader, dest interface{}) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	return dec.Decode(dest)
}

func (h *Handler) GetPluginState(c echo.Context) error {
	ctx := c.Request().Context()
	state, err := discoverPluginStateFn(ctx)
	runtime := pluginMCPRuntimeStateFn()
	if err != nil {
		log.Printf("[handler] GetPluginState error: %v", err)
		return c.JSON(http.StatusOK, map[string]interface{}{
			"marketplaces": []models.PluginMarketplace{},
			"installed":    []models.InstalledPlugin{},
			"available":    []models.AvailablePlugin{},
			"runtime":      enrichRuntimePluginIDs(ctx, runtime, nil),
			"error":        shortGenerationError(err),
		})
	}
	state.Runtime = enrichRuntimePluginIDs(ctx, runtime, state.Installed)
	return c.JSON(http.StatusOK, state)
}

// enrichRuntimePluginIDs annotates runtime entries with the owning plugin ID
// so the frontend can match runtime status to installed plugin rows.
func enrichRuntimePluginIDs(ctx context.Context, runtime []models.PluginRuntimeMCP, installed []models.InstalledPlugin) []models.PluginRuntimeMCP {
	if len(runtime) == 0 || len(installed) == 0 {
		return runtime
	}
	mapping := pluginServerNameMappingFn(ctx, installed)
	for i := range runtime {
		key := strings.ToLower(strings.TrimSpace(runtime[i].Name))
		if pid, ok := mapping[key]; ok {
			runtime[i].PluginID = pid
		}
	}
	return runtime
}

func (h *Handler) AddPluginMarketplace(c echo.Context) error {
	var payload struct {
		Source string `json:"source"`
		Scope  string `json:"scope"`
	}
	if err := decodePluginRequestBody(c.Request().Body, &payload); err != nil {
		return writePluginJSONError(c, err)
	}
	if err := addMarketplaceFn(c.Request().Context(), payload.Source, payload.Scope); err != nil {
		log.Printf("[handler] AddPluginMarketplace error: %v", err)
		return writePluginJSONError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) UpdatePluginMarketplace(c echo.Context) error {
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return writePluginJSONError(c, fmt.Errorf("name is required"))
	}
	if err := updateMarketplaceFn(c.Request().Context(), name); err != nil {
		log.Printf("[handler] UpdatePluginMarketplace error: %v", err)
		return writePluginJSONError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) DeletePluginMarketplace(c echo.Context) error {
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		return writePluginJSONError(c, fmt.Errorf("name is required"))
	}
	if err := removeMarketplaceFn(c.Request().Context(), name); err != nil {
		log.Printf("[handler] DeletePluginMarketplace error: %v", err)
		return writePluginJSONError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) InstallPlugin(c echo.Context) error {
	var payload struct {
		PluginID string `json:"plugin_id"`
		Scope    string `json:"scope"`
		AgentID  string `json:"agent_id"`
	}
	if err := decodePluginRequestBody(c.Request().Body, &payload); err != nil {
		return writePluginJSONError(c, err)
	}

	warning, installErr, enableErr := h.installPluginForModalAgent(c, payload.PluginID, payload.Scope, payload.AgentID)
	if installErr != nil {
		return writePluginJSONError(c, installErr)
	}

	resp := map[string]interface{}{"ok": true}
	if warning != "" {
		resp["warning"] = warning
	}
	if strings.TrimSpace(payload.AgentID) != "" {
		resp["enabled_for_agent"] = enableErr == nil
		if enableErr != nil {
			resp["enable_error"] = shortGenerationError(enableErr)
		}
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *Handler) UninstallPlugin(c echo.Context) error {
	var payload struct {
		PluginID string `json:"plugin_id"`
	}
	if err := decodePluginRequestBody(c.Request().Body, &payload); err != nil {
		return writePluginJSONError(c, err)
	}
	if err := uninstallPluginFn(c.Request().Context(), payload.PluginID); err != nil {
		log.Printf("[handler] UninstallPlugin error: %v", err)
		projectID, _ := h.getCurrentProjectID(c)
		h.createPluginRuntimeAlert(projectID, fmt.Sprintf("Plugin uninstall failed: %s", payload.PluginID), shortGenerationError(err))
		return writePluginJSONError(c, err)
	}

	workDir, err := os.Getwd()
	if err != nil || strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := reconcilePluginMCPRunningFn(reconcileCtx, workDir); err != nil {
		warning := shortGenerationError(err)
		log.Printf("[handler] UninstallPlugin MCP reconcile warning plugin=%q: %v", payload.PluginID, err)
		projectID, _ := h.getCurrentProjectID(c)
		h.createPluginRuntimeAlert(projectID, fmt.Sprintf("Plugin MCP reconcile warning: %s", payload.PluginID), warning)
	}

	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) ResetPluginMarketplaces(c echo.Context) error {
	if err := resetMarketplacesFn(c.Request().Context()); err != nil {
		log.Printf("[handler] ResetPluginMarketplaces error: %v", err)
		return writePluginJSONError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func buildAgentModelOptions(configs []models.LLMConfig) []models.AgentModelOption {
	seen := make(map[string]struct{}, len(configs))
	options := make([]models.AgentModelOption, 0, len(configs))
	for _, cfg := range configs {
		value := strings.TrimSpace(cfg.Model)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		label := strings.TrimSpace(cfg.Name)
		if label == "" {
			label = value
		}
		options = append(options, models.AgentModelOption{Value: value, Label: label})
		seen[value] = struct{}{}
	}
	return options
}

func (h *Handler) ListAgents(c echo.Context) error {
	isHtmx := isHTMX(c)
	log.Printf("[handler] ListAgents requested htmx=%v", isHtmx)

	agents, err := h.agentRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ListAgents error: %v", err)
		return err
	}
	log.Printf("[handler] ListAgents found %d agents", len(agents))

	modelConfigs, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ListAgents listing model configs failed: %v", err)
		return err
	}
	modelOptions := buildAgentModelOptions(modelConfigs)

	if isHtmx {
		return render(c, http.StatusOK, pages.AgentsContent(agents, modelOptions))
	}

	currentProjectID, _ := h.getCurrentProjectID(c)
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.Agents(projects, currentProjectID, agents, modelOptions))
}

func (h *Handler) CreateAgent(c echo.Context) error {
	agent := models.Agent{
		Name:         c.FormValue("name"),
		Description:  c.FormValue("description"),
		SystemPrompt: c.FormValue("system_prompt"),
		Model:        c.FormValue("model"),
	}

	allowedModels := map[string]struct{}{}
	modelConfigs, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] CreateAgent listing model configs failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	for _, cfg := range modelConfigs {
		modelID := strings.TrimSpace(cfg.Model)
		if modelID == "" {
			continue
		}
		allowedModels[modelID] = struct{}{}
	}
	agent.Model = normalizeAgentModel(agent.Model, allowedModels)

	// Parse tools from JSON hidden field
	if toolsJSON := c.FormValue("tools_json"); toolsJSON != "" {
		if err := json.Unmarshal([]byte(toolsJSON), &agent.Tools); err != nil {
			log.Printf("[handler] CreateAgent error parsing tools: %v", err)
		}
	}
	if agent.Tools == nil {
		agent.Tools = []string{}
	}
	agent.Tools = normalizeAgentTools(agent.Tools)

	// Parse selected plugins from JSON hidden field
	if pluginsJSON := c.FormValue("plugins_json"); pluginsJSON != "" {
		if err := json.Unmarshal([]byte(pluginsJSON), &agent.Plugins); err != nil {
			log.Printf("[handler] CreateAgent error parsing plugins: %v", err)
		}
	}
	validatedPlugins, err := h.normalizeAndValidateSelectedPlugins(c.Request().Context(), agent.Plugins)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	agent.Plugins = validatedPlugins

	// Parse skills from JSON hidden field
	if skillsJSON := c.FormValue("skills_json"); skillsJSON != "" {
		if err := json.Unmarshal([]byte(skillsJSON), &agent.Skills); err != nil {
			log.Printf("[handler] CreateAgent error parsing skills: %v", err)
		}
	}
	if agent.Skills == nil {
		agent.Skills = []models.SkillConfig{}
	}

	// Parse MCP servers from JSON hidden field
	if mcpJSON := c.FormValue("mcp_servers_json"); mcpJSON != "" {
		if err := json.Unmarshal([]byte(mcpJSON), &agent.MCPServers); err != nil {
			log.Printf("[handler] CreateAgent error parsing mcp_servers: %v", err)
		}
	}
	if agent.MCPServers == nil {
		agent.MCPServers = []models.MCPServerConfig{}
	}

	log.Printf("[handler] CreateAgent name=%q model=%s tools=%d skills=%d mcp=%d",
		agent.Name, agent.Model, len(agent.Tools), len(agent.Skills), len(agent.MCPServers))

	if err := h.agentRepo.Create(c.Request().Context(), &agent); err != nil {
		log.Printf("[handler] CreateAgent error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return h.ListAgents(c)
}

func (h *Handler) UpdateAgent(c echo.Context) error {
	id := c.Param("id")
	existing, err := h.agentRepo.GetByID(c.Request().Context(), id)
	if err != nil || existing == nil {
		return echo.NewHTTPError(http.StatusNotFound, "Agent not found")
	}

	existing.Name = c.FormValue("name")
	existing.Description = c.FormValue("description")
	existing.SystemPrompt = c.FormValue("system_prompt")
	existing.Model = c.FormValue("model")

	allowedModels := map[string]struct{}{}
	modelConfigs, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] UpdateAgent listing model configs failed: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	for _, cfg := range modelConfigs {
		modelID := strings.TrimSpace(cfg.Model)
		if modelID == "" {
			continue
		}
		allowedModels[modelID] = struct{}{}
	}
	existing.Model = normalizeAgentModel(existing.Model, allowedModels)

	if toolsJSON := c.FormValue("tools_json"); toolsJSON != "" {
		if err := json.Unmarshal([]byte(toolsJSON), &existing.Tools); err != nil {
			log.Printf("[handler] UpdateAgent error parsing tools: %v", err)
		}
	}
	existing.Tools = normalizeAgentTools(existing.Tools)
	if pluginsJSON := c.FormValue("plugins_json"); pluginsJSON != "" {
		if err := json.Unmarshal([]byte(pluginsJSON), &existing.Plugins); err != nil {
			log.Printf("[handler] UpdateAgent error parsing plugins: %v", err)
		}
	}
	validatedPlugins, err := h.normalizeAndValidateSelectedPlugins(c.Request().Context(), existing.Plugins)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	existing.Plugins = validatedPlugins
	if skillsJSON := c.FormValue("skills_json"); skillsJSON != "" {
		if err := json.Unmarshal([]byte(skillsJSON), &existing.Skills); err != nil {
			log.Printf("[handler] UpdateAgent error parsing skills: %v", err)
		}
	}
	if mcpJSON := c.FormValue("mcp_servers_json"); mcpJSON != "" {
		if err := json.Unmarshal([]byte(mcpJSON), &existing.MCPServers); err != nil {
			log.Printf("[handler] UpdateAgent error parsing mcp_servers: %v", err)
		}
	}

	log.Printf("[handler] UpdateAgent id=%s name=%q", id, existing.Name)

	if err := h.agentRepo.Update(c.Request().Context(), existing); err != nil {
		log.Printf("[handler] UpdateAgent error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return h.ListAgents(c)
}

func (h *Handler) DeleteAgent(c echo.Context) error {
	id := c.Param("id")
	log.Printf("[handler] DeleteAgent id=%s", id)

	if err := h.agentRepo.Delete(c.Request().Context(), id); err != nil {
		log.Printf("[handler] DeleteAgent error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return h.ListAgents(c)
}

// GetAgentJSON returns a single agent as JSON (for edit modal population).
func (h *Handler) GetAgentJSON(c echo.Context) error {
	id := c.Param("id")
	agent, err := h.agentRepo.GetByID(c.Request().Context(), id)
	if err != nil || agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "Agent not found")
	}
	return c.JSON(http.StatusOK, agent)
}
