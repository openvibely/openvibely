package handler

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

type recoveredInputStreamStore struct {
	replayCountingStore
	completeErr error
}

func (s *recoveredInputStreamStore) GetByID(context.Context, string) (*models.Execution, error) {
	return &models.Execution{Status: s.status}, nil
}
func (s *recoveredInputStreamStore) Complete(_ context.Context, _ string, status models.ExecutionStatus, _ string, _ string, _ int, _ int64) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	s.status = status
	return nil
}
func TestRecoveredInputNotifiesLiveStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &recoveredInputStreamStore{replayCountingStore: replayCountingStore{status: models.ExecRunning}}
		h := &Handler{execRepo: store, executionStreamHub: events.NewExecutionStreamHub()}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames := make(chan liveFrame, 10)
		go h.streamExecution(ctx, liveCommand{ExecID: "exec"}, func(f liveFrame) bool { frames <- f; return true })
		synctest.Wait()
		h.completeRecoveredChatInputExecution(ctx, &chatInputRequest{ExecID: "exec"})
		synctest.Wait()

		if store.status != models.ExecFailed {
			t.Fatal("execution was not failed")
		}
		select {
		case f := <-frames:
			if f.Event != "error" {
				t.Fatalf("wanted error, got %s", f.Event)
			}
		default:
			t.Fatal("execution is failed in persistence but live subscriber received no terminal event")
		}
	})
}

func TestRecoveredInputFailedWriteDoesNotNotifyStream(t *testing.T) {
	store := &recoveredInputStreamStore{replayCountingStore: replayCountingStore{status: models.ExecRunning}, completeErr: errors.New("write failed")}
	hub := events.NewExecutionStreamHub()
	sub, unsubscribe, err := hub.Subscribe("exec")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	h := &Handler{execRepo: store, executionStreamHub: hub}
	h.completeRecoveredChatInputExecution(context.Background(), &chatInputRequest{ExecID: "exec"})
	if store.status != models.ExecRunning {
		t.Fatal("failed write changed status")
	}
	select {
	case ev := <-sub:
		t.Fatalf("unexpected notification after failed write: %+v", ev)
	default:
	}
}
