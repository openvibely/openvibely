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
		events, e := tc.stores.ProjectAccess.Audit(ctx, project.ID)
		require.NoError(t, e)
		require.Equal(t, "usage_reset.requested", events[0].Action)
		return map[string]any{"ok": true}, nil
	})
	require.NoError(t, err)
	require.Equal(t, true, result["ok"])
	events, err := tc.stores.ProjectAccess.Audit(ctx, project.ID)
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
	events, err = tc.stores.ProjectAccess.Audit(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, "usage_reset.failed_or_unknown", events[0].Action, "cancelled requests still record uncertain outcomes")
}
