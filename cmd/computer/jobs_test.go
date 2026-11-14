//go:build linux

package main

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"strings"
	"testing"
	"time"
)

func TestComputerJobSurvivesRequestAndCancelsProcessGroup(t *testing.T) {
	manager := agenttools.NewProcessManager(shellCommand)
	defer manager.Close()
	_, err := manager.Execute(context.Background(), t.TempDir(), "shell", json.RawMessage(`{"command":"printf started; sleep 30","timeout":60,"yield_time_ms":250}`))
	if err != nil {
		t.Fatal(err)
	}
	job := manager.Jobs("")[0]
	defer job.Cancel()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(job.Snapshot()["output"].(string), "started") {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("no live output")
		case <-tick.C:
		}
	}
	if job.Snapshot()["running"] != true {
		t.Fatal("job finished before cancellation")
	}
	job.Cancel()
	select {
	case <-job.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("job did not cancel")
	}
	if job.Snapshot()["running"] != false {
		t.Fatal("job still running")
	}
}
