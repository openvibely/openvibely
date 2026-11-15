package service

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/applog"
	"time"
)

func (s *ProjectComputerService) cancelJobs(ctx context.Context, id, jobID string) error {
	conn, err := s.Connection(ctx, id)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"job_id": jobID})
	_, err = s.request(ctx, conn, "POST", "/jobs/cancel", body)
	return err
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
