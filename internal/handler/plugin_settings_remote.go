package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type pluginSettingsArgs struct {
	ProjectID, PackageID, PageID, InstanceID, Path, Action string
	AddPages                                               bool
}

func pluginSettingsOwner(h *Handler, ctx context.Context, a pluginSettingsArgs) (string, authz.Permission, error) {
	if err := h.requireActionAdmin(ctx); err != nil {
		return "", authz.Manage, err
	}
	if h.pagePlugins == nil {
		return "", authz.Manage, statusErr(http.StatusNotFound, "plugin page not found")
	}
	if a.InstanceID != "" {
		in, err := h.pagePlugins.Store().GetInstance(ctx, a.InstanceID)
		if err != nil {
			return "", authz.Manage, err
		}
		if in == nil || (!models.IsAggregateProject(a.ProjectID) && in.ProjectID != a.ProjectID) {
			return "", authz.Manage, statusErr(http.StatusNotFound, "plugin page not found")
		}
		return in.ProjectID, authz.Manage, nil
	}
	if models.IsAggregateProject(a.ProjectID) && a.Action == "remove-package" {
		return instanceActionProject, authz.Manage, nil
	}
	return a.ProjectID, authz.Manage, nil
}

var actPluginSettings = defineAction("page.settings", pluginSettingsOwner, func(h *Handler, ctx context.Context, a pluginSettingsArgs) (pages.PluginSettingsView, error) {
	return h.localPluginSettingsView(ctx, a.ProjectID)
})
var actPluginInstall = defineAction("page.install", pluginSettingsOwner, func(h *Handler, ctx context.Context, a pluginSettingsArgs) (*models.PagePluginPackage, error) {
	pkg, err := h.pagePlugins.InstallFromDir(ctx, a.Path, pluginPrincipal(ctx))
	if err == nil && a.AddPages {
		err = h.addInstalledPluginPages(ctx, a.ProjectID, pkg)
	}
	return pkg, err
})
var actPluginCreate = defineAction("page.create", pluginSettingsOwner, func(h *Handler, ctx context.Context, a pluginSettingsArgs) (*models.PagePluginInstance, error) {
	return h.pagePlugins.CreateInstance(ctx, a.ProjectID, a.PackageID, a.PageID)
})
var actPluginManage = defineAction("page.manage", pluginSettingsOwner, func(h *Handler, ctx context.Context, a pluginSettingsArgs) (bool, error) {
	switch a.Action {
	case "enable":
		return true, h.pagePlugins.Enable(ctx, a.InstanceID)
	case "disable":
		return true, h.pagePlugins.Disable(ctx, a.InstanceID)
	case "remove":
		return true, h.pagePlugins.RemoveInstance(ctx, a.InstanceID)
	case "remove-package":
		if a.InstanceID != "" {
			return false, pluginNotFound()
		}
		return true, h.pagePlugins.RemovePackage(ctx, a.PackageID)
	}
	return false, statusErr(http.StatusBadRequest, "unknown page action")
})

func (h *Handler) pluginSettingsView(ctx context.Context, projectID string) (pages.PluginSettingsView, error) {
	if _, remote := remoteid.ServerOf(projectID); remote {
		return actPluginSettings(h, ctx, pluginSettingsArgs{ProjectID: projectID})
	}
	if !models.IsAggregateProject(projectID) {
		return h.localPluginSettingsView(ctx, projectID)
	}
	out, err := h.localPluginSettingsView(ctx, projectID)
	if err != nil {
		return out, err
	}
	ids, err := h.cardActionProjects(ctx, projectID)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	projectSeen := map[string]bool{}
	for _, p := range out.Projects {
		projectSeen[p.ID] = true
	}
	for _, p := range out.Packages {
		seen[p.Package.ID] = true
	}
	for _, id := range ids {
		if _, remote := remoteid.ServerOf(id); !remote {
			continue
		}
		v, err := actPluginSettings(h, ctx, pluginSettingsArgs{ProjectID: id})
		if err != nil {
			return out, err
		}
		out.Instances = append(out.Instances, v.Instances...)
		for _, p := range v.Projects {
			if !projectSeen[p.ID] && models.InScope(ctx, p.ID) {
				projectSeen[p.ID] = true
				out.Projects = append(out.Projects, p)
			}
		}
		for _, p := range v.Packages {
			if !seen[p.Package.ID] {
				seen[p.Package.ID] = true
				out.Packages = append(out.Packages, p)
			}
		}
	}
	return out, nil
}

type pluginUploadChunkArgs struct {
	ProjectID, Token, Name string
	Offset                 int64
	Data                   []byte `remote:"raw"`
}

var actPluginUploadChunk = defineAction("page.upload.chunk", func(h *Handler, ctx context.Context, a pluginUploadChunkArgs) (string, authz.Permission, error) {
	return pluginSettingsOwner(h, ctx, pluginSettingsArgs{ProjectID: a.ProjectID})
}, func(h *Handler, ctx context.Context, a pluginUploadChunkArgs) (bool, error) {
	if a.Offset < 0 || len(a.Data) > pluginAssetChunkSize || a.Offset > int64(pageplugins.MaxPackageBytes)-int64(len(a.Data)) {
		return false, statusErr(http.StatusRequestEntityTooLarge, "package file too large")
	}
	p, err := stagedUploadPath("plugin-"+a.ProjectID, a.Token, a.Name)
	if err != nil {
		return false, err
	}
	if a.Offset == 0 {
		sweepStagedUploads()
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return false, err
		}
	}
	flags := os.O_WRONLY | os.O_APPEND
	if a.Offset == 0 {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(p, flags, 0600)
	if err != nil {
		return false, statusErr(http.StatusConflict, "upload out of order")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() != a.Offset {
		return false, statusErr(http.StatusConflict, "upload out of order")
	}
	_, err = f.Write(a.Data)
	return err == nil, err
})

type pluginUploadArgs struct {
	ProjectID string
	Files     []uploadFile
	Paths     []string
	AddPages  bool
}

var actPluginUpload = defineAction("page.upload", func(h *Handler, ctx context.Context, a pluginUploadArgs) (string, authz.Permission, error) {
	return pluginSettingsOwner(h, ctx, pluginSettingsArgs{ProjectID: a.ProjectID})
}, func(h *Handler, ctx context.Context, a pluginUploadArgs) (*models.PagePluginPackage, error) {
	if len(a.Files) == 0 || len(a.Files) > pageplugins.MaxPackageFiles || len(a.Files) != len(a.Paths) {
		return nil, statusErr(http.StatusBadRequest, "invalid package files")
	}
	key := "plugin-" + a.ProjectID
	defer removeStagedUploads(key, a.Files)
	work, err := os.MkdirTemp("", "ov-remote-plugin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	var files []pluginUploadFile
	for _, f := range a.Files {
		p, err := stagedUploadPath(key, f.Token, f.Name)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		files = append(files, pluginUploadFile{path: p, size: st.Size()})
	}
	if err = stagePluginUpload(work, files, a.Paths); err != nil {
		return nil, statusErr(http.StatusBadRequest, err.Error())
	}
	pkg, err := h.pagePlugins.InstallFromDir(ctx, work, pluginPrincipal(ctx))
	if err == nil && a.AddPages {
		err = h.addInstalledPluginPages(ctx, a.ProjectID, pkg)
	}
	return pkg, err
})

func (h *Handler) uploadRemotePlugin(ctx context.Context, projectID string, files []pluginUploadFile, paths []string, addPages bool) (*models.PagePluginPackage, error) {
	staged := make([]uploadFile, 0, len(files))
	buf := make([]byte, pluginAssetChunkSize)
	for _, file := range files {
		token := generateSessionID()
		f, err := os.Open(file.path)
		if err != nil {
			return nil, err
		}
		var offset int64
		for {
			n, readErr := io.ReadFull(f, buf)
			if n > 0 || offset == 0 {
				_, err = actPluginUploadChunk(h, ctx, pluginUploadChunkArgs{ProjectID: projectID, Token: token, Name: "file", Offset: offset, Data: buf[:n]})
				if err != nil {
					f.Close()
					return nil, err
				}
				offset += int64(n)
			}
			if readErr != nil {
				f.Close()
				if readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
					return nil, readErr
				}
				break
			}
		}
		staged = append(staged, uploadFile{Token: token, Name: "file", Size: file.size})
	}
	return actPluginUpload(h, ctx, pluginUploadArgs{ProjectID: projectID, Files: staged, Paths: paths, AddPages: addPages})
}
