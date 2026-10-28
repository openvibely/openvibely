package handler

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/pkg/filecontent"
	"github.com/openvibely/openvibely/pkg/spreadsheetpreviewindex"
	"github.com/openvibely/openvibely/web/templates/components"
)

// serveFileContent is called only after the owner route authorizes this file.
// The original remains byte-for-byte intact; preview decoding is separate.
func serveFileContent(c echo.Context, path, name string) error {
	if save, ok := c.Get(desktopSaveKey).(DesktopFileSaver); ok {
		f, err := os.Open(path)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, "file not found")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return echo.NewHTTPError(http.StatusNotFound, "file not found")
		}
		canceled, err := save(c.Request().Context(), filepath.Base(name), f)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Could not save the file. Please try again.").SetInternal(err)
		}
		return c.JSON(http.StatusOK, map[string]bool{"saved": !canceled, "canceled": canceled})
	}
	if c.QueryParam("spreadsheet-strings") == "1" {
		if c.Request().Method != http.MethodPost {
			return echo.NewHTTPError(http.StatusMethodNotAllowed, "method not allowed")
		}
		indexes, err := spreadsheetpreviewindex.DecodeIndexes(c.Request().Body)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid shared-string request")
		}
		indexPath := spreadsheetpreviewindex.SidecarPath(path)
		values, err := spreadsheetpreviewindex.Read(indexPath, indexes)
		if errors.Is(err, os.ErrNotExist) {
			job, started := spreadsheetpreviewindex.StartBuild(path)
			if started {
				c.Response().Header().Set("Cache-Control", "private, no-store")
				c.Response().Header().Set("Retry-After", "1")
				return c.NoContent(http.StatusAccepted)
			}
			select {
			case <-job.Done():
				if buildErr := job.Err(); buildErr != nil {
					return echo.NewHTTPError(http.StatusServiceUnavailable, "preview string index unavailable").SetInternal(buildErr)
				}
				values, err = spreadsheetpreviewindex.Read(indexPath, indexes)
			default:
				c.Response().Header().Set("Cache-Control", "private, no-store")
				c.Response().Header().Set("Retry-After", "1")
				return c.NoContent(http.StatusAccepted)
			}
		}
		if errors.Is(err, os.ErrNotExist) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "preview string index unavailable")
		}
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "could not read preview string index")
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		return c.JSON(http.StatusOK, values)
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	c.Response().Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data: blob:")
	if c.QueryParam("download") == "1" {
		return c.Attachment(path, name)
	}
	f, err := os.Open(path)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "file not found")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return echo.NewHTTPError(http.StatusNotFound, "file not found")
	}
	sample := make([]byte, 512)
	n, _ := f.Read(sample)
	media := filecontent.FileMediaType(name, sample[:n])
	ext := strings.ToLower(filepath.Ext(name))
	kind := components.FilePreviewKind(name)
	if ext == ".pdf" {
		media = "application/pdf"
	}
	if c.QueryParam("view") == "1" {
		c.Response().Header().Del("Content-Security-Policy")
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		base := c.Request().URL.String()
		text := ""
		if kind == "docx" || kind == "text" || kind == "markdown" || kind == "html" || kind == "json" {
			data, err := filecontent.ReadPath(path, filecontent.MaxFileBytes)
			if err != nil {
				return components.FilePreview(name, "unknown", "", base).Render(c.Request().Context(), c.Response())
			}
			if kind == "docx" {
				doc, _, err := filecontent.DocxPreview(name, data)
				if err != nil {
					return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
				}
				return components.FileDocumentPreview(name, doc, components.FileURLMode(base, "download")).Render(c.Request().Context(), c.Response())
			}
			var ok bool
			text, ok = filecontent.DecodeAttachmentText(data)
			if !ok {
				kind = "unsupported-encoding"
			}
			if len(text) > 1500000 {
				if kind == "json" {
					kind = "text"
				}
				text = string([]rune(text)[:min(len([]rune(text)), 1500000)]) + "\n[Preview limited. Download the original for full contents.]"
			}
		}
		return components.FilePreview(name, kind, text, base).Render(c.Request().Context(), c.Response())
	}
	if c.QueryParam("thumbnail") == "1" && (ext == ".heic" || ext == ".heif") {
		data, err := filecontent.ReadPath(path, filecontent.MaxFileBytes)
		if err != nil {
			return err
		}
		preview, err := filecontent.NativeImagePreview(c.Request().Context(), data)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnsupportedMediaType, err.Error())
		}
		return c.Blob(http.StatusOK, "image/jpeg", preview)
	}
	if ext == ".svg" {
		data, err := filecontent.ReadPath(path, 1<<20)
		if err != nil || !filecontent.IsSVG(name, data) {
			media = "text/plain"
		} else {
			media = "image/svg+xml"
		}
	}
	if ext == ".html" || ext == ".htm" {
		c.Response().Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' blob:; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'")
		c.Response().Header().Set("Referrer-Policy", "no-referrer")
		data, err := filecontent.ReadPath(path, filecontent.MaxFileBytes)
		if err != nil {
			return err
		}
		text, ok := filecontent.DecodeAttachmentText(data)
		if !ok {
			return echo.NewHTTPError(415, "HTML text encoding unavailable")
		}
		return c.Blob(http.StatusOK, "text/html; charset=utf-8", []byte(text))
	}
	if media == "" {
		media = "application/octet-stream"
	}
	c.Response().Header().Set("Content-Type", media)
	c.Response().Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	http.ServeContent(c.Response(), c.Request(), name, info.ModTime(), f)
	return nil
}
