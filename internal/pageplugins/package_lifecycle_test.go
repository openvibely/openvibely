package pageplugins

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

// Pause inside the store, after the manager's admission lock should be held.
type packageAdmissionStore struct {
	persistence.PagePluginStore
	createEntered, countEntered chan struct{}
	release                     chan struct{}
	createFirst                 bool
}

func (s *packageAdmissionStore) CreateInstance(ctx context.Context, in *models.PagePluginInstance) error {
	close(s.createEntered)
	if s.createFirst {
		<-s.release
	}
	return s.PagePluginStore.CreateInstance(ctx, in)
}
func (s *packageAdmissionStore) CountInstancesForPackage(ctx context.Context, id string) (int, error) {
	n, err := s.PagePluginStore.CountInstancesForPackage(ctx, id)
	close(s.countEntered)
	if !s.createFirst {
		<-s.release
	}
	return n, err
}

func TestPackageRemovalSerializesInstanceCreation(t *testing.T) {
	for _, createFirst := range []bool{true, false} {
		name := "removal wins"
		if createFirst {
			name = "creation wins"
		}
		t.Run(name, func(t *testing.T) {
			m, env, project, _ := newTestManager(t)
			ctx := t.Context()
			pkg, err := m.InstallExample(ctx, "admin")
			require.NoError(t, err)
			store := &packageAdmissionStore{PagePluginStore: env.PagePlugins, createEntered: make(chan struct{}), countEntered: make(chan struct{}), release: make(chan struct{}), createFirst: createFirst}
			m.store = store
			type creation struct {
				instance *models.PagePluginInstance
				err      error
			}
			created := make(chan creation, 1)
			removed := make(chan error, 1)
			create := func() { in, err := m.CreateInstance(ctx, project, pkg.ID, ""); created <- creation{in, err} }
			remove := func() { removed <- m.RemovePackage(ctx, pkg.ID) }
			var secondEntered <-chan struct{}
			if createFirst {
				go create()
				<-store.createEntered
				go remove()
				secondEntered = store.countEntered
			} else {
				go remove()
				<-store.countEntered
				go create()
				secondEntered = store.createEntered
			}
			overlapped := false
			select {
			case <-secondEntered:
				overlapped = true
			case <-time.After(50 * time.Millisecond):
			}
			close(store.release)
			result := <-created
			removeErr := <-removed
			require.False(t, overlapped, "package admission overlapped removal's unused check")
			if createFirst {
				require.NoError(t, result.err)
				require.ErrorIs(t, removeErr, ErrPackageInUse)
				_, err = os.Stat(pkg.InstallPath)
				require.NoError(t, err)
				in, err := env.PagePlugins.GetInstance(ctx, result.instance.ID)
				require.NoError(t, err)
				require.NotNil(t, in)
			} else {
				require.NoError(t, removeErr)
				require.ErrorIs(t, result.err, ErrNotFound)
				require.Nil(t, result.instance)
				_, err = os.Stat(pkg.InstallPath)
				require.True(t, os.IsNotExist(err))
				saved, err := env.PagePlugins.GetPackage(ctx, pkg.ID)
				require.NoError(t, err)
				require.Nil(t, saved)
			}
		})
	}
}
