package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRuntimeUsageAuthorizationAndAccountScope(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := NewOrganizationService(env.Organizations, "test-secret")
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	project := &models.Project{Name: "Usage scope"}
	require.NoError(t, env.Projects.Create(owner, project))
	policy, err := env.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	member, err := org.Accept(ctx, token, "usage-viewer", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, member.UserID, "viewer"))
	// Even a forged/stale admin claim must use the current database role.
	principal := member.Principal
	principal.Role = "admin"
	viewer := models.WithPrincipal(ctx, principal)
	cfg := &models.LLMConfig{Name: "Account", Provider: models.ProviderOpenAI, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "secret", OAuthAccountID: "account"}
	require.NoError(t, env.LLMConfigs.Create(ctx, cfg))
	svc := NewUsageAnalyticsService(env.Usage, env.LLMConfigs)
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
		return &models.AccountUsageResult{Provider: "openai", AgentConfigID: cfg.ID, AccountID: cfg.OAuthAccountID, PlanType: "private-plan"}, nil
	})
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), env.Organizations, false)
	input := json.RawMessage(`{"range":"all"}`)
	output, err := ExecuteViewUsageAnalyticsTool(owner, svc, project.ID, input)
	require.NoError(t, err)
	require.Contains(t, output, "private-plan")
	output, err = ExecuteViewUsageAnalyticsTool(viewer, svc, project.ID, input)
	require.NoError(t, err)
	require.NotContains(t, output, "private-plan")
	var response usageAnalyticsActionResponse
	require.NoError(t, json.Unmarshal([]byte(output), &response))
	require.Empty(t, response.AccountLimits)
	require.Equal(t, project.ID, response.ProjectID)
	_, err = ExecuteViewUsageAnalyticsTool(viewer, svc, "foreign", input)
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = ExecuteViewUsageAnalyticsTool(ctx, svc, project.ID, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, member.UserID, ""))
	_, err = ExecuteViewUsageAnalyticsTool(viewer, svc, project.ID, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), env.Organizations, true)
	output, err = ExecuteViewUsageAnalyticsTool(ctx, svc, project.ID, input)
	require.NoError(t, err)
	require.Contains(t, output, "private-plan", "auth-off retains owner account access")
}
