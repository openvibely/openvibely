package handler

import (
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

func (h *Handler) ProjectComputerChrome(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	state, err := h.computerService(c).Get(c.Request().Context(), c.Param("id"))
	if err != nil || !state.Held || state.HandoffKind != "request_chrome_login" {
		return echo.NewHTTPError(409, "No Chrome import awaiting approval")
	}
	if profile := c.QueryParam("profile"); profile != "" {
		sites, err := h.computerService(c).ChromeCookieChoices(c.Request().Context(), profile)
		if err != nil {
			return echo.NewHTTPError(400, err.Error())
		}
		return c.JSON(200, map[string]any{"cookies": sites})
	}
	profiles, err := h.computerService(c).ChromeProfiles()
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	pending, err := h.computerService(c).PendingChromeImport(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"profiles": profiles, "pending_import": pending})
}
func (h *Handler) ProjectComputerChromeImport(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	var input struct {
		GrantID    string                         `json:"grant_id"`
		Scope      string                         `json:"scope"`
		Hours      int                            `json:"hours"`
		Selections []models.ChromeCookieSelection `json:"selections"`
	}
	if json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 65536)).Decode(&input) != nil {
		return echo.NewHTTPError(400, "Select a profile and sites")
	}
	var count int
	var err error
	if input.GrantID != "" {
		count, err = h.computerService(c).ResumeChromeImport(c.Request().Context(), c.Param("id"), input.GrantID)
	} else {
		count, err = h.computerService(c).ImportChromeSelection(c.Request().Context(), c.Param("id"), input.Scope, input.Selections, input.Hours)
	}
	var partial *service.ChromeImportIncomplete
	if errors.As(err, &partial) {
		return c.JSON(409, map[string]any{"message": err.Error(), "grant_id": partial.GrantID, "imported": count})
	}
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(200, map[string]int{"imported": count})
}

func (h *Handler) ProjectComputerChromeGrants(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	offset := 0
	if raw := c.QueryParam("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > int(^uint(0)>>1)-200 {
			return echo.NewHTTPError(400, "Invalid permissions page")
		}
		offset = parsed
	}
	if c.Request().Method == "POST" {
		var input struct {
			ID string `json:"grant_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 1024)).Decode(&input) != nil || input.ID == "" {
			return echo.NewHTTPError(400, "Choose a grant")
		}
		if err := h.computerService(c).RevokeChromeGrant(c.Request().Context(), c.Param("id"), input.ID); err != nil {
			return echo.NewHTTPError(400, err.Error())
		}
	}
	grants, more, err := h.computerService(c).ChromeGrantsPage(c.Request().Context(), c.Param("id"), offset)
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"grants": grants, "offset": offset, "has_more": more, "next_offset": offset + len(grants)})
}
