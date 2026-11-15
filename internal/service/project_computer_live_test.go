package service

import (
	"encoding/json"
	"fmt"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

// Uses only newly created, disposable Docker resources and an explicit test image.
func TestProjectComputerLocalDockerLifecycle(t *testing.T) {
	image := os.Getenv("OPENVIBELY_TEST_COMPUTER_IMAGE")
	if image == "" {
		t.Skip("set OPENVIBELY_TEST_COMPUTER_IMAGE for isolated Docker integration")
	}
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", image)
	db := teststores.New(t)
	repo := db.Projects
	project := &models.Project{Name: "Disposable computer integration"}
	if err := repo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	name := computerContainerName(project.ID)
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Logf("container cleanup: %s", out)
		}
		if out, err := exec.Command("docker", "volume", "rm", name+"-home").CombinedOutput(); err != nil {
			t.Errorf("volume cleanup: %s", out)
		}
	})
	svc := NewProjectComputerService(repo, &LocalComputerProvisioner{})
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Lifecycle", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := db.Tasks.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	enableComputerTaskForTest(t, svc, project.ID, task.ID)
	tools := svc.RuntimeTools(t.Context(), project.ID, task.ID)
	call := func(name, body string) string {
		t.Helper()
		out, handled, bad, err := tools.Executor(t.Context(), name, json.RawMessage(body))
		if err != nil || bad || !handled {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	call("computer_files", `{"operation":"write_file","input":{"file_path":"persist.txt","content":"durable"}}`)
	call("release_computer", `{}`)
	if err := svc.Stop(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stop(t.Context(), project.ID); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}
	if out, err := exec.Command("docker", "rm", name).CombinedOutput(); err != nil {
		t.Fatalf("replace fixture container: %v %s", err, out)
	}
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if got := call("computer_files", `{"operation":"read_file","input":{"file_path":"persist.txt"}}`); got != "durable" {
		t.Fatalf("persistent home lost: %q", got)
	}
	call("computer", `{"action":"get_desktop_state"}`)
	call("release_computer", `{}`)
}

func TestProjectComputerTaskDesktopsLive(t *testing.T) {
	image := os.Getenv("OPENVIBELY_TEST_COMPUTER_IMAGE")
	if image == "" {
		t.Skip("requires disposable computer image")
	}
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", image)
	db := teststores.New(t)
	repo := db.Projects
	project := &models.Project{Name: "Task desktops", RepoPath: t.TempDir()}
	if err := repo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	name := computerContainerName(project.ID)
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", name).Run()
		if err := exec.Command("docker", "volume", "rm", name+"-home").Run(); err != nil {
			t.Error(err)
		}
	})
	svc := NewProjectComputerService(repo, &LocalComputerProvisioner{})
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	tasks := db.Tasks
	ids := []string{}
	for _, title := range []string{"First", "Second"} {
		task := &models.Task{ProjectID: project.ID, Title: title, Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
		if err := tasks.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	enableComputerTaskForTest(t, svc, project.ID, ids[0])
	enableComputerTaskForTest(t, svc, project.ID, ids[1])
	first := svc.RuntimeTools(t.Context(), project.ID, ids[0])
	second := svc.RuntimeTools(t.Context(), project.ID, ids[1])
	runtimes := []*llmcontracts.RuntimeTools{first, second}
	call := func(n int, name, body string) string {
		t.Helper()
		out, handled, bad, err := runtimes[n].Executor(t.Context(), name, json.RawMessage(body))
		if err != nil || bad || !handled {
			t.Fatalf("%s: %v %s", name, err, out)
		}
		return out
	}
	for i := range ids {
		call(i, "computer_files", fmt.Sprintf(`{"operation":"write_file","input":{"file_path":"same.txt","content":"task-%d"}}`, i))
		call(i, "project_browser", `{"action":"navigate","url":"http://127.0.0.1:10001/json/version"}`)
	}
	for i := range ids {
		if got := call(i, "computer_files", `{"operation":"read_file","input":{"file_path":"same.txt"}}`); got != fmt.Sprintf("task-%d", i) {
			t.Fatal("task files shared", got)
		}
		call(i, "computer", `{"action":"get_desktop_state"}`)
	}
	jobOutput := call(0, "computer_shell", `{"command":"printf ready; sleep 30","yield_time_ms":250,"timeout":60}`)
	session := regexp.MustCompile(`session_id: (sh_[a-f0-9]+)`).FindStringSubmatch(jobOutput)
	if len(session) != 2 {
		t.Fatal("session did not start", jobOutput)
	}
	jobID := session[1]
	_, handled, bad, err := second.Executor(t.Context(), "computer_job", json.RawMessage(fmt.Sprintf(`{"session_id":%q}`, jobID)))
	if !handled || !bad || err == nil {
		t.Fatal("session leaked into another task desktop")
	}
	jobRead := call(0, "computer_job", fmt.Sprintf(`{"session_id":%q}`, jobID))
	if !strings.Contains(jobRead, jobID) {
		t.Fatal("session disappeared", jobRead)
	}
	call(0, "request_browser_login", `{"url":"http://127.0.0.1:10001/json/version","expected_text":"Protocol-Version"}`)
	if _, _, _, err := first.Executor(t.Context(), "project_browser", json.RawMessage(`{"action":"read"}`)); err == nil {
		t.Fatal("login not private")
	}
	call(1, "project_browser", `{"action":"read"}`)
	scoped, err := svc.ForTask(t.Context(), project.ID, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	conn, err := scoped.Connection(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobData, err := scoped.request(t.Context(), conn, "GET", "/jobs?job_id="+jobID, nil)
	if err != nil || strings.Contains(string(jobData), `"running":true`) {
		t.Fatal("private login left a background command running", err, string(jobData))
	}
	if err := scoped.CompleteLogin(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	for i := range ids {
		call(i, "release_computer", `{}`)
	}
	if err := svc.Stop(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "rm", name).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	for i := range ids {
		if got := call(i, "computer_files", `{"operation":"read_file","input":{"file_path":"same.txt"}}`); got != fmt.Sprintf("task-%d", i) {
			t.Fatal("session persistence failed", got)
		}
		call(i, "release_computer", `{}`)
	}
}
