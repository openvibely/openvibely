package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestHandlerHasConfiguredModelsUsesExistenceQuery(t *testing.T) {
	env := teststores.New(t, teststores.WithIOCounting())
	counter := env.IO
	repo := env.LLMConfigs
	h := &Handler{llmConfigRepo: repo}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	counter.Reset()
	counter.SetEnabled(true)
	hasModels, err := h.hasConfiguredModels(c)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("hasConfiguredModels: %v", err)
	}
	if !hasModels {
		t.Fatal("hasConfiguredModels = false, want true for seeded test model")
	}

	observed := counter.Snapshot()
	if observed.Requests != 1 || observed.ReadTextBytes != 0 {
		t.Fatalf("unexpected physical existence IO: %+v", observed)
	}
}

func TestHandlerHasConfiguredModelsNilRepoErrors(t *testing.T) {
	h := &Handler{}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	hasModels, err := h.hasConfiguredModels(c)
	if err == nil {
		t.Fatal("hasConfiguredModels nil repo error = nil")
	}
	if hasModels {
		t.Fatal("hasConfiguredModels nil repo = true, want false")
	}
	if !strings.Contains(err.Error(), "model repository is not configured") {
		t.Fatalf("hasConfiguredModels nil repo error = %q", err.Error())
	}
}

func TestHandlerHasConfiguredModelsEmptyRepo(t *testing.T) {
	env := teststores.New(t)
	repo := env.LLMConfigs
	teststores.ClearLLMConfigs(t, env.LLMConfigs)
	h := &Handler{llmConfigRepo: repo}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		t.Fatalf("hasConfiguredModels empty repo: %v", err)
	}
	if hasModels {
		t.Fatal("hasConfiguredModels empty repo = true, want false")
	}
}
