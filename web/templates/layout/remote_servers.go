package layout

import (
	"context"
	"strings"
)

// RemoteProject is a project owned by a connected remote server.
type RemoteProject struct {
	ServerID    string
	ServerName  string
	ID          string
	Name        string
	RepoKey     string
	RepoPending bool
	RepoPath    string
}

// RemoteServer is a connected server shown in combined navigation.
type RemoteServer struct {
	ID       string
	Name     string
	Projects []RemoteProject
}

type remoteServersContextKey struct{}

func WithRemoteServers(ctx context.Context, servers []RemoteServer) context.Context {
	return context.WithValue(ctx, remoteServersContextKey{}, servers)
}

func RemoteServers(ctx context.Context) []RemoteServer {
	servers, _ := ctx.Value(remoteServersContextKey{}).([]RemoteServer)
	return servers
}

// SharedMember is one copy of a repository.
type SharedMember struct {
	ID      string
	Name    string
	Machine string
	Path    string
}

// SharedProject is one repository and its reachable copies. Its ID names the
// project tab however many copies are reachable.
type SharedProject struct {
	ID      string
	Name    string
	Members []SharedMember
}

// Combined reports whether pages show several copies together.
func (p SharedProject) Combined() bool { return len(p.Members) > 1 }

// PageID is the project_id its pages use: the copy itself when it is the only one.
func (p SharedProject) PageID() string {
	if len(p.Members) == 1 {
		return p.Members[0].ID
	}
	return p.ID
}

func combinedProjects(ctx context.Context) []SharedProject {
	var out []SharedProject
	for _, p := range SharedProjects(ctx) {
		if p.Combined() {
			out = append(out, p)
		}
	}
	return out
}

// Machines lists where the shared project's copies live.
func (p SharedProject) Machines() string {
	var names []string
	seen := map[string]bool{}
	for _, m := range p.Members {
		if !seen[m.Machine] {
			seen[m.Machine] = true
			names = append(names, m.Machine)
		}
	}
	return strings.Join(names, ", ")
}

// TabID is the project tab showing id: its repository's tab, else id itself.
func TabID(ctx context.Context, id string) string {
	for _, p := range SharedProjects(ctx) {
		if p.ID == id {
			return id
		}
		for _, m := range p.Members {
			if m.ID == id {
				return p.ID
			}
		}
	}
	return id
}

type sharedProjectsContextKey struct{}

func WithSharedProjects(ctx context.Context, projects []SharedProject) context.Context {
	return context.WithValue(ctx, sharedProjectsContextKey{}, projects)
}

func SharedProjects(ctx context.Context) []SharedProject {
	projects, _ := ctx.Value(sharedProjectsContextKey{}).([]SharedProject)
	return projects
}

// sharedMember reports whether id is a copy listed under a combined project.
func sharedMember(ctx context.Context, id string) bool {
	for _, p := range combinedProjects(ctx) {
		for _, m := range p.Members {
			if m.ID == id {
				return true
			}
		}
	}
	return false
}

// sharedProjectMembers maps each shared project to its copies for page scripts.
func sharedProjectMembers(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	for _, p := range SharedProjects(ctx) {
		for _, m := range p.Members {
			out[p.ID] = append(out[p.ID], m.ID)
		}
	}
	return out
}

// sharedProjectMachines names the machine of each copy under a combined project.
func sharedProjectMachines(ctx context.Context) map[string][]SharedMember {
	out := map[string][]SharedMember{}
	for _, p := range combinedProjects(ctx) {
		out[p.ID] = p.Members
	}
	return out
}

func remoteUngrouped(ctx context.Context, projects []RemoteProject) []RemoteProject {
	var out []RemoteProject
	for _, p := range projects {
		if !sharedMember(ctx, p.ID) {
			out = append(out, p)
		}
	}
	return out
}
