package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Foreground requests consume incremental results; the same bounded output is
// available in the task panel while the tool call is still running.
func (s *ProjectComputerService) shell(ctx context.Context, conn ComputerConnection, input []byte) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", conn.Endpoint+"/shell", bytes.NewReader(input))
	if err != nil {
		return "", true, err
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	req.Header.Set("Accept", "application/x-ndjson")
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return "", true, errors.New("computer command interrupted or unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", true, errors.New("computer command could not start")
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 128<<20))
	var request struct {
		Background bool `json:"background"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return "", true, err
	}
	var last json.RawMessage
	for {
		var next json.RawMessage
		err = decoder.Decode(&next)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", true, errors.New("computer output stream interrupted")
		}
		last = next
	}
	if len(last) == 0 {
		return "", true, errors.New("computer returned no command result")
	}
	var result struct {
		ExitCode int    `json:"exit_code"`
		Type     string `json:"type"`
		TimedOut bool   `json:"timed_out"`
	}
	if err = json.Unmarshal(last, &result); err != nil {
		return "", true, err
	}
	if !request.Background && res.Header.Get("Content-Type") == "application/x-ndjson" && result.Type != "result" {
		return "", true, errors.New("computer output stream ended before completion")
	}
	return string(last), result.ExitCode != 0 || result.TimedOut, nil
}
func (s *ProjectComputerService) Jobs(ctx context.Context, id, jobID string) ([]byte, error) {
	if s.taskID == "" {
		return nil, errors.New("choose a task desktop")
	}
	conn, err := s.Connection(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.request(ctx, conn, "GET", "/jobs?job_id="+url.QueryEscape(jobID), nil)
}
func (s *ProjectComputerService) cancelJobs(ctx context.Context, id, jobID string) error {
	conn, err := s.Connection(ctx, id)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"job_id": jobID})
	_, err = s.request(ctx, conn, "POST", "/jobs/cancel", body)
	return err
}

func (s *ProjectComputerService) CancelJob(ctx context.Context, id, jobID string) error {
	if s.taskID == "" || jobID == "" {
		return errors.New("choose a task job")
	}
	gate := s.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	return s.cancelJobs(ctx, id, jobID)
}

// Computer sessions use task-local job IDs, never host process sessions.
type computerProcessInput struct {
	ID         string `json:"session_id"`
	Input      string `json:"input"`
	CloseStdin bool   `json:"close_stdin"`
	Terminate  bool   `json:"terminate"`
	Yield      *int   `json:"yield_time_ms"`
}

func (s *ProjectComputerService) waitComputerSession(ctx context.Context, conn ComputerConnection, id string, yield *int, defaultWait int) (string, bool, error) {
	if id == "" {
		return "", true, errors.New("computer returned no session ID")
	}
	wait := defaultWait
	if yield != nil {
		wait = *yield
	}
	maxWait := 30000
	if defaultWait == 5000 {
		maxWait = 300000
	}
	wait = max(250, min(wait, maxWait))
	deadline := time.Now().Add(time.Duration(wait) * time.Millisecond)
	for {
		data, err := s.request(ctx, conn, "GET", "/jobs?job_id="+url.QueryEscape(id), nil)
		if err != nil {
			return "", true, err
		}
		var response struct {
			Jobs []map[string]any `json:"jobs"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return "", true, err
		}
		var job map[string]any
		for _, candidate := range response.Jobs {
			if candidate["job_id"] == id {
				job = candidate
				break
			}
		}
		if job == nil {
			return "", true, errors.New("computer session not found in this task desktop")
		}
		running, _ := job["running"].(bool)
		if !running || !time.Now().Before(deadline) {
			if running {
				job["session_id"] = id
			}
			out, err := json.Marshal(job)
			exit, _ := job["exit_code"].(float64)
			timedOut, _ := job["timed_out"].(bool)
			return string(out), exit != 0 || timedOut, err
		}
		delay := time.Until(deadline)
		if delay > 100*time.Millisecond {
			delay = 100 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", true, ctx.Err()
		case <-timer.C:
		}
	}
}
