package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
)

func TestSkillsPageListsGlobalAndProjectStandaloneSkillCards(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	globalRoot := t.TempDir()
	projectRepoPath := t.TempDir()
	h.SetAgentSkillRoot(globalRoot)
	project := createProject(t, h, "Skills Project")
	project.RepoPath = projectRepoPath
	if err := h.projectRepo.Update(context.Background(), project); err != nil {
		t.Fatalf("update project repo path: %v", err)
	}

	writeStandaloneSkill(t, globalRoot, "global_review", "Global Review", "global description", "global")
	writeStandaloneSkill(t, filepath.Join(projectRepoPath, ".openvibely"), "project_review", "Project Review", "project description", "project")

	req := httptest.NewRequest(http.MethodGet, "/skills?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Skills", "Search skills...", "global_review", "project_review", `data-search-card`, `data-skill-scope="global"`, `data-skill-scope="project"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q", want)
		}
	}
	for _, unwanted := range []string{"Standalone project and global skills available for skill routing.", "project skill · project_review", "global skill · global_review"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("expected body not to contain %q", unwanted)
		}
	}
}

func TestSkillsPageDeleteConfirmationDialog(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "debug_tests", "Debug Tests", "debug description", "global")

	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="delete_skill_confirm_modal" class="modal"`,
		`id="delete_skill_confirm_name"`,
		`data-destructive-confirm-dialog`,
		`window.openDestructiveConfirmDialog = function(dialogID, nameID, displayName)`,
		`openDestructiveConfirmDialog('delete_skill_confirm_modal', 'delete_skill_confirm_name', button.dataset.skillName || deleteSkillHandle)`,
		`onclick="delete_skill_confirm_modal.close()"`,
		`onclick="confirmDeleteSkill()"`,
		`class="btn btn-error"`,
		`onclick="deleteSkill(this)"`,
		`modal.showModal()`,
		`htmx.ajax('DELETE', '/skills/' + encodeURIComponent(deleteSkillHandle)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected skills delete confirmation markup/script to contain %q", want)
		}
	}
	if strings.Contains(body, `confirm('Delete skill`) {
		t.Fatal("expected skill delete flow to avoid browser confirm()")
	}
}

func TestSkillsPageHeaderUsesAddSkillDropdownMenu(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="btn btn-primary btn-sm"`,
		`+ Add Skill`,
		`Create Skill`,
		`openNewSkillModal()`,
		`Import Skill Package`,
		`openImportSkillModal()`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q", want)
		}
	}
	if strings.Contains(body, `data-skill-header-kebab`) {
		t.Fatalf("expected skills header not to use a separate kebab button")
	}
}

func TestSkillsPageCardsIncludeKebabEditAndDeleteActions(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "global_review", "Global Review", "global description", "global")

	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="dropdown dropdown-end"`,
		`editSkillFromData(this.closest('[data-skill-handle]'))`,
		`deleteSkill(this)`,
		`data-skill-scope="global"`,
		`Delete`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q", want)
		}
	}
}

func TestSkillsPageNewSkillModalIncludesFrontmatterTemplate(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`kind: openvibely.agent_skill`,
		`version: 1`,
		`skill:`,
		`key: openvibely_database_migration_workflow`,
		`scope: project`,
		`description: Manage OpenVibely goose schema migrations`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q", want)
		}
	}
}

func TestSkillsModalDisablesScopeWhenEditing(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`document.getElementById('skill_scope').disabled = false`,
		`document.getElementById('skill_scope').disabled = true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q", want)
		}
	}
}

func TestSkillsPageEditModalLazyLoadsPackageFileList(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkillWithBody(t, root, "global_review", "Global Review", "global description", "global", "Use this skill when details are opened.")
	refPath := filepath.Join(root, "skills", "global_review", "references", "notes.md")
	if err := os.MkdirAll(filepath.Dir(refPath), 0o755); err != nil {
		t.Fatalf("mkdir support dir: %v", err)
	}
	if err := os.WriteFile(refPath, []byte("notes"), 0o644); err != nil {
		t.Fatalf("write support file: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/skills", nil)
	listRec := httptest.NewRecorder()
	e.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	listBody := listRec.Body.String()
	for _, unwanted := range []string{`data-skill-content=`, `data-skill-files=`, `references/notes.md`, `Use this skill when details are opened.`} {
		if strings.Contains(listBody, unwanted) {
			t.Fatalf("expected list body not to contain lazy detail value %q", unwanted)
		}
	}
	for _, want := range []string{`id="skill_files"`, `Package files`, `/details?scope=`} {
		if !strings.Contains(listBody, want) {
			t.Fatalf("expected list body to contain %q", want)
		}
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/skills/global_review/details?scope=global", nil)
	detailRec := httptest.NewRecorder()
	e.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("expected detail 200, got %d: %s", detailRec.Code, detailRec.Body.String())
	}
	var detail skillDetailResponse
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Handle != "global_review" || detail.Scope != "global" || detail.Name != "Global Review" || !detail.Enabled {
		t.Fatalf("unexpected detail metadata: %+v", detail)
	}
	if !strings.Contains(detail.Content, "Use this skill when details are opened.") {
		t.Fatalf("expected detail content to include skill body, got %q", detail.Content)
	}
	if len(detail.Files) != 1 || detail.Files[0] != "references/notes.md" {
		t.Fatalf("expected detail support files, got %v", detail.Files)
	}
}

func TestDeleteSkillRemovesStandaloneSkillAndReturnsCards(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "debug_tests", "Debug Tests", "Find and fix tests", "global")

	req := httptest.NewRequest(http.MethodDelete, "/skills/debug_tests?scope=global", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "debug_tests")); !os.IsNotExist(err) {
		t.Fatalf("expected skill directory to be removed, stat err=%v", err)
	}
	if strings.Contains(rec.Body.String(), "Debug Tests") {
		t.Fatalf("expected response to omit deleted skill card")
	}
	index, err := os.ReadFile(filepath.Join(root, "skills", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read skill index: %v", err)
	}
	if strings.Contains(string(index), "debug_tests") {
		t.Fatalf("expected skill index to omit deleted skill, got:\n%s", index)
	}
}

func TestImportSkillPackageWritesSkillAndSupportFiles(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("scope", "global"); err != nil {
		t.Fatalf("write scope: %v", err)
	}
	addMultipartFile(t, writer, "files", "SKILL.md", `---
kind: openvibely.agent_skill
version: 1
skill:
    key: imported_skill
    name: Imported Skill
    scope: project
    description: Imported from disk.
---
Use this imported skill.
`)
	if err := writer.WriteField("paths", "imported_skill/SKILL.md"); err != nil {
		t.Fatalf("write skill path: %v", err)
	}
	addMultipartFile(t, writer, "files", "guide.md", "# Guide\n")
	if err := writer.WriteField("paths", "imported_skill/references/guide.md"); err != nil {
		t.Fatalf("write support path: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/skills/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	skillPath := filepath.Join(root, "skills", "imported_skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read imported skill: %v", err)
	}
	content := string(data)
	for _, want := range []string{"key: imported_skill", "scope: global", "Use this imported skill."} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected imported skill to contain %q; got:\n%s", want, content)
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, "skills", "imported_skill", "references", "guide.md")); err != nil || string(got) != "# Guide\n" {
		t.Fatalf("expected support file to be imported, got %q err=%v", got, err)
	}
	index, err := os.ReadFile(filepath.Join(root, "skills", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read skills index: %v", err)
	}
	if !strings.Contains(string(index), "imported_skill") {
		t.Fatalf("expected index to include imported skill, got:\n%s", index)
	}
	if !strings.Contains(rec.Body.String(), "Imported Skill") {
		t.Fatalf("expected response to include imported skill card")
	}
}

func TestImportSkillPackageAcceptsStandardSkillFormat(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("scope", "global"); err != nil {
		t.Fatalf("write scope: %v", err)
	}
	addMultipartFile(t, writer, "files", "SKILL.md", `---
name: skill-creator
description: Create new skills, modify and improve existing skills, and measure skill performance.
---

# Skill Creator

A skill for creating new skills and iteratively improving them.
`)
	if err := writer.WriteField("paths", "skill-creator/SKILL.md"); err != nil {
		t.Fatalf("write skill path: %v", err)
	}
	addMultipartFile(t, writer, "files", "guide.md", "# Guide\n")
	if err := writer.WriteField("paths", "skill-creator/references/guide.md"); err != nil {
		t.Fatalf("write support path: %v", err)
	}
	addMultipartFile(t, writer, "files", "generate_review.py", "print('review')\n")
	if err := writer.WriteField("paths", "skill-creator/eval-viewer/generate_review.py"); err != nil {
		t.Fatalf("write eval viewer path: %v", err)
	}
	addMultipartFile(t, writer, "files", "LICENSE.txt", "MIT\n")
	if err := writer.WriteField("paths", "skill-creator/LICENSE.txt"); err != nil {
		t.Fatalf("write license path: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/skills/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	path := filepath.Join(root, "skills", "skill-creator", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read imported skill: %v", err)
	}
	content := string(data)
	for _, want := range []string{"kind: openvibely.agent_skill", "key: skill-creator", "name: skill-creator", "scope: global", "# Skill Creator"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected imported standard skill to contain %q; got:\n%s", want, content)
		}
	}
	for path, want := range map[string]string{
		"references/guide.md":            "# Guide\n",
		"eval-viewer/generate_review.py": "print('review')\n",
		"LICENSE.txt":                    "MIT\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, "skills", "skill-creator", filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Fatalf("expected package file %s to be imported, got %q err=%v", path, got, err)
		}
	}
	if !strings.Contains(rec.Body.String(), "skill-creator") {
		t.Fatalf("expected response to include imported standard skill card")
	}
}

func TestSkillDetailLoadsProjectOverrideForSelectedScope(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	globalRoot := t.TempDir()
	projectRepoPath := t.TempDir()
	h.SetAgentSkillRoot(globalRoot)
	project := createProject(t, h, "Skills Override Project")
	project.RepoPath = projectRepoPath
	if err := h.projectRepo.Update(context.Background(), project); err != nil {
		t.Fatalf("update project repo path: %v", err)
	}
	projectRoot := filepath.Join(projectRepoPath, ".openvibely")
	writeStandaloneSkillWithBody(t, globalRoot, "shared_skill", "Global Shared", "global description", "global", "global body")
	writeStandaloneSkillWithBody(t, projectRoot, "shared_skill", "Project Shared", "project description", "project", "project override body")

	req := httptest.NewRequest(http.MethodGet, "/skills/shared_skill/details?scope=project&project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var detail skillDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Scope != "project" || detail.Name != "Project Shared" || detail.Description != "project description" {
		t.Fatalf("expected project override metadata, got %+v", detail)
	}
	if !strings.Contains(detail.Content, "project override body") || strings.Contains(detail.Content, "global body") {
		t.Fatalf("expected project override body only, got %q", detail.Content)
	}
}

func TestSkillsListLargeCatalogOmitsBodies(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	const skillCount = 200
	largeBody := strings.Repeat("large-body-marker ", 32*1024/len("large-body-marker ")+1)
	for i := 0; i < skillCount; i++ {
		handle := fmt.Sprintf("large_skill_%03d", i)
		writeStandaloneSkillWithBody(t, root, handle, "Large Skill "+handle, "large description", "global", largeBody)
		for _, rel := range []string{"references/notes.md", "templates/example.md", "scripts/run.sh"} {
			path := filepath.Join(root, "skills", handle, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir support file: %v", err)
			}
			if err := os.WriteFile(path, []byte("support"), 0o644); err != nil {
				t.Fatalf("write support file: %v", err)
			}
		}
	}

	currentHTML := serveSkillsHTMX(t, e)
	if strings.Contains(currentHTML, "large-body-marker") || strings.Contains(currentHTML, "references/notes.md") || strings.Contains(currentHTML, `data-skill-content=`) || strings.Contains(currentHTML, `data-skill-files=`) {
		t.Fatalf("compact list response contains lazy detail data")
	}
	if len(currentHTML) >= 1<<20 {
		t.Fatalf("compact list response is unexpectedly large: %d bytes", len(currentHTML))
	}
}

func TestCreateSkillWritesStandaloneSkillAndReturnsCards(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	payload := skillSaveRequest{
		Handle:      "debug_tests",
		Name:        "Debug Tests",
		Description: "Find and fix test failures",
		Scope:       "global",
		Body:        "Run the focused failure first, then the package test.",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/skills?project_id=default", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	path := filepath.Join(root, "skills", "debug_tests", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	content := string(data)
	for _, want := range []string{"key: debug_tests", "name: Debug Tests", "scope: global", "Run the focused failure"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected skill file to contain %q; got %s", want, content)
		}
	}
	if !strings.Contains(rec.Body.String(), "debug_tests") {
		t.Fatalf("expected response to include new skill card")
	}
}

func TestCreateSkillResponseIncludesFreshSearchText(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	payload := skillSaveRequest{
		Handle:      "search_target",
		Name:        "Search Test Skill",
		Description: "appears for active test filter",
		Scope:       "global",
		Body:        "Run checks.",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/skills", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	response := rec.Body.String()
	for _, want := range []string{
		`id="skills-container"`,
		`data-card-search="skills"`,
		`data-skill-handle="search_target"`,
		`data-search-text="search_target Search Test Skill appears for active test filter global`,
	} {
		if !strings.Contains(response, want) {
			t.Fatalf("expected create response to contain %q; got %s", want, response)
		}
	}
}

func TestUpdateSkillResponseSearchTextReflectsClearedFields(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "no_longer_matching", "Test Skill", "test description", "global")

	payload := skillSaveRequest{
		Handle:      "no_longer_matching",
		Name:        "",
		Description: "",
		Scope:       "global",
		Body:        "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: no_longer_matching\n    name: Test Skill\n    scope: global\n    description: test description\n---\n\nNo matching keyword remains.",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/skills/no_longer_matching", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	response := rec.Body.String()
	if !strings.Contains(response, `data-skill-handle="no_longer_matching"`) {
		t.Fatalf("expected updated skill card in response; got %s", response)
	}
	if strings.Contains(response, `data-search-text="no_longer_matching Test Skill test description`) {
		t.Fatalf("expected search text to drop cleared name/description; got %s", response)
	}
	data, err := os.ReadFile(filepath.Join(root, "skills", "no_longer_matching", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "Test Skill") || strings.Contains(content, "test description") {
		t.Fatalf("expected cleared fields not to remain in frontmatter; got\n%s", content)
	}
}

func TestCreateSkillDoesNotWriteEnabledTrueToFrontmatter(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	// Simulate modal submit with enabled=true (the default toggle state)
	trueVal := true
	payload := skillSaveRequest{
		Handle:      "clean_skill",
		Name:        "Clean Skill",
		Description: "A skill created via modal",
		Scope:       "global",
		Body:        "Do the thing.",
		Enabled:     &trueVal,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/skills", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "skills", "clean_skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if strings.Contains(string(data), "enabled:") {
		t.Fatalf("enabled skill must not have 'enabled:' in frontmatter; got:\n%s", data)
	}
}

func TestEditSkillViaModalWithEnabledFalseWritesEnabledFalse(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	writeStandaloneSkill(t, root, "my_skill", "My Skill", "desc", "global")

	falseVal := false
	payload := skillSaveRequest{
		Handle:      "my_skill",
		Name:        "My Skill",
		Description: "desc",
		Scope:       "global",
		Body:        "Do the thing.",
		Enabled:     &falseVal,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/skills/my_skill", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "skills", "my_skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if !strings.Contains(string(data), "enabled: false") {
		t.Fatalf("expected 'enabled: false' in frontmatter after disabling via modal; got:\n%s", data)
	}
}

func TestEditSkillViaModalWithEnabledTrueDoesNotWriteEnabledTrue(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	// Start with a disabled skill
	writeDisabledSkill(t, root, "was_disabled", "Was Disabled", "desc", "global")

	// Re-enable via modal (toggle=true)
	trueVal := true
	payload := skillSaveRequest{
		Handle:      "was_disabled",
		Name:        "Was Disabled",
		Description: "desc",
		Scope:       "global",
		Body:        "Do the thing.",
		Enabled:     &trueVal,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/skills/was_disabled", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "skills", "was_disabled", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if strings.Contains(string(data), "enabled:") {
		t.Fatalf("re-enabled skill must not have 'enabled:' in frontmatter (absence = enabled); got:\n%s", data)
	}
}

func TestSetSkillEnabledEnableNormalizesToAbsent(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	writeDisabledSkill(t, root, "was_off", "Was Off", "desc", "global")

	req := httptest.NewRequest(http.MethodPost, "/skills/was_off/enabled", strings.NewReader(`{"enabled":true,"scope":"global"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "skills", "was_off", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if strings.Contains(string(data), "enabled:") {
		t.Fatalf("re-enabled skill must not have 'enabled:' in frontmatter (absence = enabled); got:\n%s", data)
	}
}

func TestSetSkillEnabledDisablesAndEnablesSkill(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "debug_tests", "Debug Tests", "Find and fix tests", "global")

	// Disable the skill
	disableBody := `{"enabled":false,"scope":"global"}`
	req := httptest.NewRequest(http.MethodPost, "/skills/debug_tests/enabled", strings.NewReader(disableBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the SKILL.md now has enabled: false
	data, err := os.ReadFile(filepath.Join(root, "skills", "debug_tests", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill after disable: %v", err)
	}
	if !strings.Contains(string(data), "enabled: false") {
		t.Fatalf("expected SKILL.md to contain 'enabled: false', got:\n%s", data)
	}

	// Verify the response shows the Disabled badge
	body := rec.Body.String()
	if !strings.Contains(body, "Disabled") {
		t.Fatalf("expected response to show Disabled badge for disabled skill")
	}
	if !strings.Contains(body, "debug_tests") {
		t.Fatalf("expected disabled skill to remain visible in management page")
	}

	// Re-enable the skill
	enableBody := `{"enabled":true,"scope":"global"}`
	req2 := httptest.NewRequest(http.MethodPost, "/skills/debug_tests/enabled", strings.NewReader(enableBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("enable: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	data2, err := os.ReadFile(filepath.Join(root, "skills", "debug_tests", "SKILL.md"))
	if err != nil {
		t.Fatalf("read skill after enable: %v", err)
	}
	if strings.Contains(string(data2), "enabled: false") {
		t.Fatalf("expected SKILL.md to not contain 'enabled: false' after re-enable, got:\n%s", data2)
	}
	body2 := rec2.Body.String()
	if strings.Contains(body2, `<span class="badge badge-warning badge-sm">Disabled</span>`) {
		t.Fatalf("expected Disabled badge to be gone after re-enable")
	}
}

func TestSetSkillEnabledRejectsInvalidHandle(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/skills/../etc/enabled", strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// The router won't match the path with traversal, so we just verify a
	// well-formed bad handle returns 400 or 404
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for invalid handle traversal, got %d", rec.Code)
	}
}

func TestSetSkillEnabledReturns404ForMissingSkill(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/skills/nonexistent/enabled", strings.NewReader(`{"enabled":false,"scope":"global"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing skill, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSkillsPageShowsDisabledBadgeAndKebabToggle(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	// Write an enabled skill
	writeStandaloneSkill(t, root, "global_enabled", "Enabled Skill", "enabled", "global")
	// Write a disabled skill directly
	writeDisabledStandaloneSkill(t, root, "global_disabled", "Disabled Skill", "disabled", "global")

	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Both skills should appear (disabled skills are visible on the management page)
	if !strings.Contains(body, "global_enabled") {
		t.Fatalf("expected enabled skill to appear on skills page")
	}
	if !strings.Contains(body, "global_disabled") {
		t.Fatalf("expected disabled skill to remain visible on skills page")
	}

	// Disabled badge should appear for the disabled skill
	if !strings.Contains(body, "Disabled") {
		t.Fatalf("expected Disabled badge in skill cards")
	}
	if !strings.Contains(body, `data-skill-enabled="false"`) {
		t.Fatalf("expected data-skill-enabled=false attribute on disabled card")
	}
	if !strings.Contains(body, `data-skill-enabled="true"`) {
		t.Fatalf("expected data-skill-enabled=true attribute on enabled card")
	}

	// Enable/Disable buttons should appear in kebab menus
	if !strings.Contains(body, "setSkillEnabled") {
		t.Fatalf("expected setSkillEnabled JS call in skill card kebab menu")
	}
}

func TestSkillsPageDisabledSkillExcludedFromRuntimeCatalog(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "enabled_skill", "Enabled", "desc", "global")
	writeDisabledStandaloneSkill(t, root, "disabled_skill", "Disabled", "desc", "global")

	// The skills page (management) should show both
	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "enabled_skill") || !strings.Contains(body, "disabled_skill") {
		t.Fatalf("management page should show both skills; body excerpt: %s", body[:min(500, len(body))])
	}

	// Runtime catalog via BuildCatalog should exclude disabled skills
	catalog, err := agentskills.BuildCatalog("test-turn", root, "")
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	if _, ok := catalog.Lookup("enabled_skill"); !ok {
		t.Fatalf("enabled skill must be in runtime catalog")
	}
	if _, ok := catalog.Lookup("disabled_skill"); ok {
		t.Fatalf("disabled skill must NOT be in runtime catalog")
	}
}

func writeDisabledStandaloneSkill(t *testing.T, root, handle, name, description, scope string) {
	t.Helper()
	enabled := false
	decl := &agentlibrary.SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Skill: agentlibrary.SkillBlock{
			Key:         handle,
			Name:        name,
			Scope:       scope,
			Description: description,
			Enabled:     &enabled,
		},
	}
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: root, Project: root}, nil)
	if _, err := importer.WriteSkill(context.Background(), decl, "Use this skill when appropriate."); err != nil {
		t.Fatalf("write disabled skill %s: %v", handle, err)
	}
}

func addMultipartFile(t *testing.T, writer *multipart.Writer, fieldName, filename, content string) {
	t.Helper()
	part, err := writer.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create multipart file %s: %v", filename, err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write multipart file %s: %v", filename, err)
	}
}

func TestSetSkillAlwaysUse_SetsAlwaysUseFlagInIndex(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "guidance_skill", "Guidance", "project guidance", "global")

	req := httptest.NewRequest(http.MethodPost, "/skills/guidance_skill/always_use", strings.NewReader(`{"always_use":true,"scope":"global"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify SKILLS.md was updated with always_use list
	indexData, err := os.ReadFile(filepath.Join(root, "skills", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read SKILLS.md: %v", err)
	}
	meta := agentskills.ParseSkillsIndexMeta(string(indexData))
	if len(meta.AlwaysUse) != 1 || meta.AlwaysUse[0] != "guidance_skill" {
		t.Fatalf("expected [guidance_skill] in always_use, got %v", meta.AlwaysUse)
	}

	// Response should show "Always use" badge
	body := rec.Body.String()
	if !strings.Contains(body, "Always use") {
		t.Fatalf("expected response to show 'Always use' badge; got: %s", body)
	}
}

func TestSetSkillAlwaysUse_RemovesAlwaysUseFlag(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "guidance_skill", "Guidance", "project guidance", "global")

	// First set always_use
	req1 := httptest.NewRequest(http.MethodPost, "/skills/guidance_skill/always_use", strings.NewReader(`{"always_use":true,"scope":"global"}`))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("HX-Request", "true")
	rec1 := httptest.NewRecorder()
	e.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("set: expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Then remove always_use
	req2 := httptest.NewRequest(http.MethodPost, "/skills/guidance_skill/always_use", strings.NewReader(`{"always_use":false,"scope":"global"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("remove: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	indexData, err := os.ReadFile(filepath.Join(root, "skills", "SKILLS.md"))
	if err != nil {
		t.Fatalf("read SKILLS.md: %v", err)
	}
	meta := agentskills.ParseSkillsIndexMeta(string(indexData))
	if len(meta.AlwaysUse) != 0 {
		t.Fatalf("expected empty always_use after removal, got %v", meta.AlwaysUse)
	}
	// "Always use" badge should not appear after removal
	body := rec2.Body.String()
	if strings.Contains(body, `badge-primary`) && strings.Contains(body, `Always use`) {
		t.Fatalf("expected 'Always use' badge to be gone after removal")
	}
}

func TestSetSkillAlwaysUse_IdempotentSecondSet(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "guidance_skill", "Guidance", "project guidance", "global")

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/skills/guidance_skill/always_use", strings.NewReader(`{"always_use":true,"scope":"global"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	indexData, _ := os.ReadFile(filepath.Join(root, "skills", "SKILLS.md"))
	meta := agentskills.ParseSkillsIndexMeta(string(indexData))
	if len(meta.AlwaysUse) != 1 {
		t.Fatalf("expected exactly 1 always_use entry (idempotent), got %v", meta.AlwaysUse)
	}
}

func TestSetSkillAlwaysUse_Returns404ForMissingSkill(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/skills/nonexistent_skill/always_use", strings.NewReader(`{"always_use":true,"scope":"global"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing skill, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSetSkillAlwaysUse_RejectsInvalidHandle(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	// Use a path-traversal pattern; the router or handler must reject it.
	req := httptest.NewRequest(http.MethodPost, "/skills/../etc/always_use", strings.NewReader(`{"always_use":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for path-traversal handle, got 200")
	}
}

func TestSkillsPageShowsAlwaysUseBadgeForMarkedSkills(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	writeStandaloneSkill(t, root, "guidance_skill", "Guidance", "project guidance", "global")
	writeStandaloneSkill(t, root, "other_skill", "Other", "other desc", "global")

	// Mark guidance_skill as always_use via the SetSkillAlwaysUse mutation.
	indexPath := filepath.Join(root, "skills", "SKILLS.md")
	if err := agentlibrary.SetSkillAlwaysUse(indexPath, "guidance_skill", true); err != nil {
		t.Fatalf("set always use: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Always use") {
		t.Fatalf("expected 'Always use' badge for guidance_skill, got:\n%s", body)
	}
	if !strings.Contains(body, "setSkillAlwaysUse") {
		t.Fatalf("expected setSkillAlwaysUse JS function reference in response")
	}
}

func serveSkillsHTMX(t *testing.T, e http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/skills", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected skills HTMX 200, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func writeStandaloneSkill(t *testing.T, root, handle, name, description, scope string) {
	t.Helper()
	writeStandaloneSkillWithBody(t, root, handle, name, description, scope, "Use this skill when appropriate.")
}

func writeStandaloneSkillWithBody(t *testing.T, root, handle, name, description, scope, body string) {
	t.Helper()
	decl := &agentlibrary.SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Skill: agentlibrary.SkillBlock{
			Key:         handle,
			Name:        name,
			Scope:       scope,
			Description: description,
		},
	}
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: root, Project: root}, nil)
	if _, err := importer.WriteSkill(context.Background(), decl, body); err != nil {
		t.Fatalf("write skill %s: %v", handle, err)
	}
}

func writeDisabledSkill(t *testing.T, root, handle, name, description, scope string) {
	t.Helper()
	disabled := false
	decl := &agentlibrary.SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Skill: agentlibrary.SkillBlock{
			Key:         handle,
			Name:        name,
			Scope:       scope,
			Description: description,
			Enabled:     &disabled,
		},
	}
	importer := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: root, Project: root}, nil)
	if _, err := importer.WriteSkill(context.Background(), decl, "Use this skill when appropriate."); err != nil {
		t.Fatalf("write disabled skill %s: %v", handle, err)
	}
}
