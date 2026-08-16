package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type skillAlwaysUseRequest struct {
	AlwaysUse bool   `json:"always_use"`
	Scope     string `json:"scope"`
}

func (h *Handler) SetSkillAlwaysUse(c echo.Context) error {
	handle := strings.TrimSpace(c.Param("skill"))
	if !validDialogSkillKey(handle) {
		return echo.NewHTTPError(http.StatusBadRequest, "skill handle must be a slug")
	}
	var req skillAlwaysUseRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
	}
	scope := h.dialogStandaloneSkillScope(c, req.Scope)
	root, err := h.rootForDialogScope(c, scope)
	if err != nil {
		return err
	}
	// Verify skill exists at scope before mutating the index.
	skillPath := filepath.Join(root, "skills", handle, "SKILL.md")
	if _, statErr := os.Stat(skillPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return echo.NewHTTPError(http.StatusNotFound, "skill not found at selected scope")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, statErr.Error())
	}
	indexPath := filepath.Join(root, "skills", "SKILLS.md")
	if err := agentlibrary.SetSkillAlwaysUse(indexPath, handle, req.AlwaysUse); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return h.ListSkills(c)
}

type skillSaveRequest struct {
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Body        string `json:"body"`
	Enabled     *bool  `json:"enabled"`
}

func (h *Handler) ListSkills(c echo.Context) error {
	skills, err := h.listStandaloneSkills(c)
	if err != nil {
		return err
	}
	canManage := h.agentSkillRoot != "" || h.currentProjectSkillRoot(c) != ""
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.SkillsContent(skills, canManage))
	}
	currentProjectID, _ := h.getCurrentProjectID(c)
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.Skills(projects, currentProjectID, skills, canManage))
}

func (h *Handler) CreateSkill(c echo.Context) error {
	var req skillSaveRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
	}
	res, err := h.writeStandaloneSkillFromDialog(c, req, false)
	if err != nil {
		return err
	}
	eventType := models.SkillEventEdited
	if res != nil && len(res.Created) > 0 {
		eventType = models.SkillEventCreated
	}
	h.recordManualSkillEvent(c, eventType, req.Handle, req.Scope, "")
	return h.ListSkills(c)
}

func (h *Handler) UpdateSkill(c echo.Context) error {
	handle := strings.TrimSpace(c.Param("skill"))
	var req skillSaveRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
	}
	if req.Handle == "" {
		req.Handle = handle
	}
	if req.Handle != handle {
		return echo.NewHTTPError(http.StatusBadRequest, "skill handle mismatch")
	}
	if _, err := h.writeStandaloneSkillFromDialog(c, req, true); err != nil {
		return err
	}
	h.recordManualSkillEvent(c, models.SkillEventEdited, req.Handle, req.Scope, "")
	return h.ListSkills(c)
}

func (h *Handler) ImportSkillPackage(c echo.Context) error {
	scope := h.dialogStandaloneSkillScope(c, c.FormValue("scope"))
	root, err := h.rootForDialogScope(c, scope)
	if err != nil {
		return err
	}
	form, err := c.MultipartForm()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "skill package upload is required")
	}
	files := form.File["files"]
	if len(files) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "skill package upload is required")
	}
	pkg, err := readUploadedSkillPackage(files, form.Value["paths"])
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	decl, body, err := parseUploadedStandaloneSkillDeclaration(pkg.SkillMD, pkg.PackageName, scope)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if decl.IsAgentRootDeclaration() || strings.TrimSpace(decl.Agent.Key) != "" {
		return echo.NewHTTPError(http.StatusBadRequest, "standalone skill packages must not set agent.key")
	}
	if !validDialogSkillKey(decl.Skill.Key) {
		return echo.NewHTTPError(http.StatusBadRequest, "skill package SKILL.md must declare a valid skill.key")
	}
	decl.Agent.Key = ""
	decl.Skill.Scope = scope
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: h.currentProjectSkillRoot(c)}, nil)
	res, err := importer.WriteSkill(c.Request().Context(), decl, body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	skillDir, err := agentlibrary.SkillDir(root, decl.Skill.Key)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	for _, file := range pkg.PackageFiles {
		if err := writeUploadedSkillPackageFile(skillDir, file.Path, file.Content); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	eventType := models.SkillEventEdited
	if res != nil && len(res.Created) > 0 {
		eventType = models.SkillEventCreated
	}
	h.recordManualSkillEvent(c, eventType, decl.Skill.Key, scope, "")
	return h.ListSkills(c)
}

func (h *Handler) DeleteSkill(c echo.Context) error {
	handle := strings.TrimSpace(c.Param("skill"))
	if !validDialogSkillKey(handle) {
		return echo.NewHTTPError(http.StatusBadRequest, "skill handle must be a slug")
	}
	scope := h.dialogStandaloneSkillScope(c, c.QueryParam("scope"))
	root, err := h.rootForDialogScope(c, scope)
	if err != nil {
		return err
	}
	path := filepath.Join(root, "skills", handle)
	if stat, statErr := os.Stat(path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return echo.NewHTTPError(http.StatusNotFound, "skill not found at selected scope")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, statErr.Error())
	} else if !stat.IsDir() {
		return echo.NewHTTPError(http.StatusBadRequest, "skill path is not a directory")
	}
	if err := os.RemoveAll(path); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if err := removeStandaloneSkillIndexEntry(filepath.Join(root, "skills", "SKILLS.md"), handle); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return h.ListSkills(c)
}

type skillEnabledRequest struct {
	Enabled bool   `json:"enabled"`
	Scope   string `json:"scope"`
}

func (h *Handler) SetSkillEnabled(c echo.Context) error {
	handle := strings.TrimSpace(c.Param("skill"))
	if !validDialogSkillKey(handle) {
		return echo.NewHTTPError(http.StatusBadRequest, "skill handle must be a slug")
	}
	var req skillEnabledRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
	}
	scope := h.dialogStandaloneSkillScope(c, req.Scope)
	root, err := h.rootForDialogScope(c, scope)
	if err != nil {
		return err
	}
	skillPath := filepath.Join(root, "skills", handle, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return echo.NewHTTPError(http.StatusNotFound, "skill not found at selected scope")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	decl, body, parseErr := agentlibrary.ParseDeclaration(string(data))
	if parseErr != nil {
		return echo.NewHTTPError(http.StatusBadRequest, parseErr.Error())
	}
	if req.Enabled {
		// Enabling — normalize to nil (absence = enabled, keeps frontmatter clean)
		decl.Skill.Enabled = nil
	} else {
		disabled := false
		decl.Skill.Enabled = &disabled
	}
	decl.Agent.Key = ""
	decl.Skill.Key = handle
	decl.Skill.Scope = scope
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: h.currentProjectSkillRoot(c)}, nil)
	if _, writeErr := importer.WriteSkill(c.Request().Context(), decl, body); writeErr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, writeErr.Error())
	}
	h.recordManualSkillEvent(c, models.SkillEventEdited, handle, scope, "")
	return h.ListSkills(c)
}

func removeStandaloneSkillIndexEntry(path, handle string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	body := string(data)
	sections := skillIndexSections(body)
	if len(sections) == 0 {
		return nil
	}
	updated := body
	for _, section := range sections {
		if skillIndexSectionHandle(section) != handle {
			continue
		}
		updated = removeSkillIndexSectionText(updated, section)
		break
	}
	if strings.TrimSpace(updated) == strings.TrimSpace(body) {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o644)
}

func skillIndexSections(body string) []string {
	lines := strings.Split(body, "\n")
	var sections []string
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "## ") {
			continue
		}
		start := i
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "## ") {
			i++
		}
		sections = append(sections, strings.Join(lines[start:i], "\n"))
		i--
	}
	return sections
}

func skillIndexSectionHandle(section string) string {
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "## "))
		}
	}
	return ""
}

func removeSkillIndexSectionText(body, section string) string {
	lines := strings.Split(body, "\n")
	sectionLines := strings.Split(section, "\n")
	removeStart := -1
	removeEnd := -1
	for i := 0; i <= len(lines)-len(sectionLines); i++ {
		if strings.Join(lines[i:i+len(sectionLines)], "\n") == section {
			removeStart = i
			removeEnd = i + len(sectionLines)
			break
		}
	}
	if removeStart < 0 {
		return body
	}
	for removeStart > 0 && strings.TrimSpace(lines[removeStart-1]) == "" {
		removeStart--
		break
	}
	for removeEnd < len(lines) && strings.TrimSpace(lines[removeEnd]) == "" {
		removeEnd++
		break
	}
	updated := append([]string{}, lines[:removeStart]...)
	updated = append(updated, lines[removeEnd:]...)
	return strings.TrimRight(strings.Join(updated, "\n"), "\n") + "\n"
}

type uploadedSkillPackage struct {
	SkillMD      string
	PackageName  string
	PackageFiles []uploadedSkillPackageFile
}

type uploadedSkillPackageFile struct {
	Path    string
	Content []byte
}

func readUploadedSkillPackage(files []*multipart.FileHeader, paths []string) (uploadedSkillPackage, error) {
	var pkg uploadedSkillPackage
	for i, header := range files {
		filename := header.Filename
		if i < len(paths) && strings.TrimSpace(paths[i]) != "" {
			filename = paths[i]
		}
		rel, err := normalizedUploadedSkillPackagePath(filename)
		if err != nil {
			return pkg, err
		}
		content, err := readMultipartFile(header)
		if err != nil {
			return pkg, err
		}
		if rel == "SKILL.md" {
			if pkg.SkillMD != "" {
				return pkg, fmt.Errorf("skill package must include exactly one SKILL.md")
			}
			pkg.SkillMD = string(content)
			pkg.PackageName = uploadedSkillPackageName(filename)
			continue
		}
		if rel == "SKILLS.md" {
			continue
		}
		pkg.PackageFiles = append(pkg.PackageFiles, uploadedSkillPackageFile{Path: rel, Content: content})
	}
	if strings.TrimSpace(pkg.SkillMD) == "" {
		return pkg, fmt.Errorf("skill package must include SKILL.md")
	}
	sort.Slice(pkg.PackageFiles, func(i, j int) bool {
		return pkg.PackageFiles[i].Path < pkg.PackageFiles[j].Path
	})
	return pkg, nil
}

func parseUploadedStandaloneSkillDeclaration(content, packageName, scope string) (*agentlibrary.SkillDeclaration, string, error) {
	return agentlibrary.NormalizeStandaloneSkillPackage(content, packageName, scope)
}

func uploadedSkillPackageName(filename string) string {
	filename = strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/")
	filename = strings.TrimPrefix(filename, "/")
	clean := filepath.ToSlash(filepath.Clean(filename))
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if part == "SKILL.md" && i > 0 {
			return strings.TrimSpace(parts[i-1])
		}
	}
	return ""
}

func readMultipartFile(header *multipart.FileHeader) ([]byte, error) {
	file, err := header.Open()
	if err != nil {
		return nil, fmt.Errorf("read uploaded file %q: %w", header.Filename, err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read uploaded file %q: %w", header.Filename, err)
	}
	return data, nil
}

func normalizedUploadedSkillPackagePath(filename string) (string, error) {
	filename = strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/")
	filename = strings.TrimPrefix(filename, "/")
	if filename == "" {
		return "", fmt.Errorf("uploaded file path is empty")
	}
	clean := filepath.ToSlash(filepath.Clean(filename))
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("uploaded file path %q is not allowed", filename)
	}
	parts := strings.Split(clean, "/")
	if idx := uploadedSkillPathIndex(parts); idx >= 0 {
		parts = parts[idx:]
	} else if len(parts) > 1 {
		parts = parts[1:]
	}
	clean = strings.Join(parts, "/")
	if !validUploadedSkillPackageFilePath(clean) {
		return "", fmt.Errorf("uploaded file path %q is not allowed", filename)
	}
	if clean != "SKILL.md" && strings.HasSuffix(clean, "/SKILL.md") {
		return "", fmt.Errorf("skill package must include exactly one root SKILL.md")
	}
	return clean, nil
}

func uploadedSkillPathIndex(parts []string) int {
	for i, part := range parts {
		if part == "SKILL.md" {
			return i
		}
	}
	return -1
}

func validUploadedSkillPackageFilePath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "" || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		if strings.HasPrefix(part, ".") && part != ".well-known" {
			return false
		}
	}
	return true
}

func writeUploadedSkillPackageFile(skillDir, rel string, content []byte) error {
	if !validUploadedSkillPackageFilePath(rel) || rel == "SKILL.md" || strings.HasSuffix(rel, "/SKILL.md") {
		return fmt.Errorf("uploaded file path %q is not allowed", rel)
	}
	abs := filepath.Join(skillDir, filepath.FromSlash(rel))
	skillDirAbs, err := filepath.Abs(skillDir)
	if err != nil {
		return err
	}
	absResolved, err := filepath.Abs(abs)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(absResolved, skillDirAbs+string(filepath.Separator)) {
		return fmt.Errorf("uploaded file path %q escapes skill folder", rel)
	}
	if err := os.MkdirAll(filepath.Dir(absResolved), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(absResolved), err)
	}
	perm := os.FileMode(0o644)
	if strings.HasPrefix(filepath.ToSlash(rel), "scripts/") {
		perm = 0o755
	}
	if err := os.WriteFile(absResolved, content, perm); err != nil {
		return fmt.Errorf("write %s: %w", absResolved, err)
	}
	if perm == 0o755 {
		if err := os.Chmod(absResolved, perm); err != nil {
			return fmt.Errorf("chmod %s: %w", absResolved, err)
		}
	}
	return nil
}

func listStandaloneSkillPackageFiles(skillPath string) []string {
	skillDir := filepath.Dir(skillPath)
	var files []string
	_ = filepath.WalkDir(skillDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			if entry != nil && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") && path != skillDir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(skillDir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "SKILL.md" || !validUploadedSkillPackageFilePath(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	sort.Strings(files)
	return files
}

func (h *Handler) listStandaloneSkills(c echo.Context) ([]pages.SkillCard, error) {
	projectRoot := h.currentProjectSkillRoot(c)
	catalog, err := agentskills.BuildCatalogAll("skills-page", h.agentSkillRoot, projectRoot)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Read always_use lists from both roots to populate AlwaysUse on each card.
	globalAlwaysUse := alwaysUseSet(h.agentSkillRoot)
	projectAlwaysUse := alwaysUseSet(projectRoot)

	out := make([]pages.SkillCard, 0, len(catalog.Entries()))
	for _, entry := range catalog.Entries() {
		scope := h.scopeForStandaloneSkillPath(c, entry.AbsolutePath)
		card := pages.SkillCard{
			Handle: entry.Handle,
			Name:   entry.Skill,
			Scope:  scope,
			Source: string(entry.Source),
			Files:  listStandaloneSkillPackageFiles(entry.AbsolutePath),
		}
		// Populate AlwaysUse from the appropriate root's index.
		switch scope {
		case "global":
			card.AlwaysUse = globalAlwaysUse[entry.Handle]
		default: // project
			card.AlwaysUse = projectAlwaysUse[entry.Handle]
		}
		card.Enabled = true // default to enabled when frontmatter is absent
		if data, readErr := os.ReadFile(entry.AbsolutePath); readErr == nil {
			card.Content = string(data)
			if decl, body, parseErr := agentlibrary.ParseDeclaration(string(data)); parseErr == nil && decl != nil {
				card.Name = firstDialogNonEmpty(decl.Skill.Name, decl.Skill.Key, entry.Skill)
				card.Description = firstNonEmpty(decl.Skill.Description, decl.Routing.Description)
				card.Archived = decl.Skill.Archived
				card.Enabled = decl.Skill.Enabled == nil || *decl.Skill.Enabled
				card.Content, _ = agentlibrary.RenderSkillMarkdown(decl, body)
			}
		}
		out = append(out, card)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope == "project"
		}
		return out[i].Handle < out[j].Handle
	})
	return out, nil
}

// alwaysUseSet reads the SKILLS.md always_use list for root and returns a set
// map for fast membership tests. Returns an empty map for empty/missing roots.
func alwaysUseSet(root string) map[string]bool {
	if root == "" {
		return map[string]bool{}
	}
	meta := agentskills.ReadSkillsIndexMeta(agentskills.SkillsIndexPath(root))
	out := make(map[string]bool, len(meta.AlwaysUse))
	for _, h := range meta.AlwaysUse {
		out[h] = true
	}
	return out
}

func (h *Handler) writeStandaloneSkillFromDialog(c echo.Context, req skillSaveRequest, requireExisting bool) (*agentlibrary.ImportResult, error) {
	handle := strings.TrimSpace(req.Handle)
	if !validDialogSkillKey(handle) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "skill handle must be a slug")
	}
	scope := h.dialogStandaloneSkillScope(c, req.Scope)
	root, err := h.rootForDialogScope(c, scope)
	if err != nil {
		return nil, err
	}
	if requireExisting {
		if _, statErr := os.Stat(filepath.Join(root, "skills", handle, "SKILL.md")); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, echo.NewHTTPError(http.StatusNotFound, "skill not found at selected scope")
			}
			return nil, echo.NewHTTPError(http.StatusInternalServerError, statErr.Error())
		}
	}
	decl, body, err := normalizeSkillDialogDeclaration(skillDialogNormalizationRequest{
		Handle:               handle,
		Scope:                scope,
		Name:                 req.Name,
		Description:          req.Description,
		Body:                 req.Body,
		Enabled:              req.Enabled,
		RejectAgentOwnership: true,
	})
	if err != nil {
		return nil, err
	}
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: h.agentSkillRoot, Project: h.currentProjectSkillRoot(c)}, nil)
	res, err := importer.WriteSkill(c.Request().Context(), decl, body)
	if err != nil {
		return res, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return res, nil
}

func (h *Handler) dialogStandaloneSkillScope(c echo.Context, requested string) string {
	scope := strings.ToLower(strings.TrimSpace(requested))
	if scope == "global" || scope == "project" {
		return scope
	}
	if h.currentProjectSkillRoot(c) != "" {
		return "project"
	}
	return "global"
}

func (h *Handler) scopeForStandaloneSkillPath(c echo.Context, path string) string {
	clean := filepath.Clean(path)
	if projectRoot := h.currentProjectSkillRoot(c); projectRoot != "" {
		if rel, err := filepath.Rel(filepath.Clean(projectRoot), clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "project"
		}
	}
	if h.agentSkillRoot != "" {
		if rel, err := filepath.Rel(filepath.Clean(h.agentSkillRoot), clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "global"
		}
	}
	return "project"
}
