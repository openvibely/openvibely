package handler

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/internal/update"
)

type SystemHealthResponse struct {
	SchemaVersion  int    `json:"schema_version"`
	Ready          bool   `json:"ready"`
	Status         string `json:"status"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	BuildTime      string `json:"build_time"`
	Artifact       string `json:"artifact"`
	UpdateMode     string `json:"update_mode"`
	Distribution   string `json:"distribution"`
	DatabaseSchema int    `json:"database_schema"`
	DrainState     string `json:"drain_state"`
}

func (h *Handler) SetSystemHealth(build buildinfo.Build, updateMode, distribution, hostedToken, dockerToken string, databaseSchema int, drain *update.DrainManager) {
	h.buildIdentity = build
	h.updateMode = updateMode
	h.distribution = distribution
	h.hostedAgentToken = hostedToken
	h.dockerAgentToken = dockerToken
	h.databaseSchema = databaseSchema
	h.drainManager = drain
	h.systemReady = true
}

func (h *Handler) SetManagedUpdateError(message string) { h.managedUpdateError = message }

// SystemHealth reports readiness and immutable build identity.
// @Summary System health and build identity
// @Tags system
// @Produce json
// @Success 200 {object} SystemHealthResponse
// @Failure 401 {object} map[string]any
// @Failure 503 {object} SystemHealthResponse
// @Router /api/system/health [get]
func (h *Handler) SystemHealth(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if !h.authorizeSystemHealth(c) {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "unauthorized", "message": "health access denied", "retryable": false}})
	}
	drainState := update.DrainStateIdle
	if h.drainManager != nil {
		drainState = h.drainManager.Status().State
	}
	status := "ready"
	code := http.StatusOK
	if !h.systemReady {
		status = "unhealthy"
		code = http.StatusServiceUnavailable
	}
	body := SystemHealthResponse{
		SchemaVersion: 1, Ready: h.systemReady, Status: status,
		Version: h.buildIdentity.Version, Commit: h.buildIdentity.Commit, BuildTime: h.buildIdentity.BuildTime,
		Artifact: h.buildIdentity.Artifact, UpdateMode: h.updateMode, Distribution: h.distribution,
		DatabaseSchema: h.databaseSchema, DrainState: drainState,
	}
	return c.JSON(code, body)
}

func (h *Handler) authorizeSystemHealth(c echo.Context) bool {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		host = c.Request().RemoteAddr
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.IsLoopback() {
		return true
	}
	if c.Get("auth_user") != nil {
		return true
	}
	session := h.recognizeSession(c.Request(), time.Now())
	if session.localUser != nil || session.hostedClaims != nil {
		return true
	}
	token := ""
	switch h.updateMode {
	case buildinfo.ModeHosted:
		token = h.hostedAgentToken
	case buildinfo.ModeDockerAgent:
		token = h.dockerAgentToken
	}
	provided := strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
	return token != "" && len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}
