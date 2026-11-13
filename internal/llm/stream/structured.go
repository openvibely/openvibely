package stream

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/tooloutput"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/transcript"
)

// StructuredOutputStore seals a display stream without parsing/replacing its
// records. The byte count and digest describe exactly the published stream.
type StructuredOutputStore interface {
	AppendStructuredOutput(context.Context, string, []models.ConversationChange) ([]models.ConversationChange, error)
	StartStructuredOutput(context.Context, string) (int64, error)
	SealStructuredOutput(context.Context, string, []byte, int) error
}

type savedDisplayShape struct {
	textBytes      int
	prefix, suffix string
	complete       bool
}

type displayRecord struct {
	savedShape   savedDisplayShape
	item         transcript.DisplayItem
	id           string
	position     int64
	revision     int64
	version      int
	savedVersion int
	body         strings.Builder
	savedBytes   int
}

type structuredState struct {
	inflightShapes   []savedDisplayShape
	store            StructuredOutputStore
	hash             hash.Hash
	position         int64
	total            int
	pending          []*displayRecord
	active           *displayRecord
	err              error
	inflight         []models.ConversationChange
	inflightRecords  []*displayRecord
	inflightVersions []int
	inflightSizes    []int
	inflightTotal    int
}

// A message may be large, but completed messages must not accumulate throughout
// a run. Threshold flushing applies backpressure if storage cannot keep up.
const structuredFlushBytes = 4 * 1024 * 1024

func (w *Writer) enableStructured(seed string) {
	store, ok := w.repo.(StructuredOutputStore)
	if !ok {
		return
	}
	// Existing raw/legacy output is converted once on resumption, before adding
	// new records. Fresh executions never go through the legacy parser.
	if seed != "" {
		if err := w.repo.FinalizeOutput(context.WithoutCancel(w.ctx), w.execID, seed); err != nil {
			return
		}
	}
	position, err := store.StartStructuredOutput(context.WithoutCancel(w.ctx), w.execID)
	if err != nil {
		return
	}
	h := sha256.New()
	h.Write([]byte(seed))
	w.structured = &structuredState{store: store, hash: h, position: position, total: len(seed)}
	w.buf.Reset()
}

func (s *structuredState) newRecord(item transcript.DisplayItem) *displayRecord {
	r := &displayRecord{item: item, id: "stream:" + persistence.NewID(), position: s.position, version: 1}
	s.position++
	s.pending = append(s.pending, r)
	return r
}

func (s *structuredState) finishActive() {
	if s.active != nil {
		s.active.item.Complete = true
		s.active.version++
		s.active = nil
	}
}

func (s *structuredState) queuedBytes() int {
	n := 0
	for _, r := range s.pending {
		n += max(0, len(r.item.Text)+len(r.item.Prefix)+len(r.item.Suffix)-r.savedBytes)
	}
	return n
}

// eventStructured consumes the same canonical event used by the live display.
// Text is formatted for publication only; it is never parsed for persistence.
func (w *Writer) eventStructured(e Event, skipThinking bool) bool {
	if w.structured == nil {
		return false
	}
	if skipThinking && (e.Type == EventThinkingOpen || e.Type == EventThinkingText || e.Type == EventThinkingEnd) {
		return true
	}
	if e.Type == EventTextOnly {
		w.WriteTextString(e.Text)
		return true
	}
	if e.Type == EventSessionID {
		w.SetSessionID(e.SessionID)
		return true
	}
	if e.Type == EventError {
		if e.IsError {
			w.MarkError()
		}
		return true
	}
	if e.Type == EventRawOutput {
		return false
	} // Legacy raw writers keep the old constructor.
	w.mu.Lock()
	s := w.structured
	var live string
	text := strings.ToValidUTF8(e.Text, "\uFFFD")
	switch e.Type {
	case EventTextDelta:
		if s.active == nil || s.active.item.Type != "text" {
			s.finishActive()
			s.active = s.newRecord(transcript.DisplayItem{Type: "text", Complete: true})
		}
		s.active.appendText(text)
		s.active.version++
		live = text
		w.textBuf.WriteString(text)
	case EventThinkingOpen:
		s.finishActive()
		s.active = s.newRecord(transcript.DisplayItem{Type: "thinking", Prefix: "\n[Thinking]\n"})
		live = s.active.item.Prefix
	case EventThinkingText:
		if s.active == nil || s.active.item.Type != "thinking" {
			// Preserve bytes rather than silently dropping malformed provider events.
			s.finishActive()
			s.active = s.newRecord(transcript.DisplayItem{Type: "text", Complete: true})
		}
		s.active.appendText(text)
		s.active.version++
		live = text
	case EventThinkingEnd:
		live = "\n[/Thinking]\n"
		if s.active != nil && s.active.item.Type == "thinking" {
			s.active.item.Suffix = live
			s.active.item.Complete = true
			s.active.version++
			s.active = nil
		} else {
			s.newRecord(transcript.DisplayItem{Type: "text", Text: live, Complete: true})
		}
	case EventToolUse:
		s.finishActive()
		name, secondary := sanitizeToolMarkerPart(e.ToolName), sanitizeToolMarkerPart(e.Secondary)
		if name != "" {
			live = fmt.Sprintf("\n[Using tool: %s]\n", name)
			if secondary != "" {
				live = fmt.Sprintf("\n[Using tool: %s | %s]\n", name, secondary)
			}
			s.newRecord(transcript.DisplayItem{Type: "using", Name: name, Secondary: secondary, Prefix: live, Complete: true})
		}
	case EventToolResult:
		s.finishActive()
		status := "done"
		if e.IsError {
			status = "error"
		}
		item := transcript.DisplayItem{Type: "tool_result", Name: e.ToolName, Status: status, Prefix: fmt.Sprintf("[Tool %s %s]\n", e.ToolName, status), Text: strings.ToValidUTF8(tooloutput.Encode(e.Output), "\uFFFD"), Suffix: "\n[/Tool]\n", Encoded: true, Complete: true}
		s.newRecord(item)
		live = item.Prefix + item.Text + item.Suffix
	default:
		w.mu.Unlock()
		return false
	}
	live = strings.ToValidUTF8(live, "\uFFFD")
	s.total += len(live)
	s.hash.Write([]byte(live))
	offset := s.total
	backpressure := s.queuedBytes() >= structuredFlushBytes
	w.mu.Unlock()
	if live != "" {
		llmcontracts.NotifyActivity(w.ctx)
		w.publishLive(live, offset)
	}
	if backpressure {
		w.flushStructured(false)
	}
	return true
}

func (w *Writer) publishLive(text string, offset int) {
	if w.publisher != nil && w.execID != "" {
		w.publisher.Publish(events.ExecutionStreamEvent{ExecID: w.execID, Type: events.ExecutionStreamDelta, Delta: text, Offset: offset})
	}
}

// flushStructured snapshots under the mutex, then releases it during I/O so
// live events can continue. flushMu serializes database acknowledgements.
func (w *Writer) flushStructured(final bool) {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	ctx := w.ctx
	if final {
		ctx = context.WithoutCancel(ctx)
	}
	s := w.structured
	for {
		w.mu.Lock()
		if len(s.inflight) == 0 {
			for _, r := range s.pending {
				if r.savedVersion == r.version {
					continue
				}
				operation := models.ConversationPut
				var payload any = r.item
				if r.revision != 0 && r.savedShape.prefix == r.item.Prefix && r.savedShape.suffix == r.item.Suffix && r.savedShape.complete == r.item.Complete {
					delta := r.item.Text[r.savedShape.textBytes:]
					if delta == "" {
						r.savedVersion = r.version
						continue
					}
					operation = models.ConversationAppend
					payload = struct {
						Text string `json:"text"`
					}{delta}
				}
				raw, err := json.Marshal(payload)
				if err != nil {
					s.err = err
					w.mu.Unlock()
					return
				}
				s.inflight = append(s.inflight, models.ConversationChange{ItemID: r.id, Position: r.position, Lane: models.ConversationDisplay, Operation: operation, PreviousRevision: r.revision, MutationID: persistence.NewID(), Payload: raw})
				s.inflightRecords = append(s.inflightRecords, r)
				s.inflightVersions = append(s.inflightVersions, r.version)
				s.inflightShapes = append(s.inflightShapes, savedDisplayShape{textBytes: len(r.item.Text), prefix: r.item.Prefix, suffix: r.item.Suffix, complete: r.item.Complete})
				s.inflightSizes = append(s.inflightSizes, len(r.item.Text)+len(r.item.Prefix)+len(r.item.Suffix))
			}
			s.inflightTotal = s.total
		}
		var pending []*displayRecord
		for _, r := range s.pending {
			if r == s.active || r.savedVersion != r.version {
				pending = append(pending, r)
			}
		}
		s.pending = pending
		hasBatch := len(s.inflight) > 0
		w.mu.Unlock()
		if hasBatch {
			if err := w.persistStructuredBatch(ctx); err != nil {
				w.mu.Lock()
				s.err = err
				w.mu.Unlock()
				return
			}
		}
		w.mu.Lock()
		s.err = nil
		offset := w.persistedOffset
		// Empty streams are valid too.
		if !hasBatch && len(s.pending) == 0 {
			offset = s.total
			w.persistedOffset = offset
		}
		caughtUp := offset == s.total
		for _, r := range s.pending {
			if r.savedVersion != r.version {
				caughtUp = false
				break
			}
		}
		digest := s.hash.Sum(nil)
		w.mu.Unlock()
		w.publishPersisted(offset)
		if !final {
			return
		}
		if !caughtUp {
			continue
		}
		err := s.store.SealStructuredOutput(ctx, w.execID, digest, offset)
		w.mu.Lock()
		s.err = err
		w.mu.Unlock()
		return
	}
}

// Output materializes display text only for legacy consumers that explicitly
// need it. It does not retain that text in the writer or convert storage again.
func (w *Writer) Output() (string, error) {
	if w.structured == nil {
		return w.String(), nil
	}
	w.flushStructured(true)
	w.mu.Lock()
	err := errors.Join(w.structured.err, w.inspectionErr)
	w.mu.Unlock()
	if err != nil {
		return "", err
	}
	execution, err := w.repo.GetByID(context.WithoutCancel(w.ctx), w.execID)
	if err != nil {
		return "", err
	}
	if execution == nil {
		return "", fmt.Errorf("missing execution %s", w.execID)
	}
	return execution.Output, nil
}

func (w *Writer) persistStructuredBatch(ctx context.Context) error {
	s := w.structured
	written, err := s.store.AppendStructuredOutput(ctx, w.execID, s.inflight)
	if err != nil {
		return err
	}
	if len(written) != len(s.inflight) {
		return fmt.Errorf("incomplete structured write acknowledgement")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, r := range s.inflightRecords {
		r.revision = written[i].Sequence
		r.savedVersion = s.inflightVersions[i]
		r.savedBytes = s.inflightSizes[i]
		r.savedShape = s.inflightShapes[i]
	}
	w.persistedOffset = s.inflightTotal
	s.inflight = nil
	s.inflightRecords = nil
	s.inflightVersions = nil
	s.inflightSizes = nil
	s.inflightShapes = nil
	var pending []*displayRecord
	for _, r := range s.pending {
		if r == s.active || r.savedVersion != r.version {
			pending = append(pending, r)
		}
	}
	s.pending = pending
	return nil
}

// Raw fragments (for example a Markdown fence closed before compaction) remain
// literal display text. They are not reparsed into provider messages.
func (w *Writer) writeStructuredRaw(text string) (int, error) {
	text = strings.ToValidUTF8(text, "\uFFFD")
	w.mu.Lock()
	s := w.structured
	if s.active == nil {
		s.active = s.newRecord(transcript.DisplayItem{Type: "text", Complete: true})
	}
	s.active.appendText(text)
	s.active.version++
	s.total += len(text)
	s.hash.Write([]byte(text))
	offset := s.total
	backpressure := s.queuedBytes() >= structuredFlushBytes
	w.mu.Unlock()
	if text != "" {
		llmcontracts.NotifyActivity(w.ctx)
		w.publishLive(text, offset)
	}
	if backpressure {
		w.flushStructured(false)
	}
	return len(text), nil
}

func (r *displayRecord) appendText(text string) {
	if r.body.Len() == 0 && r.item.Text != "" {
		r.body.WriteString(r.item.Text)
	}
	r.body.WriteString(text)
	r.item.Text = r.body.String()
}

// Result closes the current record at the adapter boundary. Output remains
// available for occasional inspection (for example compaction) mid-message.
func (w *Writer) Result() (string, error) {
	if w.structured != nil {
		w.mu.Lock()
		w.structured.finishActive()
		w.mu.Unlock()
	}
	return w.Output()
}

func (w *Writer) writeStructuredCompaction(progress llmcontracts.CompactionProgress) bool {
	if w.structured == nil {
		return false
	}
	if progress.State != "started" && progress.State != "done" && progress.State != "failed" {
		return true
	}
	item := transcript.DisplayItem{Type: "compaction", State: progress.State, Duration: progress.Duration.Milliseconds(), Complete: true}
	live := transcript.DisplayText([]transcript.DisplayItem{item})
	item.Prefix = live
	w.mu.Lock()
	s := w.structured
	s.finishActive()
	s.newRecord(item)
	s.total += len(live)
	s.hash.Write([]byte(live))
	offset := s.total
	w.mu.Unlock()
	w.publishLive(live, offset)
	return true
}

// PersistenceError reports the latest structured save error after Flush.
func (w *Writer) PersistenceError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.structured != nil {
		return errors.Join(w.structured.err, w.inspectionErr)
	}
	return nil
}
