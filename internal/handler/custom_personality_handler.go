package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// listPersonalityCardPage assembles the complete compact Personality collection,
// applies collection state, and only then slices the requested page.
func (h *Handler) listPersonalityCardPage(ctx context.Context, page cardPageRequest, selected string) ([]pages.PersonalityListCard, bool, error) {
	var customs []models.CustomPersonality
	var err error
	if h.customPersonalityRepo != nil {
		customs, err = h.customPersonalityRepo.List(ctx)
		if err != nil {
			return nil, false, err
		}
	}
	cards := pages.BuildPersonalityListCards(strings.TrimSpace(selected), customs, personalityListState(page))
	start := page.Offset
	if start > len(cards) {
		start = len(cards)
	}
	end := start + page.PageSize + 1
	if end > len(cards) {
		end = len(cards)
	}
	items, hasMore := cardPageItems(cards[start:end], page.PageSize)
	return items, hasMore, nil
}

func personalityListState(page cardPageRequest) pages.PersonalityListState {
	active := ""
	if page.PersonalityActive != nil {
		if *page.PersonalityActive {
			active = "true"
		} else {
			active = "false"
		}
	}
	return pages.PersonalityListState{Search: page.Search, Kind: page.PersonalityKind, Active: active, Sort: page.PersonalitySort}
}

// renderPersonalitySection re-renders the personality section with current data.
func (h *Handler) renderPersonalitySection(c echo.Context) error {
	ctx := c.Request().Context()

	var personality string
	if h.settingsRepo != nil {
		personality, _ = h.settingsRepo.Get(ctx, "personality")
	}

	page := parseCardPageRequest(c)
	customs, hasMore, err := h.listPersonalityCardPage(ctx, page, personality)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load custom personalities")
	}
	setCardPageResponse(c, hasMore)

	return render(c, http.StatusOK, pages.PersonalitySectionPageWithPaginationState(personality, customs, hasMore, personalityListState(page)))
}

type customPersonalitySavePayload struct {
	Personality *models.CustomPersonality
	IsJSON      bool
}

func parseCustomPersonalitySavePayload(c echo.Context, key string) (*customPersonalitySavePayload, error) {
	var name, description, systemPrompt string
	isJSON := strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON)

	if isJSON {
		var req struct {
			Name         string `json:"name"`
			Key          string `json:"key"`
			Description  string `json:"description"`
			SystemPrompt string `json:"system_prompt"`
		}
		if err := c.Bind(&req); err != nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "Invalid JSON")
		}
		name = strings.TrimSpace(req.Name)
		if key == "" {
			key = strings.TrimSpace(req.Key)
		}
		description = strings.TrimSpace(req.Description)
		systemPrompt = strings.TrimSpace(req.SystemPrompt)
	} else {
		name = strings.TrimSpace(c.FormValue("name"))
		if key == "" {
			key = strings.TrimSpace(c.FormValue("key"))
		}
		description = strings.TrimSpace(c.FormValue("description"))
		systemPrompt = strings.TrimSpace(c.FormValue("system_prompt"))
	}

	p, err := service.NewCustomPersonalityFromFields(name, key, description, systemPrompt)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return &customPersonalitySavePayload{
		Personality: p,
		IsJSON:      isJSON,
	}, nil
}

// CreateCustomPersonality handles POST /personality/custom
func (h *Handler) CreateCustomPersonality(c echo.Context) error {
	if h.customPersonalityRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Custom personalities not configured")
	}

	payload, err := parseCustomPersonalitySavePayload(c, "")
	if err != nil {
		return err
	}
	p := payload.Personality
	if !payload.IsJSON || p.Key == "" {
		p.Key = service.NormalizeCustomPersonalityKey(p.Name)
	}
	if p.Key == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Key is required")
	}

	// Allow custom personalities to override presets by using the same key
	existing, err := h.customPersonalityRepo.GetByKey(c.Request().Context(), p.Key)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to check key uniqueness")
	}
	if existing != nil {
		return echo.NewHTTPError(http.StatusConflict, "A custom personality with this key already exists")
	}

	if err := h.customPersonalityRepo.Create(c.Request().Context(), p); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create custom personality")
	}

	if payload.IsJSON {
		return c.JSON(http.StatusCreated, p)
	}
	return h.renderPersonalitySection(c)
}

// GetCustomPersonality handles GET /personality/custom/:key
func (h *Handler) GetCustomPersonality(c echo.Context) error {
	if h.customPersonalityRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Custom personalities not configured")
	}

	key := c.Param("key")
	p, err := h.customPersonalityRepo.GetByKey(c.Request().Context(), key)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load personality")
	}
	if p == nil {
		for _, preset := range service.AllPersonalities() {
			if preset.Key == key && key != "" {
				return c.JSON(http.StatusOK, &models.CustomPersonality{
					Name:         preset.Name,
					Key:          preset.Key,
					Description:  preset.Description,
					SystemPrompt: service.GetPersonalityPrompt(key),
				})
			}
		}
		return echo.NewHTTPError(http.StatusNotFound, "Custom personality not found")
	}

	return c.JSON(http.StatusOK, p)
}

// UpdateCustomPersonality handles PUT /personality/custom/:key
func (h *Handler) UpdateCustomPersonality(c echo.Context) error {
	if h.customPersonalityRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Custom personalities not configured")
	}

	key := c.Param("key")

	payload, err := parseCustomPersonalitySavePayload(c, key)
	if err != nil {
		return err
	}
	p := payload.Personality

	existing, err := h.customPersonalityRepo.GetByKey(c.Request().Context(), key)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load personality")
	}

	if existing != nil {
		if err := h.customPersonalityRepo.Update(c.Request().Context(), key, p); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to update custom personality")
		}
	} else {
		if key == "" || !service.IsPresetPersonality(key) {
			return echo.NewHTTPError(http.StatusNotFound, "Custom personality not found")
		}
		if err := h.customPersonalityRepo.Create(c.Request().Context(), p); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save personality")
		}
	}

	// Refresh from DB to get the complete object with ID and Key
	updated, err := h.customPersonalityRepo.GetByKey(c.Request().Context(), key)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to retrieve updated personality")
	}

	if payload.IsJSON {
		return c.JSON(http.StatusOK, updated)
	}
	return h.renderPersonalitySection(c)
}

func (h *Handler) DeleteCustomPersonalitiesBulk(c echo.Context) error {
	if h.customPersonalityRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "custom personalities unavailable")
	}
	keys, err := decodeBulkIDs(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	active := ""
	if h.settingsRepo != nil {
		active, _ = h.settingsRepo.Get(c.Request().Context(), "personality")
	}
	for _, key := range keys {
		if key == active {
			return echo.NewHTTPError(http.StatusBadRequest, "the active personality cannot be deleted")
		}
		if service.IsPresetPersonality(key) {
			return echo.NewHTTPError(http.StatusBadRequest, "built-in personalities and their overrides cannot be bulk deleted")
		}
		personality, getErr := h.customPersonalityRepo.GetByKey(c.Request().Context(), key)
		if getErr != nil || personality == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "all selected personalities must be inactive custom entries")
		}
	}
	if err := h.customPersonalityRepo.DeleteBulk(c.Request().Context(), keys); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]int{"deleted": len(keys)})
}

// DeleteCustomPersonality handles DELETE /personality/custom/:key
func (h *Handler) DeleteCustomPersonality(c echo.Context) error {
	if h.customPersonalityRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Custom personalities not configured")
	}

	key := c.Param("key")
	// Check if exists first
	existing, err := h.customPersonalityRepo.GetByKey(c.Request().Context(), key)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to check personality existence")
	}
	if existing == nil && !service.IsPresetPersonality(key) {
		return echo.NewHTTPError(http.StatusNotFound, "Custom personality not found")
	}

	if existing != nil {
		if err := h.customPersonalityRepo.Delete(c.Request().Context(), key); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete custom personality")
		}
	}

	// Reset to default if the deleted personality was active
	if h.settingsRepo != nil {
		current, _ := h.settingsRepo.Get(c.Request().Context(), "personality")
		if current == key {
			_ = h.settingsRepo.Set(c.Request().Context(), "personality", "")
		}
	}

	isJSON := strings.Contains(c.Request().Header.Get(echo.HeaderAccept), echo.MIMEApplicationJSON) ||
		strings.Contains(c.Request().Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON)
	if isJSON {
		return c.NoContent(http.StatusNoContent)
	}
	return h.renderPersonalitySection(c)
}
