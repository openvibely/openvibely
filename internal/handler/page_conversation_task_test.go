package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

type failingPageConversationStore struct {
	persistence.PagePluginStore
	err   error
	calls int
}

func (s *failingPageConversationStore) GetConversationTask(context.Context, string) (string, error) {
	s.calls++
	return "", s.err
}

func TestPageConversationAdapters(t *testing.T) {
	for _, steering := range []bool{false, true} {
		name, method, path := "reader", http.MethodGet, "/api/plugin-pages/:instanceId/chat/thread/executions/:execId/fragment"
		if steering {
			name, method, path = "steering", http.MethodPost, "/api/plugin-pages/:instanceId/chat/thread/queued/:inputId/steer"
		}
		t.Run(name, func(t *testing.T) {
			env := setupPluginPages(t)
			adapter := env.h.pluginThreadContext
			if steering {
				adapter = env.h.pluginSteeringTask
			}
			newContext := func() echo.Context {
				req := httptest.NewRequest(method, pluginAPI(env.inA.ID, env.projA.ID, "/chat/thread")+"&taskId=client-query", nil)
				req.Header.Set("HX-Request", "true")
				c := env.e.NewContext(req, httptest.NewRecorder())
				c.SetPath(path)
				c.SetParamNames("instanceId", "execId", "inputId", "extra")
				c.SetParamValues(env.inA.ID, "execution", "input", "preserved")
				return c
			}
			c := newContext()
			err := adapter(c)
			var httpErr *echo.HTTPError
			require.ErrorAs(t, err, &httpErr)
			if steering {
				require.Equal(t, http.StatusConflict, httpErr.Code)
				require.Equal(t, "no active response to steer", httpErr.Message)
			} else {
				require.Equal(t, http.StatusNotFound, httpErr.Code)
			}
			require.Empty(t, c.Param("taskId"))
			env.openPage(t, env.inA)
			taskID, err := env.mgr.Store().GetConversationTask(t.Context(), env.inA.ID)
			require.NoError(t, err)
			require.NotEmpty(t, taskID)
			for _, existingTaskParam := range []bool{false, true} {
				c = newContext()
				if existingTaskParam {
					c.SetParamNames(append(c.ParamNames(), "taskId")...)
					c.SetParamValues(append(c.ParamValues(), "client-route")...)
				}
				require.NoError(t, adapter(c))
				require.Equal(t, taskID, c.Param("taskId"))
				require.Equal(t, env.inA.ID, c.Param("instanceId"))
				require.Equal(t, "execution", c.Param("execId"))
				require.Equal(t, "input", c.Param("inputId"))
				require.Equal(t, "preserved", c.Param("extra"))
				if !steering {
					require.Equal(t, "no-store", c.Response().Header().Get("Cache-Control"))
				}
			}
			lookupErr := errors.New("conversation lookup failed")
			store := &failingPageConversationStore{PagePluginStore: env.mgr.Store(), err: lookupErr}
			manager := pageplugins.NewManager(store, t.TempDir())
			t.Cleanup(manager.StopAll)
			env.h.SetPagePlugins(manager)
			c = newContext()
			require.ErrorIs(t, adapter(c), lookupErr)
			require.Equal(t, 1, store.calls)
			require.Empty(t, c.Param("taskId"))
		})
	}
}

func TestPageSteeringMutationRejectedBeforeResolution(t *testing.T) {
	// An unavailable page manager would return 404 if resolution ran first.
	h := &Handler{}
	for _, endpoint := range []echo.HandlerFunc{h.PluginChatSteer, h.PluginChatQueuedSteer} {
		for _, origin := range []string{"", "https://other.example"} {
			req := httptest.NewRequest(http.MethodPost, "/api/plugin-pages/missing/chat/thread/steer", nil)
			if origin != "" {
				req.Header.Set("HX-Request", "true")
				req.Header.Set("Origin", origin)
			}
			c := echo.New().NewContext(req, httptest.NewRecorder())
			var httpErr *echo.HTTPError
			require.ErrorAs(t, endpoint(c), &httpErr)
			require.Equal(t, http.StatusForbidden, httpErr.Code)
			require.Empty(t, c.Param("taskId"))
		}
	}
}
