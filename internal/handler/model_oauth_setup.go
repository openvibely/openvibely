package handler

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// Setup sessions hold credentials in memory until the user creates a complete
// model configuration. The random capability is never sent to the OAuth provider.
type modelOAuthSetup struct {
	owner    *Handler
	targetID string
	config   models.LLMConfig
	expires  time.Time
	failure  string
}

var modelOAuthSetups = struct {
	sync.Mutex
	sessions map[string]*modelOAuthSetup
}{sessions: make(map[string]*modelOAuthSetup)}

type modelOAuthSetupContextKey struct{}

const modelOAuthSetupHeader = "X-Model-OAuth-Setup"

func (h *Handler) modelOAuthSetupConfig(key string, connected bool) (*models.LLMConfig, error) {
	modelOAuthSetups.Lock()
	defer modelOAuthSetups.Unlock()
	session := modelOAuthSetups.sessions[key]
	if session == nil || session.owner != h || time.Now().After(session.expires) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "Sign-in session expired. Connect OAuth again.")
	}
	if connected && session.failure != "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, session.failure)
	}
	if connected && session.config.OAuthAccessToken == "" {
		return nil, echo.NewHTTPError(http.StatusConflict, "Complete OAuth sign-in first.")
	}
	config := session.config
	return &config, nil
}

func (h *Handler) BeginModelOAuthSetup(c echo.Context) error {
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20)
	config := &models.LLMConfig{Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth}
	mode := modelFormCreate
	targetID := strings.TrimSpace(c.FormValue("model_config_id"))
	if targetID != "" {
		saved, err := h.llmConfigRepo.GetByID(c.Request().Context(), targetID)
		if err != nil {
			return err
		}
		if saved == nil {
			return echo.NewHTTPError(http.StatusNotFound, "Model not found")
		}
		if saved.Provider == models.ProviderOpenAICompatible && saved.AuthMethod == models.AuthMethodOAuth && saved.PresetSlug == "custom" {
			*config = *saved
			clearOAuthCredentials(config)
			config.ID = ""
			config.OAuthConnectionID = ""
			mode = modelFormUpdate
		}
	}
	applyModelOAuthForm(c, config, mode)
	if err := applyOpenAICompatibleForm(c, config); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if config.PresetSlug != "custom" {
		return echo.NewHTTPError(http.StatusBadRequest, "This sign-in is for custom OAuth endpoints.")
	}
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return err
	}
	key := base64.RawURLEncoding.EncodeToString(keyBytes)
	modelOAuthSetups.Lock()
	modelOAuthSetups.sessions[key] = &modelOAuthSetup{owner: h, targetID: targetID, config: *config, expires: time.Now().Add(oauthFlowLifetime)}
	modelOAuthSetups.Unlock()
	time.AfterFunc(oauthFlowLifetime, func() { removeModelOAuthSetup(key) })
	if err := h.initiateOAuthForConfig(c, config, key); err != nil {
		removeModelOAuthSetup(key)
		return err
	}
	return nil
}

func removeModelOAuthSetup(key string) {
	modelOAuthSetups.Lock()
	delete(modelOAuthSetups.sessions, key)
	modelOAuthSetups.Unlock()
	shutdownPreviousOAuthServer(key)
}

func (h *Handler) ModelOAuthSetupStatus(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	config, err := h.modelOAuthSetupConfig(c.Request().Header.Get(modelOAuthSetupHeader), false)
	if err != nil {
		return err
	}
	modelOAuthSetups.Lock()
	session := modelOAuthSetups.sessions[c.Request().Header.Get(modelOAuthSetupHeader)]
	failure := ""
	if session != nil {
		failure = session.failure
	}
	modelOAuthSetups.Unlock()
	if failure != "" {
		return echo.NewHTTPError(http.StatusBadRequest, failure)
	}
	return c.JSON(http.StatusOK, map[string]bool{"connected": config.OAuthAccessToken != ""})
}

func (h *Handler) attachModelOAuthSetup(c echo.Context, config *models.LLMConfig) (string, error) {
	key := strings.TrimSpace(c.FormValue("oauth_setup_session"))
	if key == "" {
		return "", nil
	}
	signedIn, err := h.modelOAuthSetupConfig(key, true)
	if err != nil {
		return "", err
	}
	modelOAuthSetups.Lock()
	session := modelOAuthSetups.sessions[key]
	matchesTarget := session != nil && session.targetID == config.ID
	modelOAuthSetups.Unlock()
	if !matchesTarget {
		return "", echo.NewHTTPError(http.StatusConflict, "Sign-in belongs to a different model configuration.")
	}
	if config.Provider != models.ProviderOpenAICompatible || config.AuthMethod != models.AuthMethodOAuth || config.PresetSlug != "custom" ||
		oauthSecurityConfigChanged(*signedIn, *config) || signedIn.ExtraHeadersJSON != config.ExtraHeadersJSON {
		return "", echo.NewHTTPError(http.StatusConflict, "Connection settings changed. Connect OAuth again before saving the model.")
	}
	config.OAuthAccessToken = signedIn.OAuthAccessToken
	config.OAuthRefreshToken = signedIn.OAuthRefreshToken
	config.OAuthExpiresAt = signedIn.OAuthExpiresAt
	config.CustomAuthStateJSON = signedIn.CustomAuthStateJSON
	config.OAuthNeedsReauth = false
	return key, nil
}

func (h *Handler) finishModelOAuthSetup(key, access, refresh string, expires int64, state string) error {
	modelOAuthSetups.Lock()
	defer modelOAuthSetups.Unlock()
	session := modelOAuthSetups.sessions[key]
	if session == nil || session.owner != h || time.Now().After(session.expires) {
		return fmt.Errorf("sign-in session expired; connect OAuth again")
	}
	session.config.OAuthAccessToken = access
	session.config.OAuthRefreshToken = refresh
	session.config.OAuthExpiresAt = expires
	session.config.CustomAuthStateJSON = state
	return nil
}

func (h *Handler) CancelModelOAuthSetup(c echo.Context) error {
	key := c.Request().Header.Get(modelOAuthSetupHeader)
	if _, err := h.modelOAuthSetupConfig(key, false); err != nil {
		return err
	}
	removeModelOAuthSetup(key)
	return c.NoContent(http.StatusNoContent)
}

func failModelOAuthSetup(key string) {
	if key == "" {
		return
	}
	modelOAuthSetups.Lock()
	defer modelOAuthSetups.Unlock()
	if session := modelOAuthSetups.sessions[key]; session != nil {
		session.failure = "Sign-in failed or was cancelled. Connect OAuth again."
	}
}
