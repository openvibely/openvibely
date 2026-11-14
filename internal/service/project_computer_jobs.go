package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/applog"
	"net/url"
	"time"
)

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

// Cancellation must not reuse the canceled execution/request context or wait
// for the action lock: an in-flight shell may be waiting for this very signal.
func (s *ProjectComputerService) cancelRemoteExecution(conn ComputerConnection, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"execution_id": id})
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		requestCtx, requestCancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = s.request(requestCtx, conn, "POST", "/executions/cancel", body)
		requestCancel()
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			break
		}
	}
	applog.Infof("[computer] could not cancel execution %s; remote command timeouts remain active: %v", id, err)
}
