package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandler_CreateTaskProvisionalTitleCollisionAndExplicitTitle(t *testing.T) {
	h, e, configs := setupTestHandler(t)
	// Without a configured model the provisional title remains the fallback.
	agents, err := configs.List(context.Background())
	require.NoError(t, err)
	for _, agent := range agents {
		require.NoError(t, configs.Delete(context.Background(), agent.ID))
	}
	var titles []string
	for _, explicit := range []string{"", "", "My chosen title"} {
		form := url.Values{"message": {"Fix login"}, "category": {"backlog"}, "title": {explicit}}
		req := httptest.NewRequest(http.MethodPost, "/tasks?project_id=default&from=new&thread=1", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		task, err := h.taskSvc.GetByID(context.Background(), rec.Header().Get("X-Created-Task-ID"))
		require.NoError(t, err)
		require.NotNil(t, task)
		titles = append(titles, task.Title)
	}
	require.Equal(t, "Fix login", titles[0])
	require.True(t, strings.HasPrefix(titles[1], "Fix login "))
	require.NotEqual(t, titles[0], titles[1])
	require.Equal(t, "My chosen title", titles[2])
}
