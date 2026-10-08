package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

const modelFavoritesSettingKey = "ui.model_favorites"

type modelFavoritesRequest struct {
	ModelID   string   `json:"model_id"`
	Favorite  *bool    `json:"favorite"`
	ImportIDs []string `json:"import_ids"`
}

func (h *Handler) GetModelFavorites(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "settings unavailable")
	}
	value, err := h.settingsRepo.Get(c.Request().Context(), modelFavoritesSettingKey)
	if err != nil {
		return err
	}
	favorites, err := decodeModelFavorites(value)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, favorites)
}

func (h *Handler) SaveModelFavorites(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "settings unavailable")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 64<<10)
	var req modelFavoritesRequest
	dec := json.NewDecoder(c.Request().Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid favorites payload")
	}
	if (req.Favorite == nil) == (req.ImportIDs == nil) || len(req.ImportIDs) > 1000 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid favorites payload")
	}
	if req.Favorite != nil {
		if !validModelFavoriteID(req.ModelID) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid model ID")
		}
	} else {
		if req.ModelID != "" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid favorites payload")
		}
		for _, id := range req.ImportIDs {
			if !validModelFavoriteID(id) {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid model ID")
			}
		}
	}
	ctx := c.Request().Context()
	for {
		old, err := h.settingsRepo.Get(ctx, modelFavoritesSettingKey)
		if err != nil {
			return err
		}
		favorites, err := decodeModelFavorites(old)
		if err != nil {
			return err
		}
		if req.Favorite != nil {
			if *req.Favorite {
				favorites[req.ModelID] = true
			} else {
				delete(favorites, req.ModelID)
			}
		} else {
			for _, id := range req.ImportIDs {
				favorites[id] = true
			}
		}
		value, err := json.Marshal(favorites)
		if err != nil {
			return err
		}
		updated, err := h.settingsRepo.CompareAndSet(ctx, modelFavoritesSettingKey, old, string(value), nil)
		if err != nil {
			return err
		}
		if updated {
			return c.JSON(http.StatusOK, favorites)
		}
	}
}

func decodeModelFavorites(value string) (map[string]bool, error) {
	favorites := map[string]bool{}
	if value == "" {
		return favorites, nil
	}
	if err := json.Unmarshal([]byte(value), &favorites); err != nil {
		return nil, err
	}
	if favorites == nil {
		favorites = map[string]bool{}
	}
	return favorites, nil
}

func validModelFavoriteID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && id != "auto" && id != "default"
}
