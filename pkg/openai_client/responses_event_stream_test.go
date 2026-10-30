package openaiclient

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestResponsesDirectEventsMatchSSE(t *testing.T) {
	for _, ending := range []string{
		`{"type":"response.completed","response":{"id":"r1","model":"test","output":[],"usage":{"input_tokens":12,"output_tokens":4}}}`,
		`{"type":"response.failed","response":{"error":{"message":"failed"}}}`,
		"",
	} {
		t.Run(ending, func(t *testing.T) {
			frames := []string{
				`{"type":"response.output_text.delta","delta":"hello 👋\n"}`,
				`{"type":"response.output_text.delta","delta":"world"}`,
				`{"type":"response.reasoning_text.delta","delta":"thinking"}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call1","name":"test"}}`,
				`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"x\":1}"}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call1","name":"test","arguments":"{\"x\":1}"}}`,
			}
			if ending != "" {
				frames = append(frames, ending)
			}
			var sse strings.Builder
			stream := newResponsesEventStream()
			defer stream.Close()
			for _, frame := range frames {
				fmt.Fprintf(&sse, "data: %s\n\n", frame)
			}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				defer close(stream.events)
				var decoder responsesWebsocketDecoder
				buffer := make([]byte, 4096)
				for _, frame := range frames {
					data := buffer[:copy(buffer, frame)]
					typ, _, valid := decoder.decode(data)
					if !valid {
						panic("invalid fixture")
					}
					if stream.send(responsesEvent{data: data, typ: typ, websocket: true}) != nil {
						return
					}
				}
			}()
			type observed struct {
				result         *agenticTurnResult
				err            string
				text, thinking []string
				tools          []string
			}
			parse := func(events *responsesEventIterator) observed {
				var o observed
				c := NewWithAPIKey("test")
				result, err := c.parseAgenticEventsCapturingText(events, func(s string) { o.text = append(o.text, s) }, func(s string) { o.thinking = append(o.thinking, s) }, func(n string, a json.RawMessage) { o.tools = append(o.tools, n+string(a)) }, nil, nil)
				o.result = result
				if err != nil {
					o.err = err.Error()
				}
				return o
			}
			expected := parse(newResponsesEventIterator(strings.NewReader(sse.String())))
			actual := parse(newResponsesWebsocketEventIterator(stream))
			stream.Close()
			<-finished
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("direct=%#v SSE=%#v", actual, expected)
			}
			if strings.Join(actual.text, "") != "hello 👋\nworld" && actual.err == "" {
				t.Fatal("text lost")
			}
		})
	}
}

func TestResponsesEventStreamBorrowedBufferAndEarlyClose(t *testing.T) {
	stream := newResponsesEventStream()
	defer stream.Close()
	finished := make(chan struct{})
	buffer := []byte("first")
	go func() {
		defer close(finished)
		defer close(stream.events)
		if stream.send(responsesEvent{data: buffer}) != nil {
			return
		}
		copy(buffer, "later")
		_ = stream.send(responsesEvent{data: buffer})
	}()
	first, err := stream.next()
	if err != nil || string(first.data) != "first" {
		t.Fatalf("first=%s err=%v", first.data, err)
	}
	// Producer may not overwrite borrowed memory before the next call/close.
	for i := 0; i < 1000; i++ {
		if string(first.data) != "first" {
			t.Fatal("borrowed data overwritten")
		}
	}
	second, err := stream.next()
	if err != nil || string(second.data) != "later" {
		t.Fatalf("second=%s err=%v", second.data, err)
	}
	stream.Close()
	<-finished
}

func TestResponsesDirectSimpleClientMatchesSSE(t *testing.T) {
	frames := []string{
		`{"type":"response.output_text.delta","delta":"hello 👋\n"}`,
		`{"type":"response.output_text.delta"}`,
		`{"type":"response.output_text.delta","delta":"world"}`,
		`{"type":"response.completed","response":{"id":"r1","model":"test","output":[],"usage":{"input_tokens":12,"output_tokens":4}}}`,
	}
	stream := newResponsesEventStream()
	defer stream.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(stream.events)
		var decoder responsesWebsocketDecoder
		buffer := make([]byte, 4096)
		for _, frame := range frames {
			data := buffer[:copy(buffer, frame)]
			typ, _, _ := decoder.decode(data)
			if stream.send(responsesEvent{data: data, typ: typ, websocket: true}) != nil {
				return
			}
		}
	}()
	var sse, expectedText, actualText strings.Builder
	for _, frame := range frames {
		fmt.Fprintf(&sse, "data: %s\n\n", frame)
	}
	expected, e1 := parseStreamingResponse(strings.NewReader(sse.String()), func(s string) { expectedText.WriteString(s) }, true)
	actual, e2 := parseStreamingEvents(newResponsesWebsocketEventIterator(stream), func(s string) { actualText.WriteString(s) }, true)
	stream.Close()
	<-done
	if e1 != nil || e2 != nil || !reflect.DeepEqual(expected, actual) || expectedText.String() != actualText.String() || actualText.String() != "hello 👋\nworld" {
		t.Fatalf("SSE=%+v/%v direct=%+v/%v text=%q", expected, e1, actual, e2, actualText.String())
	}
}

func TestResponsesCaptureSharesDeliveredTextAcrossFailure(t *testing.T) {
	c := NewWithAPIKey("test")
	var captured, delivered strings.Builder
	captured.WriteString("earlier attempt:")
	failed := `data: {"type":"response.output_text.delta","delta":"hello 👋"}

data: {"type":"response.failed","response":{"error":{"message":"failure"}}}

`
	_, err := c.parseAgenticStreamCapturingText(strings.NewReader(failed), func(s string) { delivered.WriteString(s) }, nil, nil, nil, &captured)
	if err == nil || delivered.String() != "hello 👋" || captured.String() != "earlier attempt:hello 👋" {
		t.Fatalf("failed capture=%q delivered=%q err=%v", captured.String(), delivered.String(), err)
	}
	completed := `data: {"type":"response.output_text.delta","delta":"next"}

data: {"type":"response.completed","response":{"id":"r","output":[]}}

`
	result, err := c.parseAgenticStreamCapturingText(strings.NewReader(completed), nil, nil, nil, nil, &captured)
	if err != nil || result.text != "next" {
		t.Fatalf("completed result=%+v err=%v", result, err)
	}
	captured.WriteString(strings.Repeat("later", 10000))
	if result.text != "next" {
		t.Fatal("returned text changed after append")
	}
}

// responsesFixtureEvents uses the production decoder and acknowledged event
// handoff. Close joins the producer so tests and benchmarks cannot leak work.
func responsesFixtureEvents(frames [][]byte) *responsesEventIterator {
	stream := newResponsesEventStream()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(stream.events)
		var decoder responsesWebsocketDecoder
		for _, frame := range frames {
			typ, _, valid := decoder.decode(frame)
			if !valid {
				stream.err = fmt.Errorf("invalid fixture event: %s", frame)
				return
			}
			if stream.send(responsesEvent{data: frame, typ: typ, websocket: true}) != nil {
				return
			}
		}
	}()
	events := newResponsesWebsocketEventIterator(stream)
	events.close = func() error {
		err := stream.Close()
		<-done
		return err
	}
	return events
}
