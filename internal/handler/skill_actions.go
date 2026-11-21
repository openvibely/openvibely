package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/builtinskills"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type skillOperationArgs struct {
	ProjectID, Scope, Handle, Operation string
	Save                                skillSaveRequest `remote:"raw"`
	Value, CheckOnly                    bool
	Package                             *uploadedSkillPackage `remote:"raw"`
}
type skillOperationResult struct {
	Cards  []pages.SkillCard
	Detail skillDetailResponse        `remote:"raw"`
	Write  *agentlibrary.ImportResult `remote:"raw"`
}

var actSkillOperation = defineAction("skills.operation", func(h *Handler, ctx context.Context, a skillOperationArgs) (string, authz.Permission, error) {
	if err := h.requireActionAdmin(ctx); err != nil {
		return "", authz.Edit, err
	}
	if a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) {
		if err := h.requireActionProject(ctx, a.ProjectID, authz.View); err != nil {
			return "", authz.View, err
		}
	}
	return instanceActionProject, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a skillOperationArgs) (skillOperationResult, error) {
	return h.executeSkillOperation(ctx, a)
})

func (h *Handler) executeSkillOperation(ctx context.Context, a skillOperationArgs) (skillOperationResult, error) {
	var out skillOperationResult
	if _, ok := remoteid.ServerOf(a.ProjectID); ok {
		return out, fmt.Errorf("skill filesystem operation requires a local project")
	}
	projectRoot := ""
	if a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) {
		projectRoot = service.ProjectSkillRootForResolver(ctx, h.projectRepo, a.ProjectID)
	}
	if a.Operation == "list" {
		cards, err := h.listSkillCards(projectRoot, a.ProjectID)
		out.Cards = cards
		return out, err
	}
	root, err := h.rootForDialogScopeWithProjectRoot(a.Scope, projectRoot)
	if err != nil {
		return out, err
	}
	if a.Operation == "import" {
		if a.Package == nil {
			return out, statusErr(http.StatusBadRequest, "missing skill package")
		}
		pkg := a.Package
		decl, body, err := parseUploadedStandaloneSkillDeclaration(pkg.SkillMD, pkg.PackageName, a.Scope)
		if err != nil {
			return out, err
		}
		if decl.IsAgentRootDeclaration() || strings.TrimSpace(decl.Agent.Key) != "" || !validDialogSkillKey(decl.Skill.Key) {
			return out, statusErr(http.StatusBadRequest, "invalid standalone skill package")
		}
		decl.Agent.Key = ""
		decl.Skill.Scope = a.Scope
		importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: projectRoot}, nil)
		out.Write, err = importer.WriteSkill(ctx, decl, body)
		if err != nil {
			return out, err
		}
		dir, err := agentlibrary.SkillDir(root, decl.Skill.Key)
		if err != nil {
			return out, err
		}
		for _, file := range pkg.PackageFiles {
			if err := writeUploadedSkillPackageFile(dir, file.Path, file.Content); err != nil {
				return out, err
			}
		}
		return out, nil
	}
	if !validDialogSkillKey(a.Handle) {
		return out, statusErr(http.StatusBadRequest, "invalid skill handle")
	}
	path := filepath.Join(root, "skills", a.Handle)
	if a.Operation != "create" {
		if stat, err := os.Stat(path); err != nil || !stat.IsDir() {
			return out, statusErr(http.StatusNotFound, "skill not found at selected scope")
		}
	}
	if a.CheckOnly {
		return out, nil
	}
	switch a.Operation {
	case "detail":
		out.Detail, err = h.readSkillDetail(root, a.Scope, a.Handle, projectRoot)
	case "create", "edit":
		out.Write, err = h.writeStandaloneSkill(ctx, a.Save, a.Operation == "edit", a.Scope, projectRoot)
	case "delete":
		err = os.RemoveAll(path)
		if err == nil {
			_, err = agentlibrary.RemoveSkillIndexEntry(filepath.Join(root, "skills", "SKILLS.md"), a.Handle)
		}
	case "always_use":
		err = agentlibrary.SetSkillAlwaysUse(filepath.Join(root, "skills", "SKILLS.md"), a.Handle, a.Value)
	case "enabled":
		if a.Scope == "global" && builtinskills.IsBundledFile(filepath.Join("skills", strings.ToLower(a.Handle), "SKILL.md")) {
			err = agentskills.SetSkillEnabledPreference(root, a.Handle, a.Value)
			break
		}
		data, e := os.ReadFile(filepath.Join(path, "SKILL.md"))
		if e != nil {
			return out, e
		}
		decl, body, e := agentlibrary.ParseDeclaration(string(data))
		if e != nil {
			return out, e
		}
		decl.Skill.Enabled = nil
		if !a.Value {
			value := false
			decl.Skill.Enabled = &value
		}
		decl.Agent.Key = ""
		decl.Skill.Key = a.Handle
		decl.Skill.Scope = a.Scope
		_, err = agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: projectRoot}, nil).WriteSkill(ctx, decl, body)
	default:
		err = statusErr(http.StatusBadRequest, "unknown skill operation")
	}
	return out, err
}

func skillRequestProject(c echo.Context) string {
	project := c.QueryParam("project_id")
	if models.IsAggregateProject(project) {
		project = c.QueryParam("skill_project_id")
	}
	return project
}
func (h *Handler) remoteSkillOperation(c echo.Context, a skillOperationArgs) (skillOperationResult, bool, error) {
	if a.Scope == "global" {
		return skillOperationResult{}, false, nil
	}
	project := skillRequestProject(c)
	if _, ok := remoteid.ServerOf(project); !ok {
		return skillOperationResult{}, false, nil
	}
	if !models.InScope(c.Request().Context(), project) {
		return skillOperationResult{}, true, authz.ErrDenied
	}
	if a.Scope == "" {
		a.Scope = "project"
	}
	a.ProjectID = project
	out, err := actSkillOperation(h, c.Request().Context(), a)
	return out, true, err
}

func (h *Handler) deleteSkillSelection(c echo.Context, refs []bulkSkillRef) error {
	if len(refs) == 0 || len(refs) > bulkDeleteMaxItems {
		return statusErr(http.StatusBadRequest, "invalid selection size")
	}
	scope := c.QueryParam("project_id")
	actions := make([]skillOperationArgs, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Scope != "global" && ref.Scope != "project" {
			return statusErr(http.StatusBadRequest, "invalid skill scope")
		}
		project := ref.ProjectID
		if project == "" && !models.IsAggregateProject(scope) {
			project = scope
		}
		if models.IsAggregateProject(project) || (ref.Scope == "project" && project == "") {
			return statusErr(http.StatusBadRequest, "choose a project")
		}
		if ref.Scope == "global" {
			project = ""
		}
		if ref.Scope == "project" && scope != "" && !models.IsAggregateProject(scope) && project != scope {
			return authz.ErrDenied
		}
		if project != "" && !models.InScope(c.Request().Context(), project) {
			return authz.ErrDenied
		}
		key := project + ":" + ref.Scope + ":" + ref.Handle
		if seen[key] {
			continue
		}
		seen[key] = true
		actions = append(actions, skillOperationArgs{ProjectID: project, Scope: ref.Scope, Handle: ref.Handle, Operation: "delete"})
	}
	call := func(a skillOperationArgs) error {
		if _, ok := remoteid.ServerOf(a.ProjectID); ok {
			_, err := actSkillOperation(h, c.Request().Context(), a)
			return err
		}
		_, err := h.executeSkillOperation(c.Request().Context(), a)
		return err
	}
	for _, a := range actions {
		a.CheckOnly = true
		if err := call(a); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "all selected skills must exist and be accessible at the selected scope")
		}
	}
	var failures []string
	for _, a := range actions {
		if err := call(a); err != nil {
			failures = append(failures, fmt.Sprintf("%s/%s: %v", a.ProjectID, a.Handle, err))
		}
	}
	if len(failures) > 0 {
		return statusErr(http.StatusBadGateway, "Some skills could not be deleted; others may have been deleted. "+strings.Join(failures, "; "))
	}
	return nil
}
