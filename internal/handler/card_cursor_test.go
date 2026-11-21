package handler

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestCardCursorLocalHTTPContinuationAndRefresh(t *testing.T) {
	_, e, _, stores := setupTestHandlerWithStores(t)
	for i := 0; i < 65; i++ {
		require.NoError(t, stores.LLMConfigs.Create(t.Context(), &models.LLMConfig{Name: fmt.Sprintf("Cursor model %03d", i), Provider: models.ProviderTest, Model: "cursor"}))
	}
	// Full initial render supplies its cursor in markup (not just an HTTP header).
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models?search=Cursor+model", nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	match := regexp.MustCompile(`data-card-pagination-cursor="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	require.Len(t, match, 2)
	token := html.UnescapeString(match[1])
	require.NotEmpty(t, token)
	next := serveCardPageRequest(t, e, "/models?sort=name_asc&search=Cursor+model&card_page=1&cursor="+url.QueryEscape(token))
	require.Equal(t, 200, next.Code, next.Body.String())
	require.Contains(t, next.Body.String(), "Cursor model 020")
	require.NotContains(t, next.Body.String(), `data-model-name="Cursor model 000"`)
	require.NotEmpty(t, next.Header().Get("X-OpenVibely-Card-Cursor"))
	refreshed := serveCardPageRequest(t, e, "/models?search=Cursor+model&card_window=1&page_size=55")
	require.Equal(t, 200, refreshed.Code, refreshed.Body.String())
	require.Equal(t, 55, strings.Count(refreshed.Body.String(), "data-model-provider="))
	token = refreshed.Header().Get("X-OpenVibely-Card-Cursor")
	next = serveCardPageRequest(t, e, "/models?search=Cursor+model&card_page=1&cursor="+url.QueryEscape(token))
	require.Equal(t, 200, next.Code, next.Body.String())
	require.Contains(t, next.Body.String(), "Cursor model 055")
	require.Equal(t, "false", next.Header().Get(cardPageHasMoreHeader))
	// A token for a different search must never silently advance that search.
	invalid := serveCardPageRequest(t, e, "/models?search=another&card_page=1&cursor="+url.QueryEscape(token))
	require.NotEqual(t, 200, invalid.Code)
}

func TestCardCursorAllProjectsAcrossTwoRemoteSockets(t *testing.T) {
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	wrapped := remote.Wrap(local.Stores, &remote.Router{Hub: hub})
	env := wireTestHandler(t, wrapped, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	h.SetAutomationServices(service.NewAutomationGraphService(wrapped.Automations), nil)
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
	seed(local.Stores, "C", 25)
	for i := 0; i < 2; i++ {
		srv, _, owner := fakeRemoteOV(t, "secret")
		count := 25
		prefix := "B"
		if i == 0 {
			count = 100
			prefix = "A"
		}
		seed(owner.remoteExecutor.Stores, prefix, count)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {fmt.Sprintf("Remote %d", i+1)}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 2 }, 5*time.Second, 20*time.Millisecond)
	for _, order := range []string{"name_asc", "name_desc", "updated_desc", "updated_asc"} {
		t.Run(order, func(t *testing.T) {
			var got []string
			token := ""
			// Query through the wrapped stores and real read sockets. The HTML handler
			// uses the same Finish function after this service call.
			for page := 0; page < 12; page++ {
				s := cardcursor.New("automations", order, "all", token)
				s.Limit = 21
				pageCtx := cardcursor.With(ctx, s)
				cards, err := h.automationGraphSvc.ListPageFiltered(pageCtx, models.AllProjectsID, 21, 0, persistence.AutomationCardListFilter{Sort: order})
				require.NoError(t, err)
				cards, more, err := cardcursor.Finish(pageCtx, cards, 20)
				require.NoError(t, err)
				for _, card := range cards {
					got = append(got, card.Automation.Name)
				}
				token = cardcursor.Encoded(pageCtx)
				if page == 0 && order == "name_asc" {
					require.Equal(t, "A000", got[0])
					require.Equal(t, "A019", got[19])
					require.Len(t, s.Token.Sources, 3)
					require.Empty(t, s.Token.Sources[""])
				}
				if !more {
					break
				}
			}
			require.Len(t, got, 150)
			seen := map[string]bool{}
			for _, name := range got {
				require.False(t, seen[name], name)
				seen[name] = true
			}
			if order == "name_asc" {
				require.Equal(t, "B000", got[100])
				require.Equal(t, "C024", got[149])
			}
			if order == "name_desc" {
				require.Equal(t, "C024", got[0])
				require.Equal(t, "A000", got[149])
			}
		})
	}
	// Disconnecting a participating source fails the request without advancing it.
	state := cardcursor.New("automations", "name_asc", "all", "")
	pageCtx := cardcursor.With(ctx, state)
	rows, err := h.automationGraphSvc.ListPageFiltered(pageCtx, models.AllProjectsID, 21, 0, persistence.AutomationCardListFilter{})
	require.NoError(t, err)
	_, _, err = cardcursor.Finish(pageCtx, rows, 20)
	require.NoError(t, err)
	serverIDs := hub.Servers(models.BootstrapUserID)
	hub.Close(models.BootstrapUserID, serverIDs[0])
	_, err = h.automationGraphSvc.ListPageFiltered(pageCtx, models.AllProjectsID, 21, 0, persistence.AutomationCardListFilter{})
	require.ErrorIs(t, err, remote.ErrUnavailable)
}
