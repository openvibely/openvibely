package events

import "testing"

func TestComputerSubscriptionFiltersBeforeQueueing(t *testing.T) {
	b := NewBroadcaster()
	task, err := b.SubscribeComputer("p", "task")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(task)
	project, err := b.SubscribeComputer("p", "")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(project)
	general, err := b.SubscribeProject("p")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Unsubscribe(general)
	// None of these should occupy computer queue slots or signal loss.
	for i := 0; i < 100; i++ {
		for _, ev := range []TaskEvent{
			{ProjectID: "p", TaskID: "other", Type: ComputerStatusChanged},
			{ProjectID: "p", TaskID: "other", Type: TaskDeleted},
			{ProjectID: "p", TaskID: "task", Type: TaskBoardUpdated},
			{ProjectID: "p", Type: AlertCreated},
			{ProjectID: "other", TaskID: "task", Type: ComputerStatusChanged},
		} {
			b.Publish(ev)
		}
	}
	for _, sub := range []Subscriber{task, project} {
		if len(sub) != 0 {
			t.Fatalf("unrelated events occupied computer queue: %d", len(sub))
		}
		select {
		case <-b.Missed(sub):
			t.Fatal("unrelated events caused recovery")
		default:
		}
	}
	if len(general) == 0 {
		t.Fatal("general project subscription lost its events")
	}
	expected := []TaskEvent{
		{ProjectID: "p", Type: ComputerStatusChanged},
		{ProjectID: "p", TaskID: "task", Type: ComputerStatusChanged},
		{ProjectID: "p", TaskID: "task", Type: TaskStatusChanged},
		{ProjectID: "p", TaskID: "task", Type: TaskDeleted},
	}
	for _, ev := range expected {
		b.Publish(ev)
	}
	for _, want := range expected {
		select {
		case got := <-task:
			if got.Type != want.Type || got.TaskID != want.TaskID {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		default:
			t.Fatal("missing relevant task event")
		}
	}
	if len(project) != 1 {
		t.Fatalf("project computer should only receive project-wide change, got %d", len(project))
	}
	// Relevant events still use the bounded queue and signal genuine overflow.
	for i := 0; i < 11; i++ {
		b.Publish(expected[1])
	}
	select {
	case <-b.Missed(task):
	default:
		t.Fatal("relevant overflow did not signal recovery")
	}
}
