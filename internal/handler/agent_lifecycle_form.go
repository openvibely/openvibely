package handler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// applyLifecycleAgentFormFields reads the lifecycle-era identity and policy
// fields the agent create/edit dialog now exposes (runbook §Agent Create/Edit
// Dialog lines 2029-2348) and copies them onto the agent. All fields are
// optional; missing fields preserve the existing value.
func applyLifecycleAgentFormFields(c echo.Context, agent *models.Agent) error {
	if key := strings.TrimSpace(c.FormValue("key")); key != "" {
		if err := validateAgentKey(key); err != nil {
			return err
		}
		agent.Key = key
	}
	if scope := strings.TrimSpace(c.FormValue("scope")); scope != "" {
		s := models.AgentScope(strings.ToLower(scope))
		if s != models.AgentScopeGlobal && s != models.AgentScopeProject {
			return fmt.Errorf("invalid scope %q (must be global or project)", scope)
		}
		agent.Scope = s
	}
	// A workspace selector is not an Agent ownership change.
	if pid := strings.TrimSpace(c.FormValue("project_id")); pid != "" && pid != models.AllProjectsID {
		agent.ProjectID = pid
	}
	if v := strings.TrimSpace(c.FormValue("selectable_as_primary")); v != "" {
		agent.SelectableAsPrimary = parseBoolFormValue(v)
	} else if agent.ID == "" {
		// New agents default to available in the primary-agent picker.
		agent.SelectableAsPrimary = true
	}
	if v := strings.TrimSpace(c.FormValue("enabled")); v != "" {
		agent.Enabled = parseBoolFormValue(v)
	} else if agent.ID == "" {
		// New agents default to enabled when the form does not say otherwise.
		agent.Enabled = true
	}
	if v := strings.TrimSpace(c.FormValue("permission_defaults_json")); v != "" {
		var perms models.AgentPermissionDefaults
		if err := json.Unmarshal([]byte(v), &perms); err != nil {
			return fmt.Errorf("invalid permission_defaults_json: %w", err)
		}
		agent.PermissionDefaults = perms
	}
	if v := strings.TrimSpace(c.FormValue("source_refs_json")); v != "" {
		var refs []string
		if err := json.Unmarshal([]byte(v), &refs); err != nil {
			return fmt.Errorf("invalid source_refs_json: %w", err)
		}
		agent.SourceRefs = refs
	}
	// Manually-edited agents from the dialog never overwrite a generated_status
	// of protected, but they do flip generated→user_edited so the importer
	// preserves the user's customization (runbook §Agent Config Storage line
	// 1538-1548 + §Frontmatter Importer line 1798).
	if agent.GeneratedStatus == models.AgentStatusGenerated {
		agent.GeneratedStatus = models.AgentStatusUserEdited
	} else if agent.GeneratedStatus == "" {
		agent.GeneratedStatus = models.AgentStatusUserEdited
	}
	if agent.CreatedBy == "" {
		agent.CreatedBy = models.AgentCreatedByUser
	}
	return nil
}

func parseBoolFormValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// validateAgentKey mirrors the slug rules enforced by agentskills.handleShape
// and the agentlibrary declaration validator so the dialog cannot save a key
// that the catalog or importer would later reject.
func validateAgentKey(key string) error {
	if key == "" {
		return fmt.Errorf("agent key is required")
	}
	if len(key) > 64 {
		return fmt.Errorf("agent key must be 64 characters or fewer")
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("agent key has invalid character %q at position %d (allowed: lowercase letters, digits, _ -)", r, i)
		}
	}
	return nil
}
