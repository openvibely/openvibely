package update

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDrainClosesAdmissionBeforeSnapshotAndRequiresQuietInterval(t *testing.T) {
	now := time.Unix(1000, 0)
	active := ActiveWork{TaskExecutions: 1}
	m := NewDrainManager(func() ActiveWork { return active }, func() int { return 2 }, time.Second, func() time.Time { return now })
	status, err := m.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if m.Admit() {
		t.Fatal("admission remained open")
	}
	if status.State != DrainStateDraining || status.Active.TaskExecutions != 1 {
		t.Fatalf("status = %#v", status)
	}
	active = ActiveWork{}
	if got := m.Status(); got.State != DrainStateDraining {
		t.Fatalf("became ready before quiet interval: %#v", got)
	}
	now = now.Add(time.Second)
	if got := m.Status(); got.State != DrainStateReady {
		t.Fatalf("status after quiet interval = %#v", got)
	}
}

func TestDrainCancellationAndUnownedExpiryReopenAdmission(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewDrainManager(func() ActiveWork { return ActiveWork{} }, nil, 0, func() time.Time { return now })
	if _, err := m.BeginDrain(DrainRequest{Lease: time.Second}); err != nil {
		t.Fatal(err)
	}
	m.CancelDrain()
	if !m.Admit() {
		t.Fatal("cancel did not reopen admission")
	}
	if _, err := m.BeginDrain(DrainRequest{Lease: time.Second}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if got := m.Status(); got.State != DrainStateIdle || !m.Admit() {
		t.Fatalf("expired status = %#v, admit=%v", got, m.Admit())
	}
}

func TestDrainPublishesReopenedSignalForReleaseCancellationAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	assertSignal := func(label string) {
		t.Helper()
		select {
		case <-m.Reopened():
		case <-time.After(time.Second):
			t.Fatalf("%s did not publish drain-reopened signal", label)
		}
	}

	started, err := m.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Release(started.Generation) {
		t.Fatal("release failed")
	}
	assertSignal("release")

	if _, err := m.BeginDrain(DrainRequest{Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if !m.CancelDrain() {
		t.Fatal("cancellation failed")
	}
	assertSignal("cancellation")

	if _, err := m.BeginDrain(DrainRequest{Lease: time.Second}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if status := m.Status(); status.State != DrainStateIdle {
		t.Fatalf("expired status=%#v", status)
	}
	assertSignal("expiry")
}

func TestDrainGenerationCannotBeReplacedOrOwnedTwice(t *testing.T) {
	m := NewDrainManager(nil, nil, 0, time.Now)
	first, err := m.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.BeginDrain(DrainRequest{Lease: time.Minute}); err == nil {
		t.Fatal("active drain generation was replaced")
	}
	if !m.TakeOwnership(first.Generation) {
		t.Fatal("first ownership claim failed")
	}
	if m.TakeOwnership(first.Generation) {
		t.Fatal("drain generation was owned twice")
	}
	if m.Release("stale-generation") {
		t.Fatal("stale generation released active drain")
	}
	if !m.Release(first.Generation) || m.Status().State != DrainStateIdle {
		t.Fatal("owner could not release exact generation")
	}
}

func TestDrainPersistsGenerationAndRestoresClosedAdmission(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	path := filepath.Join(t.TempDir(), "drain.json")
	m := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := m.SetPersistence(path); err != nil {
		t.Fatal(err)
	}
	started, err := m.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	restored := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := restored.SetPersistence(path); err != nil {
		t.Fatal(err)
	}
	status := restored.Status()
	if status.Generation != started.Generation || status.State == DrainStateIdle || restored.Admit() {
		t.Fatalf("restored status = %#v", status)
	}
	restored.CancelDrain()
	third := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := third.SetPersistence(path); err != nil {
		t.Fatal(err)
	}
	if !third.Admit() {
		t.Fatal("persisted cancellation did not reopen admission")
	}
}

func TestDrainPersistenceFailureDoesNotPublishOwnershipRenewalOrRelease(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	m := NewDrainManager(nil, nil, 0, func() time.Time { return now })
	if err := m.SetPersistence(filepath.Join(t.TempDir(), "drain.json")); err != nil {
		t.Fatal(err)
	}
	started, err := m.BeginDrain(DrainRequest{Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	originalExpiry := started.ExpiresAt
	m.stateWriter = func(string, []byte) error { return errors.New("disk full") }
	if m.TakeOwnership(started.Generation) || m.Owns(started.Generation) {
		t.Fatal("ownership was published despite persistence failure")
	}
	if m.Renew(started.Generation, 2*time.Minute) || !m.Status().ExpiresAt.Equal(originalExpiry) {
		t.Fatal("lease renewal was published despite persistence failure")
	}
	if m.Release(started.Generation) || m.Status().State == DrainStateIdle {
		t.Fatal("drain release was published despite persistence failure")
	}
}

func TestWorkTrackerAccountsEveryClassAndRejectsAfterDrainCloses(t *testing.T) {
	tracker := NewWorkTracker()
	for _, class := range []WorkClass{WorkTask, WorkChat, WorkWorkflow, WorkAutomation} {
		done, err := tracker.Start(class)
		if err != nil {
			t.Fatalf("start %s: %v", class, err)
		}
		defer done()
	}
	if got := tracker.Active(); got != (ActiveWork{TaskExecutions: 1, ChatExecutions: 1, WorkflowExecutions: 1, AutomationActivities: 1}) {
		t.Fatalf("active = %#v", got)
	}
	tracker.Close()
	if _, err := tracker.Start(WorkTask); !errors.Is(err, ErrDraining) {
		t.Fatalf("start after close = %v", err)
	}
}
