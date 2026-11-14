package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

func TestProjectComputerTaskSessionsAndLogin(t *testing.T) {
	db := teststores.New(t)
	projects := db.Projects
	project := &models.Project{Name: "Sessions", RepoPath: t.TempDir()}
	if err := projects.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	tasks := db.Tasks
	one := &models.Task{ProjectID: project.ID, Title: "One", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending}
	two := &models.Task{ProjectID: project.ID, Title: "Two", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending}
	for _, task := range []*models.Task{one, two} {
		if err := tasks.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/files") {
			if r.Method == "PUT" {
				data, _ := io.ReadAll(r.Body)
				if string(data) != "fixture" {
					t.Error("wrong file content")
				}
			} else {
				w.Header().Set("Content-Type", "application/octet-stream")
				io.WriteString(w, "download")
				return
			}
		}
		io.WriteString(w, `{"ready":true,"verified":true}`)
	}))
	defer server.Close()
	svc := NewProjectComputerService(projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL, Token: "secret"}})
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if rt := svc.RuntimeTools(t.Context(), project.ID, "not-a-task"); rt != nil {
		t.Fatal("invented task got desktop")
	}
	enableComputerTaskForTest(t, svc, project.ID, one.ID)
	enableComputerTaskForTest(t, svc, project.ID, two.ID)
	first := svc.RuntimeTools(t.Context(), project.ID, one.ID)
	second := svc.RuntimeTools(t.Context(), project.ID, two.ID)
	for _, def := range first.Definitions {
		if def.Schema == nil {
			t.Fatalf("missing schema for %s", def.Name)
		}
		if _, err := toolschema.MarshalOpenAI(def.Schema, nil); err != nil {
			t.Fatalf("%s schema: %v", def.Name, err)
		}
	}
	execute := func(task int, name, input string) error {
		rt := first
		if task == 2 {
			rt = second
		}
		_, handled, bad, err := rt.Executor(t.Context(), name, json.RawMessage(input))
		if !handled {
			t.Fatalf("unhandled %s", name)
		}
		if bad && err == nil {
			t.Fatalf("failed without error %s", name)
		}
		return err
	}
	for _, n := range []int{1, 2} {
		if err := execute(n, "project_browser", `{"action":"read"}`); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(strings.Join(paths, " "), ComputerDesktopID(one.ID)) || !strings.Contains(strings.Join(paths, " "), ComputerDesktopID(two.ID)) {
		t.Fatal("tasks did not use separate desktop routes")
	}
	if err := execute(1, "project_browser", `{"action":"import_cookies","cookies":[]}`); err == nil {
		t.Fatal("model could import secrets")
	}
	if err := execute(1, "request_browser_login", `{"url":"https://example.test/login","expected_text":"Dashboard"}`); err != nil {
		t.Fatal(err)
	}
	if err := execute(1, "project_browser", `{"action":"read"}`); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatalf("private login accessible: %v", err)
	}
	if err := execute(2, "project_browser", `{"action":"read"}`); err != nil {
		t.Fatal("another task blocked", err)
	}
	scoped, err := svc.ForTask(t.Context(), project.ID, one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.Hold(t.Context(), project.ID, false); err == nil {
		t.Fatal("normal release bypassed login")
	}
	if err := svc.Stop(t.Context(), project.ID); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatal("stopped active sessions", err)
	}
	if err := scoped.CompleteLogin(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.RepoPath, "fixture.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := execute(1, "computer_transfer", `{"direction":"to_computer","local_path":"fixture.txt","computer_path":"fixture.txt"}`); err != nil {
		t.Fatal(err)
	}
	if err := execute(1, "computer_transfer", `{"direction":"from_computer","local_path":"download.txt","computer_path":"download.txt"}`); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(project.RepoPath, "download.txt")); err != nil || string(data) != "download" {
		t.Fatal("download missing", err)
	}
	if err := execute(1, "computer_transfer", `{"direction":"from_computer","local_path":"download.txt","computer_path":"download.txt"}`); err == nil {
		t.Fatal("overwrote existing file")
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(project.RepoPath, "escape"))
	if err := execute(1, "computer_transfer", `{"direction":"to_computer","local_path":"escape/secret","computer_path":"secret"}`); err == nil {
		t.Fatal("symlink escaped worktree")
	}
	for _, n := range []int{1, 2} {
		if err := execute(n, "release_computer", `{}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Stop(context.Background(), project.ID); err != nil {
		t.Fatal(err)
	}
}

type removingComputerProvisioner struct {
	fakeComputerProvisioner
	removed func(context.Context, string) error
}

func (p *removingComputerProvisioner) RemoveProject(ctx context.Context, id string) error {
	return p.removed(ctx, id)
}
func TestProjectComputerDeletionCleansBeforeCommit(t *testing.T) {
	db := teststores.New(t)
	projects := db.Projects
	project := &models.Project{Name: "Deletion"}
	if err := projects.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	removed := false
	closed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			closed = true
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	p := &removingComputerProvisioner{fakeComputerProvisioner: fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL, Token: "fixture"}}, removed: func(ctx context.Context, id string) error {
		removed = true
		return nil
	}}
	svc := NewProjectComputerService(projects, p)
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	tasks := db.Tasks
	task := &models.Task{ProjectID: project.ID, Title: "Task", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := tasks.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	taskSvc := NewTaskService(tasks, nil, nil)
	taskSvc.SetProjectComputerService(svc)
	if err := taskSvc.Delete(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("task desktop not closed")
	}
	projectSvc := NewProjectService(projects)
	projectSvc.SetProjectComputerService(svc)
	projectSvc.SetTaskService(taskSvc)
	if err := projectSvc.Delete(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if !removed || !p.stopped {
		t.Fatal("project computer not cleaned up")
	}
}
