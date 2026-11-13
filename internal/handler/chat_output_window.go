package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ChatOutputBefore serves preceding complete public blocks for automatic scroll
// loading. before=0 refreshes the latest window after a stale cursor. Authorization
// is the same project boundary as the containing thread.
func (h *Handler) ChatOutputBefore(c echo.Context) error {
	ctx := c.Request().Context()
	project, err := h.getCurrentProjectID(c)
	if err != nil || project == "" {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	before, err := strconv.ParseInt(c.QueryParam("before"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid output cursor")
	}
	store, ok := h.execRepo.(interface {
		ReadProjectDisplayTail(context.Context, string, string, int64, int) (models.DisplayTail, error)
	})
	if !ok {
		return echo.NewHTTPError(http.StatusNotImplemented)
	}
	tail, err := store.ReadProjectDisplayTail(ctx, project, c.Param("id"), before, 1024*1024)
	if errors.Is(err, persistence.ErrNotFound) {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusConflict, "Output changed; refresh the thread")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	if before != 0 && tail.Revision != c.QueryParam("revision") {
		return echo.NewHTTPError(http.StatusConflict, "Output changed; refresh the thread")
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, tail)
}
