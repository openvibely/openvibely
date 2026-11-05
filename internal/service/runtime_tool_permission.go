package service

import (
	"context"
	"encoding/json"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// RuntimeToolPermission is the minimum project role for a runtime tool on any
// surface. Unclassified tools are org-scoped or resolve their own targets.
func RuntimeToolPermission(name string) (authz.Permission, bool) {
	def := chatcontrol.Get(name)
	if def == nil {
		return "", false
	}
	switch def.Domain {
	case chatcontrol.DomainTasks, chatcontrol.DomainSchedules, chatcontrol.DomainAlerts:
		if def.Access == chatcontrol.AccessRead {
			return authz.View, true
		}
		return authz.Edit, true
	case chatcontrol.DomainProjects:
		switch name {
		case "project_info", "get_current_project", "list_projects":
			return authz.View, true
		case "update_project_settings":
			return authz.Manage, true
		}
		// Creation is org-scoped; switching authorizes the target via the guarded project store.
		return "", false
	case chatcontrol.DomainMessaging:
		return authz.Edit, true
	case chatcontrol.DomainChat:
		if name == "request_user_input" {
			return authz.Edit, true
		}
		return "", false
	case chatcontrol.DomainGitHub:
		// Specialized GitHub runtimes add maintainer checks for mutations and
		// activity-recording reads; this is the uniform minimum before caching.
		if def.Access == chatcontrol.AccessRead {
			return authz.View, true
		}
		return authz.Manage, true
	case chatcontrol.DomainAnalytics, chatcontrol.DomainPulse, chatcontrol.DomainMemory, chatcontrol.DomainSettings, chatcontrol.DomainModels, chatcontrol.DomainPersonality, chatcontrol.DomainAgents:
		return authz.View, true
	case chatcontrol.DomainAutomations:
		// Description preview invokes a model even though the registry labels its
		// draft output read-only. Treat it as builder access, not a passive read.
		if def.Access == chatcontrol.AccessRead && name != "preview_automation_description" {
			return authz.View, true
		}
		return authz.Manage, true
	default:
		return "", false
	}
}

// Channel ingress admits editors; tools needing a stronger role, or org-admin
// configuration domains, are rechecked against the current role on each call.
func requireRuntimeToolRole(ctx context.Context, access persistence.ProjectAccessStore, organizations persistence.OrganizationStore, projectID, name string, input json.RawMessage) error {
	permission, protected := RuntimeToolPermission(name)
	if !protected {
		return nil
	}
	var merge struct {
		AutoMerge               *bool   `json:"auto_merge"`
		AutoMergeOnGoalAchieved *bool   `json:"auto_merge_on_goal_achieved"`
		MergeTargetBranch       *string `json:"merge_target_branch"`
	}
	_ = json.Unmarshal(input, &merge)
	if name == "merge_task" || merge.AutoMerge != nil || merge.AutoMergeOnGoalAchieved != nil || merge.MergeTargetBranch != nil {
		permission = authz.Manage
	}
	if def := chatcontrol.Get(name); def != nil {
		switch def.Domain {
		case chatcontrol.DomainSettings, chatcontrol.DomainModels, chatcontrol.DomainPersonality, chatcontrol.DomainAgents:
			principal, ok := models.PrincipalFromContext(ctx)
			if !ok || principal.UserID == "" {
				return authz.ErrDenied
			}
			user, err := organizations.UserByID(ctx, principal.UserID)
			if err != nil {
				return err
			}
			if user == nil || user.Disabled || !user.IsOrgAdmin() {
				return authz.ErrDenied
			}
		}
	}
	return (&authz.Authorizer{Store: access}).Require(ctx, projectID, permission)
}
