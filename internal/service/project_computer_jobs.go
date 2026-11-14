package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
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
