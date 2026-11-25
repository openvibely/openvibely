package handler

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
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

type modelFavoritesArgs struct {
	ProjectID   string
	Request     *modelFavoritesRequest
	HasFavorite bool
	HasImport   bool
}

var actModelFavorites = defineAction("models.favorites", func(h *Handler, ctx context.Context, a modelFavoritesArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.View, nil
}, func(h *Handler, ctx context.Context, a modelFavoritesArgs) (map[string]bool, error) {
	if a.Request == nil && (a.HasFavorite || a.HasImport) {
		a.Request = &modelFavoritesRequest{}
	}
	if a.Request != nil {
		if a.HasFavorite && a.Request.Favorite == nil {
			value := false
			a.Request.Favorite = &value
		}
		if a.HasImport && a.Request.ImportIDs == nil {
			a.Request.ImportIDs = []string{}
		}
		return h.updateModelFavorites(ctx, *a.Request)
	}
	value, err := h.settingsRepo.Get(ctx, modelFavoritesSettingKey)
	if err != nil {
		return nil, err
	}
	return decodeModelFavorites(value)
})

func (h *Handler) modelFavoritesResponse(c echo.Context, req *modelFavoritesRequest) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "settings unavailable")
	}
	project := c.QueryParam("project_id")
	args := modelFavoritesArgs{ProjectID: project, Request: req}
	if req != nil {
		args.HasFavorite = req.Favorite != nil
		args.HasImport = req.ImportIDs != nil
	}
	favorites, err := actModelFavorites(h, c.Request().Context(), args)
	if err != nil {
		return err
	}
	if server, ok := remoteid.ServerOf(project); ok {
		scoped := make(map[string]bool, len(favorites))
		for id, value := range favorites {
			scoped[remoteid.ID(server, id)] = value
		}
		favorites = scoped
	}
	return c.JSON(http.StatusOK, favorites)
}

func (h *Handler) GetModelFavorites(c echo.Context) error {
	return h.modelFavoritesResponse(c, nil)
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
	return h.modelFavoritesResponse(c, &req)
}

func (h *Handler) updateModelFavorites(ctx context.Context, req modelFavoritesRequest) (map[string]bool, error) {
	if (req.Favorite == nil) == (req.ImportIDs == nil) || len(req.ImportIDs) > 1000 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid favorites payload")
	}
	if req.Favorite != nil {
		if !validModelFavoriteID(req.ModelID) {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid model ID")
		}
	} else {
		if req.ModelID != "" {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid favorites payload")
		}
		for _, id := range req.ImportIDs {
			if !validModelFavoriteID(id) {
				return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid model ID")
			}
		}
	}
	for {
		old, err := h.settingsRepo.Get(ctx, modelFavoritesSettingKey)
		if err != nil {
			return nil, err
		}
		favorites, err := decodeModelFavorites(old)
		if err != nil {
			return nil, err
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
			return nil, err
		}
		updated, err := h.settingsRepo.CompareAndSet(ctx, modelFavoritesSettingKey, old, string(value), nil)
		if err != nil {
			return nil, err
		}
		if updated {
			return favorites, nil
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
