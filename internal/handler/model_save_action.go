package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	llmmixture "github.com/openvibely/openvibely/internal/llm/mixture"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type modelSaveArgs struct {
	ProjectID, ID, OAuthConnectionID string
	Values                           url.Values `remote:"raw"`
	Mixture                          *llmmixture.Config
}
type modelSaveResult struct{ ID, OAuthConnectionID string }

var actModelSave = defineAction("model.save", func(h *Handler, ctx context.Context, a modelSaveArgs) (string, authz.Permission, error) {
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a modelSaveArgs) (modelSaveResult, error) {
	out := modelSaveResult{}
	if a.Values == nil {
		return out, statusErr(http.StatusBadRequest, "model fields are required")
	}
	form := ownerFormValues(a.Values)
	if _, present := a.Values["oauth_connection_id"]; present {
		a.Values.Set("oauth_connection_id", a.OAuthConnectionID)
	}
	if a.Mixture != nil {
		raw, err := json.Marshal(a.Mixture)
		if err != nil {
			return out, err
		}
		a.Values.Set("mixture_config_json", string(raw))
	}
	cfg := &models.LLMConfig{}
	mode := modelFormCreate
	if a.ID != "" {
		var err error
		cfg, err = h.llmConfigRepo.GetByID(ctx, a.ID)
		if err != nil {
			return out, err
		}
		if cfg == nil {
			return out, statusErr(http.StatusNotFound, "model not found")
		}
		mode = modelFormUpdate
	}
	previous := *cfg
	opts := modelFormOptions{mode: mode}
	if mode == modelFormUpdate {
		opts.beforeProviderSpecificFields = func() {
			changed := previous.Provider != cfg.Provider || previous.AuthMethod != cfg.AuthMethod
			if cfg.Provider == models.ProviderOpenAICompatible && cfg.AuthMethod == models.AuthMethodOAuth {
				cfg.APIKey = ""
			} else if key, ok := formValueIfPresent(form, "api_key"); ok && key != "" {
				cfg.APIKey = key
			} else if changed {
				cfg.APIKey = ""
			}
			if changed {
				if previous.Provider == models.ProviderOpenAICompatible && previous.AuthMethod == models.AuthMethodOAuth {
					cfg.CustomAuthConfigJSON = ""
				}
				clearOAuthState(cfg)
			}
		}
	}
	if err := h.normalizeBrowserModelForm(ctx, form, cfg, opts); err != nil {
		return out, statusErr(http.StatusBadRequest, err.Error())
	}
	syncCatalogProviderLimits(cfg, previous.Model)
	if mode == modelFormUpdate && previous.Provider == cfg.Provider && previous.AuthMethod == models.AuthMethodOAuth && cfg.AuthMethod == models.AuthMethodOAuth && oauthSecurityConfigChanged(previous, *cfg) {
		clearOAuthCredentials(cfg)
	}
	setupKey, err := h.attachModelOAuthSetupOnOwner(ctx, form, cfg)
	if err != nil {
		return out, err
	}
	if mode == modelFormCreate {
		err = h.llmConfigRepo.Create(ctx, cfg)
	} else {
		err = h.llmConfigRepo.Update(ctx, cfg)
	}
	if err != nil {
		if errors.Is(err, persistence.ErrOAuthConnectionChanged) {
			return out, statusErr(http.StatusConflict, err.Error())
		}
		if isModelValidationError(err) {
			return out, statusErr(http.StatusBadRequest, err.Error())
		}
		return out, err
	}
	if setupKey != "" {
		removeModelOAuthSetup(setupKey)
	}
	if h.workerSvc != nil {
		h.workerSvc.DispatchNext()
	}
	return modelSaveResult{ID: cfg.ID, OAuthConnectionID: cfg.OAuthConnectionID}, nil
})
