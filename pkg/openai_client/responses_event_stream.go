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
// parser is inspecting it. Both parsers consume events directly without SSE framing.
type responsesEventStream struct {
	events  chan responsesEvent
	ack     chan struct{}
	closed  chan struct{}
	once    sync.Once
	pending bool
	err     error // producer-owned until events closes
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

func (s *responsesEventStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// responsesEventIterator strips framing only for actual HTTP/SSE input.
// WebSocket events bypass both the line scanner and the synthetic SSE wrapper.
type responsesEventIterator struct {
	close     func() error
	nextEvent func() (responsesEvent, error)
	event     responsesEvent
	err       error
}

func newResponsesWebsocketEventIterator(stream *responsesEventStream) *responsesEventIterator {
	return &responsesEventIterator{nextEvent: stream.next, close: stream.Close}
}

func (s *responsesEventIterator) Close() error {
	if s.close != nil {
		return s.close()
	}
	return nil
}

func newResponsesEventIterator(body io.Reader) *responsesEventIterator {
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
