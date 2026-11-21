package handler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const thisComputer = "This computer"

// groupSharedProjects groups every project with a repository by repo key, so
// one repository has one tab however many copies are reachable.
func groupSharedProjects(local []models.ProjectAPIItem, servers []layout.RemoteServer) []layout.SharedProject {
	byKey := map[string]*layout.SharedProject{}
	var order []string
	add := func(key string, m layout.SharedMember) {
		if key == "" {
			return
		}
		p := byKey[key]
		if p == nil {
			p = &layout.SharedProject{ID: models.SharedProjectID(key), Name: m.Name}
			byKey[key] = p
			order = append(order, key)
		}
		p.Members = append(p.Members, m)
	}
	for _, p := range local {
		add(p.RepoKey, layout.SharedMember{ID: p.ID, Name: p.Name, Machine: thisComputer, Path: p.RepoPath})
	}
	for _, server := range servers {
		for _, p := range server.Projects {
			add(p.RepoKey, layout.SharedMember{ID: p.ID, Name: p.Name, Machine: server.Name, Path: p.RepoPath})
		}
	}
	out := make([]layout.SharedProject, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// repoGroups groups the caller's reachable projects by repository.
func (h *Handler) repoGroups(ctx context.Context, servers []layout.RemoteServer) []layout.SharedProject {
	return memoProjectLookup(ctx, func(c *projectLookupCache) map[string][]layout.SharedProject { return c.groups }, func() []layout.SharedProject {
		local := h.repositoryProjects(ctx)
		return groupSharedProjects(local, servers)
	})
}

// sharedProjects lists repositories with several reachable copies.
func (h *Handler) sharedProjects(ctx context.Context, servers []layout.RemoteServer) []layout.SharedProject {
	var out []layout.SharedProject
	for _, p := range h.repoGroups(ctx, servers) {
		if p.Combined() {
			out = append(out, p)
		}
	}
	return out
}

// sharedProject resolves a shared project ID to its reachable copies, or nil
// when no copy is reachable.
func (h *Handler) sharedProject(ctx context.Context, id string) *layout.SharedProject {
	if !models.IsSharedProjectID(id) {
		return nil
	}
	for _, p := range h.repoGroups(ctx, h.remoteServerGroups(ctx)) {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

func sharedMemberIDs(p *layout.SharedProject) []string {
	ids := make([]string, 0, len(p.Members))
	for _, m := range p.Members {
		ids = append(ids, m.ID)
	}
	return ids
}

func sharedHasMember(p *layout.SharedProject, id string) bool {
	for _, m := range p.Members {
		if m.ID == id {
			return true
		}
	}
	return false
}

// sharedMemberOptions names each copy with its machine, and its folder when
// one machine has several, so lists can tell them apart.
func sharedMemberOptions(p layout.SharedProject) []models.Project {
	perMachine := map[string]int{}
	for _, m := range p.Members {
		perMachine[m.Machine]++
	}
	home, _ := os.UserHomeDir()
	out := make([]models.Project, 0, len(p.Members))
	for _, m := range p.Members {
		name := m.Name + " · " + m.Machine
		if perMachine[m.Machine] > 1 && m.Path != "" {
			path := m.Path
			if m.Machine == thisComputer && home != "" {
				if rest, ok := strings.CutPrefix(path, home); ok && (rest == "" || strings.HasPrefix(rest, string(filepath.Separator))) {
					path = "~" + rest
				}
			}
			name += " (" + path + ")"
		}
		out = append(out, models.Project{ID: m.ID, Name: name})
	}
	return out
}

// virtualProject names an All projects or shared workspace, or nil for a real project.
func (h *Handler) virtualProject(ctx context.Context, id string) *models.Project {
	if id == models.AllProjectsID {
		return &models.Project{ID: id, Name: "All projects"}
	}
	if p := h.sharedProject(ctx, id); p != nil {
		return &models.Project{ID: p.ID, Name: p.Name}
	}
	return nil
}

// workspaceProjectOptions lists the projects an aggregate workspace spans.
func (h *Handler) workspaceProjectOptions(ctx context.Context, workspace string) ([]models.Project, error) {
	if workspace == models.AllProjectsID {
		return h.projectSvc.ListSelectorOptions(ctx)
	}
	p := h.sharedProject(ctx, workspace)
	if p == nil {
		return nil, nil
	}
	return sharedMemberOptions(*p), nil
}

// scopedProjects keeps the projects inside the context's shared project, if any.
func scopedProjects(ctx context.Context, projects []models.Project) []models.Project {
	if _, ok := models.ScopeProjects(ctx); !ok {
		return projects
	}
	out := []models.Project{}
	for _, p := range projects {
		if models.InScope(ctx, p.ID) {
			out = append(out, p)
		}
	}
	return out
}

// sharedProjectChat lets the user pick which copy of a shared project to chat in.
func (h *Handler) sharedProjectChat(c echo.Context, shared *layout.SharedProject) error {
	agents, err := h.llmConfigRepo.ListPickerOptions(c.Request().Context())
	if err != nil {
		return err
	}
	choices := sharedMemberOptions(*shared)
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.SharedChatContent(agents, shared.ID, choices))
	}
	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())
	return render(c, http.StatusOK, pages.SharedChat(projects, shared.ID, agents, choices))
}
