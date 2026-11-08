package handler

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/pageplugins"
)

func (h *Handler) PluginSettingsUpload(c echo.Context) error {
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, pageplugins.MaxPackageBytes+(4<<20))
	by, err := h.requirePluginAdmin(c)
	if err != nil {
		return err
	}
	projectID, err := h.pluginSettingsProject(c)
	if err != nil {
		return err
	}
	form, err := c.MultipartForm()
	if c.Request().MultipartForm != nil {
		defer c.Request().MultipartForm.RemoveAll()
	}
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", fmt.Errorf("choose a package folder up to 64 MiB"))
	}
	root, err := os.MkdirTemp("", "ov-plugin-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err = stagePluginUpload(root, form.File["files"], form.Value["paths"]); err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	pkg, err := h.pagePlugins.InstallFromDir(c.Request().Context(), root, by)
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	return h.renderPluginSettings(c, projectID, fmt.Sprintf("Installed %s %s", pkg.Name, pkg.Version), nil)
}

// Browser directory uploads carry relative paths separately because multipart
// filenames discard directories. Never trust either field as a filesystem path.
func stagePluginUpload(root string, files []*multipart.FileHeader, paths []string) error {
	if len(files) == 0 || len(files) > pageplugins.MaxPackageFiles || len(paths) != len(files) {
		return fmt.Errorf("choose a package folder containing plugin.json (maximum %d files)", pageplugins.MaxPackageFiles)
	}
	seen := map[string]bool{}
	folder := ""
	var total int64
	for i, header := range files {
		rel := paths[i]
		if strings.ContainsAny(rel, "\\\x00:") {
			return fmt.Errorf("invalid package path")
		}
		parts := strings.Split(rel, "/")
		if len(parts) < 2 {
			return fmt.Errorf("select a folder, not individual files")
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				return fmt.Errorf("invalid package path")
			}
		}
		if i == 0 {
			folder = parts[0]
		}
		if parts[0] != folder {
			return fmt.Errorf("select one package folder")
		}
		rel = strings.Join(parts[1:], "/")
		if seen[rel] {
			return fmt.Errorf("duplicate package path: %s", rel)
		}
		seen[rel] = true
		total += header.Size
		if total > pageplugins.MaxPackageBytes {
			return fmt.Errorf("package exceeds 64 MiB")
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		src, err := header.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			src.Close()
			return err
		}
		n, copyErr := io.Copy(dst, io.LimitReader(src, header.Size+1))
		src.Close()
		closeErr := dst.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != header.Size {
			return fmt.Errorf("invalid upload size")
		}
	}
	if !seen["plugin.json"] {
		return fmt.Errorf("selected folder must contain plugin.json")
	}
	// Browser uploads do not preserve executable bits. Restore them only for
	// manifest-declared backend files; execution still requires admin approval.
	raw, err := os.ReadFile(filepath.Join(root, "plugin.json"))
	if err != nil {
		return err
	}
	manifest, err := pageplugins.ParseManifest(raw)
	if err != nil {
		return err
	}
	if manifest.Backend != nil {
		for _, rel := range manifest.Backend.Executables {
			target, err := pageplugins.SafeJoin(root, rel)
			if err != nil {
				return err
			}
			if err := os.Chmod(target, 0700); err != nil {
				return err
			}
		}
	}
	return nil
}
