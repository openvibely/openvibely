package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openvibely/openvibely/internal/util"
)

// FileStore performs minimal filesystem setup for the per-project managed
// memory directory. The Memory Curator agent owns all subsequent reads and
// writes via its scoped-file tools, so this store only owns directory
// creation and the legacy MEMORY.md -> MEMORIES.md migration.
type FileStore struct {
	resolver *PathResolver
}

// NewFileStore returns a file store backed by the given resolver.
func NewFileStore(resolver *PathResolver) *FileStore {
	return &FileStore{resolver: resolver}
}

// Resolver returns the underlying path resolver.
func (s *FileStore) Resolver() *PathResolver { return s.resolver }

const defaultIndexContents = `# Project Memory

This index tracks durable top-level memory topic files in this directory.

- No topic files have been created yet.
`

// EnsureProject creates the per-project directory layout and initializes the
// MEMORIES.md index when it is missing. If the previous MEMORY.md index exists
// and MEMORIES.md does not, the legacy file is renamed to the canonical name.
// Idempotent.
func (s *FileStore) EnsureProject(projectID string) (string, error) {
	dir, err := s.resolver.EnsureProjectDir(projectID)
	if err != nil {
		return "", err
	}
	indexPath := filepath.Join(dir, IndexFileName)
	if _, err := os.Stat(indexPath); err == nil {
		return dir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("memory: stat index: %w", err)
	}
	legacyIndexPath := filepath.Join(dir, LegacyIndexFileName)
	if _, err := os.Stat(legacyIndexPath); err == nil {
		if err := os.Rename(legacyIndexPath, indexPath); err != nil {
			return "", fmt.Errorf("memory: migrate legacy index: %w", err)
		}
		return dir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("memory: stat legacy index: %w", err)
	}
	if err := util.WriteFileAtomic(indexPath, []byte(defaultIndexContents), 0o644); err != nil {
		return "", fmt.Errorf("memory: initialize index: %w", err)
	}
	return dir, nil
}
