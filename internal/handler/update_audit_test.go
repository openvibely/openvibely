package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSystemUpdateAuditBlocksSideEffectsAndSurvivesCancellation(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetOrganizationService(service.NewOrganizationService(tc.stores.Organizations, "secret"))
	project := createProject(t, h, "update audit")
	for _, operation := range []string{"apply", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID}))
			defer cancel()
			req := httptest.NewRequest("POST", "/api/system/update/"+operation+"?project_id="+project.ID, nil).WithContext(ctx)
			c := echo.New().NewContext(req, httptest.NewRecorder())
			calls := 0
			require.NoError(t, h.auditedSystemUpdate(c, operation, func() error {
				calls++
				events, err := tc.stores.ProjectAccess.Audit(ctx, project.ID)
				require.NoError(t, err)
				require.Equal(t, "system_update."+operation+".requested", events[0].Action)
				return nil
			}))
			events, err := tc.stores.ProjectAccess.Audit(ctx, project.ID)
			require.NoError(t, err)
			require.Equal(t, "system_update."+operation+".succeeded", events[0].Action)
			require.Equal(t, models.BootstrapUserID, events[0].ActorUserID)
			h.SetProjectAccessService(service.NewProjectAccessService(failingResetAudit{tc.stores.ProjectAccess}))
			err = h.auditedSystemUpdate(c, operation, func() error { calls++; return nil })
			require.ErrorContains(t, err, "audit unavailable")
			require.Equal(t, 1, calls)
			h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
			failure := errors.New("coordinator failure")
			err = h.auditedSystemUpdate(c, operation, func() error { cancel(); return failure })
			require.ErrorIs(t, err, failure)
			events, err = tc.stores.ProjectAccess.Audit(t.Context(), project.ID)
			require.NoError(t, err)
			require.Equal(t, "system_update."+operation+".failed_or_unknown", events[0].Action)
		})
	}
}
