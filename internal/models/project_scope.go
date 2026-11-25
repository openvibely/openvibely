package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// AllProjectsID identifies the aggregate UI and its reserved chat-history owner.
// It is never a destination for project work.
const AllProjectsID = "__all_projects__"

func AllProjectsOption() Project {
	return Project{ID: AllProjectsID, Name: "All projects"}
}

const sharedProjectPrefix = "__repo_"

// SharedProjectID is the virtual project of every copy of one repository.
func SharedProjectID(repoKey string) string {
	sum := sha256.Sum256([]byte(repoKey))
	return sharedProjectPrefix + hex.EncodeToString(sum[:8])
}

func IsSharedProjectID(id string) bool {
	rest, ok := strings.CutPrefix(id, sharedProjectPrefix)
	if !ok || len(rest) != 16 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}

// IsAggregateProject reports a virtual scope spanning several real projects.
func IsAggregateProject(id string) bool { return id == AllProjectsID || IsSharedProjectID(id) }

type scopeProjectsKey struct{}

// WithScopeProjects narrows aggregate reads to ids.
func WithScopeProjects(ctx context.Context, ids []string) context.Context {
	return context.WithValue(ctx, scopeProjectsKey{}, append([]string{}, ids...))
}

// ScopeProjects reports the projects an aggregate read is narrowed to.
func ScopeProjects(ctx context.Context) ([]string, bool) {
	ids, ok := ctx.Value(scopeProjectsKey{}).([]string)
	return ids, ok
}

// InScope reports whether projectID belongs to the context's narrowed scope.
func InScope(ctx context.Context, projectID string) bool {
	ids, ok := ScopeProjects(ctx)
	if !ok {
		return true
	}
	for _, id := range ids {
		if id == projectID {
			return true
		}
	}
	return false
}

// SharedMembers returns the projects in items that are copies of shared project id.
func SharedMembers(items []ProjectAPIItem, id string) []string {
	members := []string{}
	for _, p := range items {
		if p.RepoKey != "" && SharedProjectID(p.RepoKey) == id {
			members = append(members, p.ID)
		}
	}
	return members
}
