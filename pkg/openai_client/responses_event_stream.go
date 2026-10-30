package openaiclient

import (
	"bytes"
	"io"
	"sync"
)

// responsesEvent borrows data until the consumer asks for the next event or
// closes the stream. Decoded complex events own their JSON values.
type responsesEvent struct {
	data      []byte
	typ       string
	websocket bool
}

// responsesEventStream transfers whole WebSocket events with backpressure.
// The acknowledgement prevents reuse of the producer's frame buffer while the
// parser is inspecting it. Read is a compatibility adapter for byte consumers;
// the agentic parser consumes events directly and never adds SSE framing.
type responsesEventStream struct {
	events        chan responsesEvent
	ack           chan struct{}
	closed        chan struct{}
	once          sync.Once
	pending       bool
	err           error // producer-owned until events closes
	compatibility bytes.Buffer
}

func newResponsesEventStream() *responsesEventStream {
	return &responsesEventStream{events: make(chan responsesEvent), ack: make(chan struct{}), closed: make(chan struct{})}
}

func (s *responsesEventStream) send(event responsesEvent) error {
	select {
	case s.events <- event:
	case <-s.closed:
		return io.ErrClosedPipe
	}
	select {
	case <-s.ack:
	case <-s.closed:
	}
	// A delivered event is consumed even when the parser closes after a terminal
	// event, just as a completed pipe Write succeeds before its reader closes.
	return nil
}

func (s *responsesEventStream) next() (responsesEvent, error) {
	if s.pending {
		select {
		case s.ack <- struct{}{}:
		case <-s.closed:
			return responsesEvent{}, io.ErrClosedPipe
		}
		s.pending = false
	}
	select {
	case event, ok := <-s.events:
		if !ok {
			if s.err != nil {
				return responsesEvent{}, s.err
			}
			return responsesEvent{}, io.EOF
		}
		s.pending = true
		return event, nil
	case <-s.closed:
		return responsesEvent{}, io.ErrClosedPipe
	}
}

func (s *responsesEventStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for s.compatibility.Len() == 0 {
		event, err := s.next()
		if err != nil {
			return 0, err
		}
		if _, err = forwardResponsesWebsocketEvent(&s.compatibility, event.data, true); err != nil {
			return 0, err
		}
	}
	return s.compatibility.Read(p)
}

func (s *responsesEventStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// responsesEventIterator strips framing only for actual HTTP/SSE input.
// WebSocket events bypass both the line scanner and the synthetic SSE wrapper.
type responsesEventIterator struct {
	nextEvent func() (responsesEvent, error)
	event     responsesEvent
	err       error
}

func newResponsesEventIterator(body io.Reader) *responsesEventIterator {
	if stream, ok := body.(*responsesEventStream); ok {
		return &responsesEventIterator{nextEvent: stream.next}
	}
	scanner := newResponsesStreamScanner(body)
	currentType := ""
	return &responsesEventIterator{nextEvent: func() (responsesEvent, error) {
		for scanner.Scan() {
			raw := scanner.Bytes()
			line := bytes.TrimSpace(raw)
			if len(line) == 0 {
				currentType = ""
				continue
			}
			if bytes.HasPrefix(line, []byte("event:")) {
				currentType = string(bytes.TrimSpace(line[len("event:"):]))
				continue
			}
			data, isData, err := responsesStreamDataFromLine(raw)
			if err != nil {
				return responsesEvent{}, err
			}
			if !isData || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
				continue
			}
			return responsesEvent{data: data, typ: currentType}, nil
		}
		if err := scanner.Err(); err != nil {
			return responsesEvent{}, err
		}
		return responsesEvent{}, io.EOF
	}}
}

func (s *responsesEventIterator) Scan() bool {
	s.event, s.err = s.nextEvent()
	return s.err == nil
}
func (s *responsesEventIterator) Err() error {
	if s.err == io.EOF {
		return nil
	}
	return s.err
}
