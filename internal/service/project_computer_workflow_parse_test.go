package service

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"testing"
)

func TestWorkflowJSONFences(t *testing.T) {
	raw := `{"name":"Report","instructions":"Open Reports"}`
	for _, input := range []string{raw, "  " + raw + "\n", "```json\n" + raw + "\n```", "```\n" + raw + "\n```", "```json\r\n" + raw + "\r\n```"} {
		var w models.ComputerWorkflow
		if err := json.Unmarshal([]byte(unwrapWorkflowJSON(input)), &w); err != nil || w.Name != "Report" {
			t.Fatalf("%q: %+v %v", input, w, err)
		}
	}
	for _, input := range []string{"```json\n" + raw, "Explanation\n" + raw, "```json\n" + raw + "\n```\ntrailing"} {
		var w models.ComputerWorkflow
		if json.Unmarshal([]byte(unwrapWorkflowJSON(input)), &w) == nil {
			t.Fatalf("accepted malformed response %q", input)
		}
	}
}

func TestComputerCancellationWaitsForOriginalRegistration(t *testing.T) {
	w := &WorkerService{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := w.RegisterCancelOwned("task", cancel)
	done := w.CancelForComputer("task")
	if ctx.Err() != context.Canceled {
		t.Fatal("run not cancelled")
	}
	select {
	case <-done:
		t.Fatal("completion signaled before cleanup")
	default:
	}
	nextCtx, nextCancel := context.WithCancel(t.Context())
	defer nextCancel()
	next := w.RegisterCancelOwned("task", nextCancel)
	w.DeregisterCancelOwned("task", first)
	select {
	case <-done:
	default:
		t.Fatal("original cleanup did not complete wait")
	}
	if nextCtx.Err() != nil {
		t.Fatal("follow-up cancelled")
	}
	w.DeregisterCancelOwned("task", next)
	w.DeregisterCancelOwned("task", first)
}

func TestComputerCancellationWaitsAfterOrdinaryCancellation(t *testing.T) {
	w := &WorkerService{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	registration := w.RegisterCancelOwned("task", cancel)
	if !w.CancelRunningTask("task") {
		t.Fatal("run was not cancelled")
	}
	done := w.CancelForComputer("task")
	if done == nil {
		t.Fatal("lost completion identity")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("context not cancelled")
	}
	select {
	case <-done:
		t.Fatal("cleanup has not finished")
	default:
	}
	w.DeregisterCancelOwned("task", registration)
	select {
	case <-done:
	default:
		t.Fatal("cleanup did not acknowledge completion")
	}
}
