package sqlite

import (
	"database/sql"
	"errors"
	"github.com/openvibely/openvibely/internal/persistence"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestComputerReservationSurvivesServiceRestartAndExpires(t *testing.T) {
	db := testutil.NewTestDB(t)
	projects := NewProjectRepo(db)
	p := &models.Project{Name: "Desktop"}
	if err := projects.Create(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	repo := projects.Computers()
	if err := repo.Configure(t.Context(), p.ID, "local"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reserve(t.Context(), p.ID, "first"); err != nil {
		t.Fatal(err)
	}
	restarted := NewProjectRepo(db).Computers()
	if err := restarted.Reserve(t.Context(), p.ID, "second"); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatal(err)
	}
	if err := restarted.Release(t.Context(), p.ID, "second"); err != nil {
		t.Fatal(err)
	}
	state, err := repo.Get(t.Context(), p.ID)
	if err != nil || state.Owner != "first" {
		t.Fatalf("%+v %v", state, err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE project_computers SET lease_until=0 WHERE project_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reserve(t.Context(), p.ID, "second"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Hold(t.Context(), p.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reserve(t.Context(), p.ID, "second"); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatal(err)
	}
	if err := repo.Configure(t.Context(), "nonexistent", "local"); err == nil {
		t.Fatal("accepted missing project")
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM projects WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(t.Context(), p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestTaskComputerSessionPersistsLoginAndCascades(t *testing.T) {
	db := testutil.NewTestDB(t)
	projects := NewProjectRepo(db)
	project := &models.Project{Name: "Task sessions"}
	if err := projects.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	if err := projects.Computers().Configure(t.Context(), project.ID, "local"); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Task", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := NewTaskRepo(db, nil).Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	scoped, err := projects.Computers().ForTask(t.Context(), project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.RequestHandoff(t.Context(), project.ID, "request_browser_login", "https://example.test", "", ""); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewProjectRepo(db).Computers().ForTask(t.Context(), project.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := restarted.Get(t.Context(), project.ID)
	if err != nil || !state.Held || state.HandoffKind != "request_browser_login" {
		t.Fatal("private login not preserved", err)
	}
	if err := restarted.Reserve(t.Context(), project.ID, "agent"); !errors.Is(err, persistence.ErrComputerBusy) {
		t.Fatal("restart exposed held desktop")
	}
	if err := restarted.FinishHandoff(t.Context(), project.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reserve(t.Context(), project.ID, "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE task_computer_sessions SET lease_until=0 WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := scoped.Reserve(t.Context(), project.ID, "recovered"); err != nil {
		t.Fatal("expired session reservation blocked recovery", err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM tasks WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Get(t.Context(), project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("session survived task deletion", err)
	}
}
