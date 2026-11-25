package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRemoteAutomationBulkSpansLocalAndTwoServers(t *testing.T) {
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	wrapped := remote.Wrap(local.Stores, &remote.Router{Hub: hub})
	env := wireTestHandler(t, wrapped, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	h.SetAutomationServices(service.NewAutomationGraphService(wrapped.Automations), nil)
	h.SetAutomationBuilderServices(nil, nil, nil, nil, nil, service.NewAutomationLifecycleService(wrapped.Automations, wrapped.Schedules))
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})
	seed := func(stores persistence.Stores, prefix string, count int) {
		p := &models.Project{Name: "Project 1"}
		require.NoError(t, stores.Projects.Create(context.Background(), p))
		for i := 0; i < count; i++ {
			task := &models.Task{ProjectID: p.ID, Title: fmt.Sprintf("Task %d", i), Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 1}
			require.NoError(t, stores.Tasks.Create(context.Background(), task))
			runAt := time.Now().UTC().Add(time.Hour)
			schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, NextRun: &runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
			require.NoError(t, stores.Schedules.Create(context.Background(), schedule))
			registration := service.NewAutomationRegistrationService(stores.Automations, service.NewAutomationAdapterRegistry())
			_, _, err := registration.Register(context.Background(), service.AutomationRegistrationRequest{ProjectID: p.ID, StableKey: fmt.Sprint(i), Name: fmt.Sprintf("%s%03d", prefix, i), AdapterKey: service.AutomationAdapterNativeSDLC, Resources: []models.AutomationResourceBinding{
				{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
				{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
			}})
			require.NoError(t, err)
		}
	}
	seed(local.Stores, "C", 3)
	for i := 0; i < 2; i++ {
		srv, _, owner := fakeRemoteOV(t, "secret")
		count := 3
		prefix := "B"
		if i == 0 {
			count = 3
			prefix = "A"
		}
		seed(owner.remoteExecutor.Stores, prefix, count)
		owner.SetAutomationBuilderServices(nil, nil, nil, nil, nil, service.NewAutomationLifecycleService(owner.remoteExecutor.Stores.Automations, owner.remoteExecutor.Stores.Schedules))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {fmt.Sprintf("Remote %d", i+1)}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 2 }, 5*time.Second, 20*time.Millisecond)
	cards, err := h.automationGraphSvc.ListPageFiltered(ctx, models.AllProjectsID, 500, 0, persistence.AutomationCardListFilter{})
	require.NoError(t, err)
	var targets []string
	for _, card := range cards {
		if strings.HasSuffix(card.Automation.Name, "000") {
			targets = append(targets, card.Automation.ID)
		}
	}
	require.Len(t, targets, 3)
	payload, err := json.Marshal(bulkIDsRequest{IDs: targets})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/automations/bulk?project_id="+models.AllProjectsID, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("HX-Request", "true")
	e.ServeHTTP(rec, req.WithContext(ctx))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	after, err := h.automationGraphSvc.ListPageFiltered(ctx, models.AllProjectsID, 500, 0, persistence.AutomationCardListFilter{})
	require.NoError(t, err)
	require.Len(t, after, len(cards)-3)
	for _, card := range after {
		require.NotContains(t, targets, card.Automation.ID)
	}
}
