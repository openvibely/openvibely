package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	docs "github.com/openvibely/openvibely/docs"
	echoSwagger "github.com/swaggo/echo-swagger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var routeParamRe = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

func routePathToSwagger(path string) string {
	return routeParamRe.ReplaceAllString(path, `{$1}`)
}

func diffRouteSets(a, b map[string]struct{}) []string {
	missing := make([]string, 0)
	for k := range a {
		if _, ok := b[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

func TestSwaggerEndpoint(t *testing.T) {
	e := echo.New()
	e.GET("/swagger/*", echoSwagger.WrapHandler)

	// Test Swagger UI index
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Swagger UI")
}

func TestSwaggerJSONEndpoint(t *testing.T) {
	e := echo.New()
	e.GET("/swagger/*", echoSwagger.WrapHandler)

	req := httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "\"swagger\"")
	assert.Contains(t, rec.Body.String(), "\"/api/chat/message\"")
}

func TestSwaggerSpecCoversAllAPIRoutes(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	specJSON := docs.SwaggerInfo.ReadDoc()
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal([]byte(specJSON), &spec))
	require.NotNil(t, spec.Paths)

	documented := make(map[string]struct{})
	for path, ops := range spec.Paths {
		if !strings.HasPrefix(path, "/api/") {
			continue
		}
		for method := range ops {
			m := strings.ToUpper(method)
			if m == "PARAMETERS" {
				continue
			}
			documented[m+" "+path] = struct{}{}
		}
	}

	registered := make(map[string]struct{})
	for _, r := range e.Routes() {
		if !strings.HasPrefix(r.Path, "/api/") {
			continue
		}
		switch r.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			registered[r.Method+" "+routePathToSwagger(r.Path)] = struct{}{}
		}
	}

	missingInSpec := diffRouteSets(registered, documented)
	staleInSpec := diffRouteSets(documented, registered)

	assert.Empty(t, missingInSpec, "Swagger is missing registered API routes")
	assert.Empty(t, staleInSpec, "Swagger contains stale API routes not registered in handler")
}
