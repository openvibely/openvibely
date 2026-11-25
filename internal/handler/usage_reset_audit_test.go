package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

type failingResetAudit struct{ persistence.ProjectAccessStore }

func (s failingResetAudit) Record(context.Context, string, string, string, string, string) error {
	return errors.New("audit unavailable")
}

func TestUsageResetAuditBeforeProviderAndOutcome(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	h.SetOrganizationService(service.NewOrganizationService(tc.stores.Organizations, "secret"))
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	project := createProject(t, h, "reset audit")
	ctx, cancel := context.WithCancel(models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID}))
	defer cancel()
	req := httptest.NewRequest("POST", "/api/analytics/usage/resets/consume?project_id="+project.ID, nil).WithContext(ctx)
	c := echo.New().NewContext(req, httptest.NewRecorder())
	input := ConsumeUsageResetRequest{AccountKey: "opaque-account", CreditID: "credit", IdempotencyKey: "attempt"}
	calls := 0
	result, err := h.auditedUsageReset(c, input, func() (map[string]any, error) {
		calls++
		events, e := tc.stores.ProjectAccess.Audit(ctx, "")
		require.NoError(t, e)
		require.Equal(t, "usage_reset.requested", events[0].Action)
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.Equal(t, true, result["ok"])
	events, err := tc.stores.ProjectAccess.Audit(ctx, "")
	require.NoError(t, err)
	require.Equal(t, "usage_reset.succeeded", events[0].Action)
	require.Equal(t, models.BootstrapUserID, events[0].ActorUserID)
	require.Contains(t, events[0].Details, "attempt")
	h.SetProjectAccessService(service.NewProjectAccessService(failingResetAudit{tc.stores.ProjectAccess}))
	_, err = h.auditedUsageReset(c, input, func() (map[string]any, error) { calls++; return nil, nil })
	require.ErrorContains(t, err, "audit unavailable")
	require.Equal(t, 1, calls, "failed intent must prevent provider side effects")
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	_, err = h.auditedUsageReset(c, input, func() (map[string]any, error) { cancel(); return nil, errors.New("provider timeout") })
	require.ErrorContains(t, err, "provider timeout")
	events, err = tc.stores.ProjectAccess.Audit(t.Context(), "")
	require.NoError(t, err)
	require.Equal(t, "usage_reset.failed_or_unknown", events[0].Action, "cancelled requests still record uncertain outcomes")
}

func TestUsageResetIgnoresRemoteSelection(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(t.Context(), models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(t.Context(), token, "reset-member", "long-password-value", "")
	require.NoError(t, err)
	for _, selected := range []string{"", "missing", "r-0123456789abcdef-project1", models.AllProjectsID} {
		for _, admin := range []bool{true, false} {
			e := echo.New()
			e.Use(h.remoteRouting)
			e.Use(h.projectAccessBoundary)
			calls := 0
			e.GET("/api/analytics/usage/resets", func(c echo.Context) error { calls++; return c.NoContent(200) })
			e.POST("/api/analytics/usage/resets/consume", func(c echo.Context) error {
				_, err := h.auditedUsageReset(c, ConsumeUsageResetRequest{AccountKey: "account", CreditID: "credit", IdempotencyKey: "attempt"}, func() (map[string]any, error) { calls++; return nil, nil })
				return err
			})
			principal := user.Principal
			if admin {
				principal = models.Principal{UserID: models.BootstrapUserID}
			}
			for _, method := range []string{"GET", "POST"} {
				path := "/api/analytics/usage/resets"
				if method == "POST" {
					path += "/consume"
				}
				req := httptest.NewRequest(method, path+"?project_id="+selected, nil).WithContext(models.WithPrincipal(t.Context(), principal))
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if admin {
					require.Equal(t, 200, rec.Code, rec.Body.String())
				} else {
					require.Equal(t, 404, rec.Code, rec.Body.String())
				}
			}
			if admin {
				require.Equal(t, 2, calls)
			} else {
				require.Zero(t, calls)
			}
		}
	}
	events, err := tc.stores.ProjectAccess.Audit(t.Context(), "")
	require.NoError(t, err)
	require.NotEmpty(t, events)
	require.Equal(t, "usage_reset.succeeded", events[0].Action)
}
