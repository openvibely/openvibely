//go:build unix && !hurd

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestComputerTransferRejectsPipesAndTransfersRegularFiles(t *testing.T) {
	db := teststores.New(t)
	dir := t.TempDir()
	project := &models.Project{Name: "Transfer", RepoPath: dir}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	task := &models.Task{ProjectID: project.ID, Title: "Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		requests <- string(data)
		w.WriteHeader(200)
	}))
	defer server.Close()
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL}})
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	pipe := filepath.Join(dir, "pipe")
	require.NoError(t, unix.Mkfifo(pipe, 0600))
	require.NoError(t, os.Symlink("pipe", filepath.Join(dir, "pipe-link")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), []byte("payload"), 0600))
	require.NoError(t, os.Symlink("file", filepath.Join(dir, "file-link")))
	for _, name := range []string{"pipe", "pipe-link"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := scoped.transfer(ctx, project.ID, "to_computer", name, "destination"); done <- err }()
			select {
			case err := <-done:
				require.ErrorContains(t, err, "regular file")
			case <-ctx.Done():
				// Unblock a regressed FIFO open so the test leaves no blocked goroutine.
				f, err := os.OpenFile(pipe, os.O_WRONLY|unix.O_NONBLOCK, 0)
				if err == nil {
					f.Close()
				}
				t.Fatal("transfer blocked on a named pipe")
			}
		})
	}
	require.Empty(t, requests, "special files must never reach the computer")
	for _, name := range []string{"file", "file-link"} {
		_, err := scoped.transfer(t.Context(), project.ID, "to_computer", name, "destination")
		require.NoError(t, err)
		require.Equal(t, "payload", <-requests)
	}
	// Exercise the actual open primitive independently of the stat precheck:
	// this is what prevents blocking if a file is swapped for a FIFO.
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		f, err := openComputerTransferFile(root, "pipe")
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		f, _ := os.OpenFile(pipe, os.O_WRONLY|unix.O_NONBLOCK, 0)
		if f != nil {
			f.Close()
		}
		t.Fatal("open primitive blocked")
	}
}
