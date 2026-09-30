package openaiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

func TestWebsocketCompactionFailureLeavesSteeringQueued(t *testing.T) {
	var compactions, sampledSteers atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for {
			_, request, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if strings.Contains(string(request), "compaction_trigger") {
				compactions.Add(1)
				// Deliberately omit the required compaction item.
			} else if strings.Contains(string(request), "queued followup") {
				sampledSteers.Add(1)
			}
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"response"}}`))
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"response","status":"completed","usage":{"input_tokens":10000,"output_tokens":1}}}`))
		}
	}))
	defer srv.Close()
	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL
	defer func() { OpenAIAPIBaseURL = original }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pending, claims := true, 0
	opts := &AgenticOptions{
		Model: "gpt-6-astra", MaxTurns: 1, DisableTools: true,
		AutoCompaction: true, CompactionTokenThreshold: 1000,
		HasPendingSteering: func(context.Context) (bool, error) { return pending, nil },
		OnLocalSteering: func(context.Context) (LocalSteeringInput, error) {
			claims++
			pending = false
			return LocalSteeringInput{Text: "queued followup"}, nil
		},
	}
	_, err := NewWithAPIKey("test").SendAgentic(ctx, "hello", opts)
	if err == nil || !strings.Contains(err.Error(), "compaction") {
		t.Fatalf("expected compaction failure, got %v", err)
	}
	if !llmcontracts.ErrorIs(err, llmcontracts.ErrorMidTurnCompactionFailed) {
		t.Fatalf("mid-turn failure must prevent stale service fallback: %v", err)
	}
	if claims != 0 || !pending || compactions.Load() != 1 {
		t.Fatalf("steering consumed before compaction: claims=%d pending=%v compactions=%d", claims, pending, compactions.Load())
	}
	// A separately started turn must still be able to claim the queued steer.
	opts.AutoCompaction = false
	if _, err := NewWithAPIKey("test").SendAgentic(ctx, "retry", opts); err != nil {
		t.Fatal(err)
	}
	if claims != 1 || pending || sampledSteers.Load() != 1 {
		t.Fatalf("retry lost or duplicated steer: claims=%d pending=%v samples=%d", claims, pending, sampledSteers.Load())
	}
}
