package pages

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func defaultChannelsSettingsView(projectID string) ChannelsSettingsView {
	return ChannelsSettingsView{
		CurrentProjectID:           projectID,
		SendResponses:              true,
		RichMessagesV2:             true,
		GitHubAuthMode:             service.GitHubAuthModePAT,
		SlackBotTokenMode:          service.SlackBotTokenSourceOAuth,
		SlackSendResponses:         true,
		DiscordSendResponses:       true,
		EmailSendResponses:         true,
		EmailSkipAttachments:       true,
		EmailPollIntervalSeconds:   "60",
		SendMessageExplicitTargets: false,
	}
}

func TestOutboundTargetsCardAggregateSummaryMatchesMaterializedRendering(t *testing.T) {
	cases := []struct {
		name            string
		targets         []models.ChannelTarget
		explicitAllowed bool
		wantSummary     string
		wantPolicy      string
	}{
		{
			name:        "empty",
			wantSummary: "No saved outbound targets",
			wantPolicy:  "Saved targets only",
		},
		{
			name: "known platforms",
			targets: []models.ChannelTarget{
				{Platform: "email", TargetKind: "email", TargetID: "a@example.com"},
				{Platform: "slack", TargetKind: "channel", TargetID: "C1"},
				{Platform: "slack", TargetKind: "user", TargetID: "U1"},
				{Platform: "telegram", TargetKind: "chat", TargetID: "-100"},
				{Platform: "discord", TargetKind: "channel", TargetID: "D1"},
			},
			wantSummary: "email: 1 • slack: 2 • telegram: 1 • discord: 1",
			wantPolicy:  "Saved targets only",
		},
		{
			name: "unknown only",
			targets: []models.ChannelTarget{
				{Platform: "matrix", TargetKind: "room", TargetID: "!room"},
				{Platform: "custom", TargetKind: "channel", TargetID: "target"},
			},
			wantSummary: "2 saved outbound target(s)",
			wantPolicy:  "Saved targets only",
		},
		{
			name: "known plus unknown with explicit targets",
			targets: []models.ChannelTarget{
				{Platform: "Email", TargetKind: "email", TargetID: "a@example.com"},
				{Platform: "matrix", TargetKind: "room", TargetID: "!room"},
			},
			explicitAllowed: true,
			wantSummary:     "email: 1",
			wantPolicy:      "Explicit targets allowed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			materializedSummary := outboundTargetSummary(tc.targets)
			aggregateSummary := outboundTargetProjectSummary(tc.targets)
			require.Equal(t, materializedSummary, outboundTargetProjectSummaryText(aggregateSummary))
			require.Equal(t, tc.wantSummary, materializedSummary)

			materializedHTML := renderOutboundTargetsCardForTest(t, "project", outboundTargetProjectSummary(tc.targets), tc.explicitAllowed)
			aggregateHTML := renderOutboundTargetsCardForTest(t, "project", aggregateSummary, tc.explicitAllowed)
			require.Equal(t, materializedHTML, aggregateHTML)
			require.Contains(t, aggregateHTML, tc.wantSummary)
			require.Contains(t, aggregateHTML, tc.wantPolicy)
		})
	}
}

func TestOutboundTargetsCardSummaryMatchesRepositorySummary(t *testing.T) {
	summary := repository.ChannelTargetProjectSummary{
		Total:      7,
		Configured: true,
		ByPlatform: map[string]repository.ChannelTargetPlatformSummary{
			"email":    {Total: 1, ByKind: map[string]int{"email": 1}},
			"slack":    {Total: 2, Home: 1, Named: 1, ByKind: map[string]int{"channel": 1, "user": 1}},
			"telegram": {Total: 1, ByKind: map[string]int{"chat": 1}},
			"discord":  {Total: 2, ByKind: map[string]int{"channel": 1, "user": 1}},
			"matrix":   {Total: 1, ByKind: map[string]int{"room": 1}},
		},
	}

	html := renderOutboundTargetsCardForTest(t, "project", summary, true)
	require.Contains(t, html, "email: 1 • slack: 2 • telegram: 1 • discord: 2")
	require.Contains(t, html, "Explicit targets allowed")
	require.NotContains(t, strings.ToLower(html), "matrix: 1")
}

func renderOutboundTargetsCardForTest(t *testing.T, projectID string, summary repository.ChannelTargetProjectSummary, explicitAllowed bool) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, OutboundTargetsCard(projectID, summary, explicitAllowed).Render(t.Context(), &buf))
	return buf.String()
}
