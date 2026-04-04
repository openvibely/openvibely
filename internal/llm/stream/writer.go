package stream

import (
	"bytes"
	"context"
	"log"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/repository"
)

// Writer wraps a bytes.Buffer and periodically flushes accumulated
// output to the database so the UI can display progress in real time.
// It runs a background goroutine that flushes on a timer, ensuring output
// is visible even during long pauses (e.g., while a tool is running).
type Writer struct {
	buf           bytes.Buffer
	textBuf       bytes.Buffer // text-only content (no thinking blocks, no tool markers)
	mu            sync.Mutex
	execID        string
	taskID        string
	repo          *repository.ExecutionRepo
	ctx           context.Context
	lastFlush     time.Time
	interval      time.Duration
	dirty         bool // true when buf has unflushed content
	done          chan struct{}
	isError       bool   // true if CLI result event had is_error=true
	resultSubtype string // subtype from CLI result event (e.g. "error_max_turns")
	sessionID     string // CLI session ID for --resume on subsequent calls
}

func NewWriter(execID, taskID string, repo *repository.ExecutionRepo, ctx context.Context, interval time.Duration) *Writer {
	sw := &Writer{
		execID:    execID,
		taskID:    taskID,
		repo:      repo,
		ctx:       ctx,
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
			log.Printf("[agent-svc] streamingWriter seed load error exec=%s task=%s: %v", execID, taskID, err)
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
			w.mu.Lock()
			if w.dirty {
				if w.repo != nil && w.execID != "" {
					if dbErr := w.repo.UpdateOutput(w.ctx, w.execID, w.buf.String()); dbErr != nil {
						log.Printf("[agent-svc] streamingWriter periodic flush error exec=%s task=%s: %v", w.execID, w.taskID, dbErr)
					} else {
						log.Printf("[agent-svc] streamingWriter periodic flush to DB exec=%s task=%s total_len=%d", w.execID, w.taskID, w.buf.Len())
					}
				}
				w.dirty = false
				w.lastFlush = time.Now()
			}
			w.mu.Unlock()
		}
	}
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	log.Printf("[agent-svc] streamingWriter received %d bytes exec=%s task=%s: %q", n, w.execID, w.taskID, string(p))
	w.dirty = true
	// Eagerly flush if enough time has passed since last flush
	if time.Since(w.lastFlush) >= w.interval {
		if w.repo != nil && w.execID != "" {
			if dbErr := w.repo.UpdateOutput(w.ctx, w.execID, w.buf.String()); dbErr != nil {
				log.Printf("[agent-svc] streamingWriter flush error exec=%s task=%s: %v", w.execID, w.taskID, dbErr)
			} else {
				log.Printf("[agent-svc] streamingWriter flushed to DB exec=%s task=%s total_len=%d", w.execID, w.taskID, w.buf.Len())
			}
		}
		w.dirty = false
		w.lastFlush = time.Now()
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.repo != nil && w.execID != "" {
		if w.buf.Len() == 0 {
			log.Printf("[agent-svc] streamingWriter final flush skipped empty buffer exec=%s task=%s", w.execID, w.taskID)
			w.dirty = false
			return
		}
		flushCtx := context.WithoutCancel(w.ctx)
		if dbErr := w.repo.UpdateOutput(flushCtx, w.execID, w.buf.String()); dbErr != nil {
			log.Printf("[agent-svc] streamingWriter final flush error exec=%s task=%s: %v", w.execID, w.taskID, dbErr)
		} else {
			log.Printf("[agent-svc] streamingWriter final flush to DB exec=%s task=%s total_len=%d", w.execID, w.taskID, w.buf.Len())
		}
	}
	w.dirty = false
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
