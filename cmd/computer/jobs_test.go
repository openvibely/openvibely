//go:build linux

package main

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestComputerJobSurvivesRequestAndCancelsProcessGroup(t *testing.T) {
	manager := agenttools.NewProcessManager(shellCommand)
	defer manager.Close()
	job, err := manager.StartJob(context.Background(), t.TempDir(), "printf started; sleep 30", 60)
	if err != nil {
		t.Fatal(err)
	}
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
func TestComputerJobsHTTPStreamAndBackground(t *testing.T) {
	mux := http.NewServeMux()
	shellRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, background := range []bool{false, true} {
		body := `{"command":"printf ready; sleep 1; printf done","working_directory":".","timeout_seconds":10,"background":false}`
		if background {
			body = strings.Replace(body, `false`, `true`, 1)
		}
		req, _ := http.NewRequest("POST", server.URL+"/shell", strings.NewReader(body))
		req.Header.Set("Accept", "application/x-ndjson")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
		decoder := json.NewDecoder(res.Body)
		var first map[string]any
		if err = decoder.Decode(&first); err != nil {
			t.Fatal(err)
		}
		if first["job_id"] == "" {
			t.Fatal("missing job ID")
		}
		if background {
			res.Body.Close()
			if first["running"] != true {
				t.Fatal("background did not return immediately")
			}
			cancelRes, err := server.Client().Post(server.URL+"/jobs/cancel", "application/json", strings.NewReader(`{"job_id":"`+first["job_id"].(string)+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			cancelRes.Body.Close()
		} else {
			if first["type"] != "output" {
				t.Fatal("no early streaming result")
			}
			last := first
			for decoder.More() {
				if err = decoder.Decode(&last); err != nil {
					break
				}
			}
			res.Body.Close()
			if last["type"] != "result" || last["output"] != "readydone" {
				t.Fatalf("final result: %#v", last)
			}
		}
	}
}
