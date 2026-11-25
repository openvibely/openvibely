package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/remoteid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestModelFavoritesPersistAndMergeAcrossClients(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	request := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/ui/model-favorites", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	first := request(http.MethodGet, "")
	require.Equal(t, http.StatusOK, first.Code)
	require.JSONEq(t, `{}`, first.Body.String())

	imported := request(http.MethodPost, `{"import_ids":["model-a"]}`)
	require.Equal(t, http.StatusOK, imported.Code)
	require.JSONEq(t, `{"model-a":true}`, imported.Body.String())

	secondClient := request(http.MethodGet, "")
	require.Equal(t, http.StatusOK, secondClient.Code)
	require.JSONEq(t, `{"model-a":true}`, secondClient.Body.String())

	added := request(http.MethodPost, `{"model_id":"model-b","favorite":true}`)
	require.Equal(t, http.StatusOK, added.Code)
	require.JSONEq(t, `{"model-a":true,"model-b":true}`, added.Body.String())

	removed := request(http.MethodPost, `{"model_id":"model-a","favorite":false}`)
	require.Equal(t, http.StatusOK, removed.Code)
	require.JSONEq(t, `{"model-b":true}`, removed.Body.String())

	stored, err := h.settingsRepo.Get(context.Background(), modelFavoritesSettingKey)
	require.NoError(t, err)
	require.JSONEq(t, `{"model-b":true}`, stored)
}

func TestModelFavoritesRemoteOwner(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	project := createProject(t, owner, "Remote")
	id, _ := signInTwoUsers(t, h, e, srv.URL)
	remoteProject := remoteid.ID(id, project.ID)
	require.NoError(t, h.settingsRepo.Set(context.Background(), modelFavoritesSettingKey, `{"local":true}`))
	require.NoError(t, owner.settingsRepo.Set(context.Background(), modelFavoritesSettingKey, `{"remote-model":true}`))
	request := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/ui/model-favorites?project_id="+remoteProject, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	rec := request(http.MethodGet, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"`+remoteid.ID(id, "remote-model")+`":true}`, rec.Body.String())
	rec = request(http.MethodPost, `{"model_id":"`+remoteid.ID(id, "remote-model")+`","favorite":false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{}`, rec.Body.String())
	raw, err := owner.settingsRepo.Get(context.Background(), modelFavoritesSettingKey)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, raw)
	raw, err = h.settingsRepo.Get(context.Background(), modelFavoritesSettingKey)
	require.NoError(t, err)
	require.JSONEq(t, `{"local":true}`, raw)
}
