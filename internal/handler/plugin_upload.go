package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
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
	destination, err := h.pluginSettingsDestination(c, projectID)
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	work, err := os.MkdirTemp("", "ov-plugin-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	files, paths, err := readPluginUpload(c.Request(), work)
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	root := filepath.Join(work, "package")
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	if err = stagePluginUpload(root, files, paths); err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	pkg, err := h.pagePlugins.InstallFromDir(c.Request().Context(), root, by)
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	if projectID == models.AllProjectsID {
		if err := h.addInstalledPluginPages(c.Request().Context(), destination, pkg); err != nil {
			return h.renderPluginSettings(c, projectID, "", err)
		}
	}
	return h.renderPluginSettings(c, projectID, fmt.Sprintf("Installed %s %s", pkg.Name, pkg.Version), nil)
}

// Browser directory uploads carry relative paths separately because multipart
// filenames discard directories. Never trust either field as a filesystem path.
func stagePluginUpload(root string, files []pluginUploadFile, paths []string) error {
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
		total += header.size
		if total > pageplugins.MaxPackageBytes {
			return fmt.Errorf("package exceeds 64 MiB")
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := os.Rename(header.path, target); err != nil {
			return err
		}
	}
	if !seen["plugin.json"] {
		return fmt.Errorf("selected folder must contain plugin.json")
	}
	// Browser uploads do not preserve executable bits. Restore them only for
	// manifest-declared backend files.
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

// Spool each file independently rather than using ReadForm's global 1,000-part
// limit. File count, aggregate content, metadata, and HTTP body remain bounded.
type pluginUploadFile struct {
	path string
	size int64
}

func readPluginUpload(r *http.Request, work string) ([]pluginUploadFile, []string, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, fmt.Errorf("invalid folder upload")
	}
	var files []pluginUploadFile
	var paths []string
	var total int64
	metadata := 0
	fail := func(err error) ([]pluginUploadFile, []string, error) {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return nil, nil, fmt.Errorf("upload exceeds the request size limit (64 MiB of package files plus metadata)")
		}
		return nil, nil, fmt.Errorf("invalid folder upload: %w", err)
	}
	for parts := 0; ; parts++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(err)
		}
		if parts >= 2*pageplugins.MaxPackageFiles+16 {
			return nil, nil, fmt.Errorf("too many upload fields")
		}
		if part.FormName() == "files" && part.FileName() != "" {
			if len(files) >= pageplugins.MaxPackageFiles {
				return nil, nil, fmt.Errorf("too many files: maximum %d", pageplugins.MaxPackageFiles)
			}
			file, err := os.CreateTemp(work, "file-")
			if err != nil {
				return fail(err)
			}
			n, copyErr := io.Copy(file, io.LimitReader(part, int64(pageplugins.MaxPackageBytes)-total+1))
			closeErr := file.Close()
			if copyErr != nil {
				return fail(copyErr)
			}
			if closeErr != nil {
				return fail(closeErr)
			}
			total += n
			if total > pageplugins.MaxPackageBytes {
				return nil, nil, fmt.Errorf("package files exceed 64 MiB")
			}
			files = append(files, pluginUploadFile{file.Name(), n})
		} else {
			data, err := io.ReadAll(io.LimitReader(part, 8193))
			if err != nil {
				return fail(err)
			}
			metadata += len(data)
			if len(data) > 8192 || metadata > 2<<20 {
				return nil, nil, fmt.Errorf("upload metadata is too large")
			}
			if part.FormName() == "paths" {
				if len(paths) >= pageplugins.MaxPackageFiles {
					return nil, nil, fmt.Errorf("too many files: maximum %d", pageplugins.MaxPackageFiles)
				}
				paths = append(paths, string(data))
			}
		}
		part.Close()
	}
	return files, paths, nil
}
