package pages

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestCardActionDropdownMenusRenderSharedShellAndLocalActions(t *testing.T) {
	tests := []struct {
		name      string
		render    func(t *testing.T) string
		required  []string
		forbidden []string
	}{
		{
			name: "agents",
			render: func(t *testing.T) string {
				return renderActionDropdownComponent(t, AgentsContent([]models.Agent{{ID: "agent-1", Name: "Planner", Description: "Plans", Model: "inherit", Key: "planner", Enabled: true}}, nil))
			},
			required: []string{`aria-label="More actions for Planner"`, `title="More actions for Planner"`, `onclick="handleDropdownToggle(event)"`, `Edit`, `Delete`, `openDeleteAgentConfirm(this)`},
		},
		{
			name: "models",
			render: func(t *testing.T) string {
				modelsList := []models.LLMConfig{{ID: "model-1", Name: "Claude", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodAPIKey}}
				return renderActionDropdownComponent(t, ModelsContent(modelsList, nil, false))
			},
			required: []string{`aria-label="More actions for Claude"`, `onclick="handleDropdownToggle(event)"`, `Edit`, `Set as Default`, `Delete`, `setDefaultModel(this)`, `deleteModel(this)`},
		},
		{
			name: "skills",
			render: func(t *testing.T) string {
				return renderActionDropdownComponent(t, SkillsContent([]SkillCard{{Handle: "review-code", Name: "Review Code", Scope: "project", Enabled: true}}, true))
			},
			required: []string{`aria-label="More actions for Review Code"`, `onclick="handleDropdownToggle(event)"`, `Edit`, `Disable`, `Set always use`, `Delete`, `deleteSkill(this)`},
		},
		{
			name: "channels",
			render: func(t *testing.T) string {
				view := defaultChannelsSettingsView("project-1")
				view.HasGitHubChannel = true
				view.HasSlackChannel = true
				view.HasTelegramChannel = true
				view.HasDiscordChannel = true
				view.HasXChannel = true
				view.HasEmailChannel = true
				view.GitHubStatus = service.GitHubConnectionStatus{Configured: true, Connected: true}
				view.SlackStatus = service.SlackConnectionStatus{Configured: true, Connected: true}
				view.IsBotRunning = true
				view.DiscordStatus = service.DiscordConnectionStatus{Configured: true, Running: true}
				view.XStatus = service.XConnectionStatus{Configured: true, Connected: true, Running: true}
				view.EmailStatus = service.EmailConnectionStatus{Configured: true, Running: true, Address: "bot@example.com"}
				view.Webhooks = []models.WebhookEndpoint{{ID: "webhook-1", ProjectID: "project-1", Name: "Deploy Hook", PathToken: "deploy-hook", Enabled: true}}
				return renderActionDropdownComponent(t, SettingsContent(view))
			},
			required: []string{`aria-label="More actions for GitHub"`, `aria-label="More actions for Slack"`, `aria-label="More actions for Telegram Bot"`, `aria-label="More actions for Discord"`, `aria-label="More actions for Email"`, `aria-label="More actions for X (formerly Twitter)"`, `aria-label="More actions for Webhook"`, `aria-label="More actions for Outbound Message Targets"`, `openGitHubConfigModal()`, `openSlackConfigModal()`, `openDeleteChannelConfirm('X (formerly Twitter)'`, `hx-post="/channels/slack/test"`, `hx-post="/channels/telegram/test"`, `hx-post="/channels/email/test"`, `Rotate Secret`, `openOutboundTargetsModal()`},
		},
		{
			name: "personality",
			render: func(t *testing.T) string {
				return renderActionDropdownComponent(t, AppSettingsContent("base", "project-1", []models.CustomPersonality{{ID: "personality-1", Key: "custom", Name: "Custom Persona", Description: "Custom", SystemPromptPreview: "Prompt"}}))
			},
			required: []string{`aria-label="More actions for Custom Persona"`, `aria-label="More actions for Base"`, `onclick="handleDropdownToggle(event)"`, `Edit`, `Set as Default`, `Delete`, `hx-delete="/personality/custom/custom`},
		},
		{
			name: "automations",
			render: func(t *testing.T) string {
				cards := []models.AutomationCard{{Automation: models.Automation{ID: "automation-1", ProjectID: "project-1", Name: "Nightly Build", LifecycleState: models.AutomationActive, HealthState: models.AutomationHealthHealthy}, Version: models.AutomationVersion{AdapterKey: "custom"}}}
				return renderActionDropdownComponent(t, AutomationsContent(cards, "project-1"))
			},
			required: []string{`aria-label="More actions for Nightly Build"`, `onclick="handleDropdownToggle(event)"`, `data-automation-card-edit="automation-1"`, `Run now`, `Disable`, `Delete`, `openAutomationCardDelete(this)`},
		},
		{
			name: "alerts",
			render: func(t *testing.T) string {
				return renderActionDropdownComponent(t, AlertsContent([]models.AlertSummary{{ID: "alert-1", ProjectID: "project-1", Title: "Disk full"}}, "project-1", 1))
			},
			required: []string{`aria-label="More actions"`, `title="More actions"`, `onclick="handleDropdownToggle(event)"`, `Mark all as read`, `Delete All Alerts`, `data-delete-url="/alerts?project_id=project-1"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := tt.render(t)
			for _, want := range tt.required {
				if !strings.Contains(html, want) {
					t.Fatalf("expected rendered %s menu to contain %q", tt.name, want)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(html, forbidden) {
					t.Fatalf("expected rendered %s menu not to contain %q", tt.name, forbidden)
				}
			}
		})
	}
}

func renderActionDropdownComponent(t *testing.T, component interface {
	Render(context.Context, io.Writer) error
}) string {
	t.Helper()
	var buf bytes.Buffer
	if err := component.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render component: %v", err)
	}
	return buf.String()
}
