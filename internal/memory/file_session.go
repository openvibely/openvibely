package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/openvibely/openvibely/pkg/knowledgecache"
)

var storeLocks sync.Map

// LockStore serializes managed memory transactions, including scoped maintenance
// operations. Resolve directory aliases so both tool surfaces use the same lock.
func LockStore(dir string) func() {
	dir, _ = filepath.Abs(dir)
	// Resolve the existing ancestor even before the first save creates the store.
	for parent := dir; ; parent = filepath.Dir(parent) {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			suffix, _ := filepath.Rel(parent, dir)
			dir = filepath.Join(resolved, suffix)
			break
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	lock, _ := storeLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type fileVersion struct {
	Exists bool
	Hash   [32]byte
}

func version(path string) (fileVersion, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileVersion{}, nil
	}
	if err != nil {
		return fileVersion{}, err
	}
	return fileVersion{true, sha256.Sum256(data)}, nil
}

// FileSession remembers what a maintenance model has actually read. A lock alone
// cannot protect a model's read/think/write interval from a concurrent task save.
type FileSession struct {
	mu   sync.Mutex
	seen map[string]fileVersion
}

// Do leaves unrelated scoped files unchanged. Existing managed memory files must
// be read in this session before mutation; stale writes require a fresh read.
func (s *FileSession) Do(ctx context.Context, path string, write bool, operation func() error) error {
	root := filepath.Dir(path)
	for root != filepath.Dir(root) {
		if filepath.Base(root) == "memories" && filepath.Base(filepath.Dir(root)) == ".openvibely" {
			break
		}
		root = filepath.Dir(root)
	}
	if root == filepath.Dir(root) {
		return operation()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock := LockStore(root)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := version(path)
	if err != nil {
		return err
	}
	if s.seen == nil {
		s.seen = make(map[string]fileVersion)
	}
	previous, read := s.seen[path]
	if write && ((read && previous != current) || (!read && current.Exists)) {
		return fmt.Errorf("memory file changed or has not been read in this session: read_file %s, then retry using its current contents", path)
	}
	if err := operation(); err != nil {
		if !write && !current.Exists {
			s.seen[path] = current
		}
		return err
	}
	next, err := version(path)
	if err != nil {
		return err
	}
	s.seen[path] = next
	if write {
		knowledgecache.Invalidate(root)
	}
	return nil
}
