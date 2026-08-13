package stream

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type executionOutputRepo interface {
	GetByID(ctx context.Context, id string) (*models.Execution, error)
	UpdateOutput(ctx context.Context, id string, output string) error
}

type ExecutionStreamPublisher interface {
	Publish(event events.ExecutionStreamEvent)
}

// Writer wraps a bytes.Buffer and periodically flushes accumulated
// output to the database so the UI can display progress in real time.
// It runs a background goroutine that flushes on a timer, ensuring output
// is visible even during long pauses (e.g., while a tool is running).
// Writer is append-only: retry loops that emit live deltas keep failed-attempt
// deltas in the visible/persisted stream unless the caller adds rollback/reset
// behavior before writing retry output.
type Writer struct {
	buf                   bytes.Buffer
	textBuf               bytes.Buffer // text-only content (no thinking blocks, no tool markers)
	mu                    sync.Mutex
	flushMu               sync.Mutex
	execID                string
	taskID                string
	repo                  executionOutputRepo
	ctx                   context.Context
	publisher             ExecutionStreamPublisher
	lastFlush             time.Time
	interval              time.Duration
	dirty                 bool // true when buf has unflushed content
	afterPeriodicSnapshot func(string)
	done                  chan struct{}
	isError               bool   // true if CLI result event had is_error=true
	resultSubtype         string // subtype from CLI result event (e.g. "error_max_turns")
	sessionID             string // CLI session ID for --resume on subsequent calls
}

func NewWriter(execID, taskID string, repo *repository.ExecutionRepo, ctx context.Context, interval time.Duration) *Writer {
	return NewWriterWithPublisher(execID, taskID, repo, ctx, interval, nil)
}

func NewWriterWithPublisher(execID, taskID string, repo *repository.ExecutionRepo, ctx context.Context, interval time.Duration, publisher ExecutionStreamPublisher) *Writer {
	var outputRepo executionOutputRepo
	if repo != nil {
		outputRepo = repo
	}
	return newWriterWithOutputRepo(execID, taskID, outputRepo, ctx, interval, publisher)
}

func newWriterWithOutputRepo(execID, taskID string, repo executionOutputRepo, ctx context.Context, interval time.Duration, publisher ExecutionStreamPublisher) *Writer {
	sw := &Writer{
		execID:    execID,
		taskID:    taskID,
		repo:      repo,
		ctx:       ctx,
		publisher: publisher,
		interval:  interval,
		lastFlush: time.Now(),
		done:      make(chan struct{}),
	}

	// If this execution already has streamed output (for example after a retryable
	// provider failure on the same exec ID), seed the in-memory buffer so later
	// writes/flushes append instead of replacing prior transcript content.
	if repo != nil && execID != "" {
		seedCtx := context.WithoutCancel(ctx)
		exec, err := repo.GetByID(seedCtx, execID)
		if err != nil {
			applog.Infof("[agent-svc] streamingWriter seed load error exec=%s task=%s: %v", execID, taskID, err)
		} else if exec != nil && exec.Output != "" {
			sw.buf.WriteString(exec.Output)
		}
	}

	go sw.periodicFlush()
	return sw
}

// periodicFlush runs in a background goroutine and ensures buffered output
// is flushed to the DB even when no new writes are coming in (e.g., during
// tool execution pauses).
func (w *Writer) periodicFlush() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.flushPeriodicOnce()
		}
	}
}

func (w *Writer) flushPeriodicOnce() {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()

	w.mu.Lock()
	shouldFlush := w.dirty && w.repo != nil && w.execID != ""
	output := ""
	totalLen := 0
	if shouldFlush {
		output = w.buf.String()
		totalLen = w.buf.Len()
		w.dirty = false
		w.lastFlush = time.Now()
	}
	w.mu.Unlock()
	if shouldFlush && w.afterPeriodicSnapshot != nil {
		w.afterPeriodicSnapshot(output)
	}
	if shouldFlush {
		if dbErr := w.repo.UpdateOutput(w.ctx, w.execID, output); dbErr != nil {
			applog.Infof("[agent-svc] streamingWriter periodic flush error exec=%s task=%s: %v", w.execID, w.taskID, dbErr)
		} else {
			applog.Debugf("[agent-svc] streamingWriter periodic flush to DB exec=%s task=%s total_len=%d", w.execID, w.taskID, totalLen)
		}
	}
}

func (w *Writer) Write(p []byte) (int, error) {
	var event *events.ExecutionStreamEvent
	w.mu.Lock()
	n, err := w.buf.Write(p)
	// Raw token content is debug-only; it is noisy at info level and may
	// contain sensitive model output. Operational metadata (flush counts,
	// errors) uses applog.Infof and is always emitted.
	// Uncomment to log raw streamed LLM content when debugging stream issues:
	// applog.Debugf("[agent-svc] streamingWriter received %d bytes exec=%s task=%s: %q", n, w.execID, w.taskID, string(p))
	w.dirty = true
	if n > 0 && w.publisher != nil && w.execID != "" {
		event = &events.ExecutionStreamEvent{
			ExecID: w.execID,
			Type:   events.ExecutionStreamDelta,
			Delta:  string(p[:n]),
			Offset: w.buf.Len(),
		}
	}
	w.mu.Unlock()
	if n > 0 {
		llmcontracts.NotifyActivity(w.ctx)
	}
	if event != nil {
		w.publisher.Publish(*event)
	}
	return n, err
}

// Stop shuts down the background periodic flush goroutine.
func (w *Writer) Stop() {
	close(w.done)
}

// Flush writes the final accumulated output to the database.
// It uses a detached context so the write succeeds even if the
// original context was canceled (e.g., HTTP client disconnect).
func (w *Writer) Flush() {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()

	w.mu.Lock()
	if w.repo == nil || w.execID == "" {
		w.dirty = false
		w.mu.Unlock()
		return
	}
	if w.buf.Len() == 0 {
		applog.Infof("[agent-svc] streamingWriter final flush skipped empty buffer exec=%s task=%s", w.execID, w.taskID)
		w.dirty = false
		w.mu.Unlock()
		return
	}
	output := w.buf.String()
	totalLen := w.buf.Len()
	w.dirty = false
	w.mu.Unlock()

	flushCtx := context.WithoutCancel(w.ctx)
	if dbErr := w.repo.UpdateOutput(flushCtx, w.execID, output); dbErr != nil {
		applog.Infof("[agent-svc] streamingWriter final flush error exec=%s task=%s: %v", w.execID, w.taskID, dbErr)
	} else {
		applog.Infof("[agent-svc] streamingWriter final flush to DB exec=%s task=%s total_len=%d", w.execID, w.taskID, totalLen)
	}
}

func (w *Writer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// WriteText writes to the text-only buffer (for response text, not thinking/tool markers).
func (w *Writer) WriteText(p []byte) {
	w.textBuf.Write(p)
}

// TextString returns only the response text (no thinking blocks, no tool/status markers).
func (w *Writer) TextString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.textBuf.String()
}

func (w *Writer) IsError() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.isError
}

func (w *Writer) ResultSubtype() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.resultSubtype
}

func (w *Writer) SessionID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionID
}

func (w *Writer) setError(subtype string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.isError = true
	w.resultSubtype = subtype
}

// MarkError is the exported form used by canonical event mapping.
func (w *Writer) MarkError(subtype string) {
	w.setError(subtype)
}

// SetSessionID persists the parsed provider session/thread id.
func (w *Writer) SetSessionID(sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessionID = sessionID
}
