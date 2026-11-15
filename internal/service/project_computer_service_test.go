package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

type fakeComputerProvisioner struct {
	connection ComputerConnection
	stopped    bool
}

func (p *fakeComputerProvisioner) Provider() string { return "local" }
func (p *fakeComputerProvisioner) Resolve(context.Context, string) (ComputerConnection, error) {
	return p.connection, nil
}
func (p *fakeComputerProvisioner) Start(context.Context, string) (ComputerConnection, error) {
	return p.connection, nil
}
func (p *fakeComputerProvisioner) Stop(context.Context, string) error { p.stopped = true; return nil }

func TestProjectComputerOwnershipTakeoverAndImages(t *testing.T) {
	calls := 0
	png := append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, make([]byte, 32)...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing bridge authentication")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/health" || strings.HasSuffix(r.URL.Path, "/jobs/cancel") {
			io.WriteString(w, `{"ready":true}`)
			return
		}
		calls++
		json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(png)}}})
	}))
	defer server.Close()
	db := teststores.New(t)
	projects := db.Projects
	project := &models.Project{Name: "Computer test"}
	if err := projects.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	p := &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL, Token: "secret"}}
	svc := NewProjectComputerService(projects, p)
	if rt := svc.runtimeTools(t.Context(), project.ID, "one"); rt != nil {
		t.Fatal("tools enabled before setup")
	}
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	one := svc.runtimeTools(t.Context(), project.ID, "one")
	two := svc.runtimeTools(t.Context(), project.ID, "two")
	output, handled, bad, err := one.Executor(t.Context(), "computer", json.RawMessage(`{"action":"get_desktop_state"}`))
	if err != nil || !handled || bad {
		t.Fatalf("%v %v %v", handled, bad, err)
	}
	if _, ok := agenttools.DecodeImageResult(output); !ok {
		t.Fatal("image was lost")
	}
	if _, _, _, err = two.Executor(t.Context(), "computer", json.RawMessage(`{}`)); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatalf("second task: %v", err)
	}
	if err = svc.Stop(t.Context(), project.ID); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatalf("stop while busy: %v", err)
	}
	if err = svc.Hold(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = one.Executor(t.Context(), "computer", json.RawMessage(`{}`)); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatalf("private takeover: %v", err)
	}
	if calls != 1 {
		t.Fatalf("blocked actions reached desktop: %d", calls)
	}
	if err = svc.WithHumanInput(t.Context(), project.ID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = svc.Hold(t.Context(), project.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = svc.WithHumanInput(t.Context(), project.ID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = two.Executor(t.Context(), "computer", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = two.Executor(t.Context(), "release_computer", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err = svc.Stop(t.Context(), project.ID); err != nil || !p.stopped {
		t.Fatalf("stop: %v", err)
	}
	if rt := svc.runtimeTools(t.Context(), "other-project", "one"); rt != nil {
		t.Fatal("cross-project tools enabled")
	}
}

func TestLocalComputerResolveRejectsOwnershipAndPublicPorts(t *testing.T) {
	for _, tc := range []struct {
		label, host string
		valid       bool
	}{{"project", "127.0.0.1", true}, {"other", "127.0.0.1", false}, {"project", "0.0.0.0", false}} {
		p := &LocalComputerProvisioner{run: func(ctx context.Context, in io.Reader, args ...string) ([]byte, error) {
			return json.Marshal([]any{map[string]any{"Config": map[string]any{"Labels": map[string]string{"openvibely.project": tc.label}, "Env": []string{"OPENVIBELY_COMPUTER_TOKEN=secret"}}, "State": map[string]bool{"Running": true}, "NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": tc.host, "HostPort": "1234"}}}}}})
		}}
		_, err := p.Resolve(t.Context(), "project")
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestHostedComputerProvisionerContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer workspace-secret" {
			t.Error("missing workspace identity")
		}
		if !strings.HasPrefix(r.URL.Path, "/projects/project/computer") {
			t.Error(r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, "/stop") {
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, `{"endpoint":"http://computer:8090","token":"computer-secret"}`)
	}))
	defer server.Close()
	p := &HostedComputerProvisioner{URL: server.URL, Token: "workspace-secret"}
	for _, fn := range []func(context.Context, string) (ComputerConnection, error){p.Resolve, p.Start} {
		c, err := fn(t.Context(), "project")
		if err != nil || c.Token != "computer-secret" {
			t.Fatalf("%+v %v", c, err)
		}
	}
	if err := p.Stop(t.Context(), "project"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENVIBELY_HOSTED_INSTANCE_ID", "workspace")
	t.Setenv("OPENVIBELY_COMPUTER_CONTROL_URL", "")
	if _, ok := defaultComputerProvisioner().(*HostedComputerProvisioner); !ok {
		t.Fatal("hosted fell back to local Docker")
	}
}

func TestProjectComputerTakeoverCancelsAgentAction(t *testing.T) {
	started := make(chan struct{})
	otherDesktop := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || strings.HasSuffix(r.URL.Path, "/jobs/cancel") {
			io.WriteString(w, `{"ready":true}`)
			return
		}
		if strings.Contains(r.URL.Path, otherDesktop) && otherDesktop != "" {
			io.WriteString(w, `{}`)
			return
		}
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	db := teststores.New(t)
	projects := db.Projects
	project := &models.Project{Name: "Private takeover"}
	if err := projects.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	svc := NewProjectComputerService(projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL, Token: "secret"}})
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Task", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := db.Tasks.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	other := &models.Task{ProjectID: project.ID, Title: "Concurrent", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := db.Tasks.Create(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	enableComputerTaskForTest(t, svc, project.ID, task.ID)
	enableComputerTaskForTest(t, svc, project.ID, other.ID)
	otherDesktop = ComputerDesktopID(other.ID)
	runner := &LLMService{computerSvc: svc}
	tools := runner.taskActionRuntimeTools(t.Context(), *task)
	if tools == nil || !tools.HasDefinition("computer") {
		t.Fatal("task runner did not expose the configured computer")
	}
	finished := make(chan error, 1)
	go func() {
		_, _, _, err := tools.Executor(t.Context(), "computer", json.RawMessage(`{"action":"get_desktop_state"}`))
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("action did not start")
	}
	otherTools := svc.RuntimeTools(t.Context(), project.ID, other.ID)
	otherDone := make(chan error, 1)
	go func() {
		_, _, _, err := otherTools.Executor(t.Context(), "project_browser", json.RawMessage(`{"action":"read"}`))
		otherDone <- err
	}()
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("one task blocked another task desktop")
	}
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.Hold(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("private takeover returned an agent screenshot")
		}
	case <-time.After(time.Second):
		t.Fatal("takeover did not cancel action")
	}
}

func TestLocalComputerDeletionChecksVolumeOwnership(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			calls := []string{}
			name := computerContainerName("project")
			p := &LocalComputerProvisioner{run: func(ctx context.Context, in io.Reader, args ...string) ([]byte, error) {
				calls = append(calls, strings.Join(args, " "))
				switch args[0] {
				case "container":
					return []byte(name), nil
				case "volume":
					if args[1] == "ls" {
						return []byte(name + "-home"), nil
					}
				case "inspect":
					volume := name + "-home"
					if !owned {
						volume = "someone-else"
					}
					return json.Marshal([]any{map[string]any{"Config": map[string]any{"Labels": map[string]string{"openvibely.project": "project"}}, "State": map[string]bool{"Running": false}, "Mounts": []map[string]string{{"Name": volume, "Destination": "/home/ubuntu", "Type": "volume"}}}})
				}
				return nil, nil
			}}
			err := p.RemoveProject(t.Context(), "project")
			if (err == nil) != owned {
				t.Fatal(err)
			}
			joined := strings.Join(calls, "\n")
			if !owned && strings.Contains(joined, "volume rm") {
				t.Fatal("removed unrelated volume")
			}
			if owned && !strings.Contains(joined, "volume rm "+name+"-home") {
				t.Fatal("owned volume not removed")
			}
		})
	}
}

func enableComputerTaskForTest(t *testing.T, svc *ProjectComputerService, projectID, taskID string) {
	t.Helper()
	scoped, err := svc.ForTask(t.Context(), projectID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetEnabled(t.Context(), projectID, true); err != nil {
		t.Fatal(err)
	}
	if err = scoped.SetTaskEnabled(t.Context(), projectID, true); err != nil {
		t.Fatal(err)
	}
}
