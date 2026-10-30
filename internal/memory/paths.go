// Package memory provides repo-local managed-memory path resolution and
// minimal filesystem setup for OpenVibely projects. Task-time memory selection,
// updates, and consolidation are owned by the Memory Curator agent.
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MemoryDirName is the canonical repo-local managed memory directory name.
const MemoryDirName = "memories"

// LegacyMemoryDirName is the previous repo-local managed memory directory name.
// MemoryService migrates this directory to MemoryDirName at runtime.
const LegacyMemoryDirName = "memory"

// IndexFileName is the per-project memory index file name.
const IndexFileName = "MEMORIES.md"

// LegacyIndexFileName is the previous per-project memory index file name.
// EnsureProject migrates this file to IndexFileName when the new index is absent.
const LegacyIndexFileName = "MEMORY.md"

// PathResolver resolves the absolute on-disk path for project memory storage.
// Each project must be registered to its repo-local memory directory:
// <repo>/.openvibely/memories.
type PathResolver struct {
	mu          sync.RWMutex
	projectDirs map[string]string
}

// NewPathResolver builds a resolver. The arguments are retained for compatibility
// with existing call sites, but memory no longer has an app-owned root; each
// project must be registered with SetProjectDirOverride before use.
func NewPathResolver(_, _ string) (*PathResolver, error) {
	return &PathResolver{projectDirs: map[string]string{}}, nil
}

// SharedRepoMemoryDir returns the stable repo-local memory directory for a
// project repo.
func SharedRepoMemoryDir(repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", errors.New("memory: repo path is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(repoPath))
	if err != nil {
		return "", fmt.Errorf("memory: repo path: %w", err)
	}
	return filepath.Join(abs, ".openvibely", MemoryDirName), nil
}

// SharedRepoLegacyMemoryDir returns the previous repo-local memory directory for a
// project repo. It is used only for runtime migration to SharedRepoMemoryDir.
func SharedRepoLegacyMemoryDir(repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", errors.New("memory: repo path is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(repoPath))
	if err != nil {
		return "", fmt.Errorf("memory: repo path: %w", err)
	}
	return filepath.Join(abs, ".openvibely", LegacyMemoryDirName), nil
}

// SharedRepoMemoryIndexPath returns the stable repo-local memory index path.
func SharedRepoMemoryIndexPath(repoPath string) (string, error) {
	dir, err := SharedRepoMemoryDir(repoPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, IndexFileName), nil
}

// SetProjectDirOverride binds projectID to its repo-local memory directory.
// Passing an empty dir removes the project binding; memory operations for that
// project will fail until a repo-local directory is registered again.
func (r *PathResolver) SetProjectDirOverride(projectID, dir string) error {
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	dir = strings.TrimSpace(dir)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.projectDirs == nil {
		r.projectDirs = map[string]string{}
	}
	if dir == "" {
		delete(r.projectDirs, projectID)
		return nil
	}
	abs, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return fmt.Errorf("memory: project override dir: %w", err)
	}
	r.projectDirs[projectID] = abs
	return nil
}

// ProjectDir returns the repo-local memory directory registered for projectID.
func (r *PathResolver) ProjectDir(projectID string) (string, error) {
	if err := validateProjectID(projectID); err != nil {
		return "", err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.projectDirs != nil {
		if dir := r.projectDirs[projectID]; dir != "" {
			return dir, nil
		}
	}
	return "", fmt.Errorf("memory: project %s has no repo-local memory directory; configure a local repo_path", projectID)
}

// EnsureProjectDir creates the per-project memory directory. The layout is
// intentionally Claude-style and flat: topic markdown files live beside
// MEMORIES.md in the per-project directory.
func (r *PathResolver) EnsureProjectDir(projectID string) (string, error) {
	dir, err := r.ProjectDir(projectID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create memory dir %s: %w", dir, err)
	}
	return dir, nil
}

// ResolveSafe joins the per-project directory with rel and verifies that the
// resulting path stays inside the per-project memory directory. Symlinks that
// would escape the directory are also rejected.
//
// Callers should always go through ResolveSafe when accepting paths from
// extraction prompts/tools or external sources.
func (r *PathResolver) ResolveSafe(projectID, rel string) (string, error) {
	dir, err := r.ProjectDir(projectID)
	if err != nil {
		return "", err
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New("memory: empty path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("memory: absolute paths not allowed: %s", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("memory: path escapes project memory: %s", rel)
	}
	abs := filepath.Join(dir, cleaned)
	// Final safety: confirm the resolved path is still inside the project dir
	// after symlink resolution (best effort — if the file does not yet exist,
	// resolve only its parent).
	if err := AssertPathWithin(dir, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// AssertPathWithin verifies that target is inside (or equal to) parent after
// evaluating symlinks for the deepest existing ancestor. If neither parent
// nor target exist, it falls back to a plain prefix check.
func AssertPathWithin(parent, target string) error {
	parent = filepath.Clean(parent)
	target = filepath.Clean(target)

	parentAbs, err := evalSymlinksLenient(parent)
	if err != nil {
		return err
	}
	targetAbs, err := evalSymlinksLenient(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(parentAbs, targetAbs)
	if err != nil {
		return fmt.Errorf("memory: cannot relativize %s vs %s: %w", parentAbs, targetAbs, err)
	}
	if rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("memory: path %s escapes %s", target, parent)
	}
	return nil
}

// evalSymlinksLenient evaluates symlinks for the deepest existing ancestor of
// path; missing leaves are appended to the resolved ancestor unchanged.
func evalSymlinksLenient(path string) (string, error) {
	// Walk up to the first existing ancestor, then EvalSymlinks on it.
	stack := []string{}
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached root without finding an existing ancestor.
			break
		}
		stack = append(stack, filepath.Base(cur))
		cur = parent
	}
	resolved, err := filepath.EvalSymlinks(cur)
	if err != nil {
		// Fall back to lexical clean if EvalSymlinks fails (e.g. nothing exists).
		resolved = cur
	}
	for i := len(stack) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, stack[i])
	}
	return filepath.Clean(resolved), nil
}

// validateProjectID rejects empty, traversal-prone, or filesystem-unsafe ids.
// OpenVibely project ids are 32-char lowercase hex strings, but the resolver
// is permissive within an explicit safe character class so tests/seed data
// also work.
func validateProjectID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("memory: project id is empty")
	}
	if len(id) > 128 {
		return errors.New("memory: project id too long")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("memory: invalid character in project id: %q", id)
		}
	}
	return nil
}
