package handler

import (
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

func (h *Handler) ProjectComputerFiles(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	write := c.Request().Method == http.MethodPut
	var data []byte
	var err error
	if write {
		data, err = io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 25<<20))
		if err != nil {
			return echo.NewHTTPError(413, "Choose a file up to 25 MB")
		}
	}
	result, kind, err := h.computerService(c).HumanFiles(c.Request().Context(), c.Param("id"), c.QueryParam("path"), data, write)
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(kind, "application/octet-stream") && !write {
		c.Response().Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(c.QueryParam("path"))}))
		return c.Blob(200, "application/octet-stream", result)
	}
	return c.Blob(200, "application/json", result)
}
