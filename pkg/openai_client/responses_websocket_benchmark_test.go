package openaiclient

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type responsesWebsocketBenchmarkFixture struct {
	name   string
	frames [][]byte
}

func responsesWebsocketBenchmarkFixtures() []responsesWebsocketBenchmarkFixture {
	completion := []byte(`{"type":"response.completed","response":{"id":"resp_bench","status":"completed","model":"gpt-5.6-luna","usage":{"input_tokens":128,"output_tokens":256},"output":[]}}`)

	smallDelta := make([][]byte, 256, 257)
	for i := range smallDelta {
		smallDelta[i] = []byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":"token-%03d "}`, i))
	}
	smallDelta = append(smallDelta, completion)

	mixed := make([][]byte, 0, 34)
	for i := 0; i < 32; i++ {
		mixed = append(mixed, []byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":"mixed-%02d "}`, i)))
	}
	mixed = append(mixed,
		[]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_bench","type":"reasoning","summary":[{"type":"summary_text","text":"A representative reasoning summary."}]}}`),
		completion,
	)

	large := [][]byte{
		[]byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, strings.Repeat("larger representative streamed content ", 4096))),
		[]byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, strings.Repeat("more streamed content ", 4096))),
		completion,
	}
	return []responsesWebsocketBenchmarkFixture{
		{name: "small-delta", frames: smallDelta},
		{name: "mixed", frames: mixed},
		{name: "larger-event", frames: large},
	}
}

// BenchmarkResponsesWebsocketFrameToParser measures the network-free WebSocket
// frame decoding/framing and downstream parser path. The fixtures are fixed so
// before/after runs measure identical event sequences.
func BenchmarkResponsesWebsocketFrameToParser(b *testing.B) {
	for _, fixture := range responsesWebsocketBenchmarkFixtures() {
		for _, parser := range []string{"standard", "agentic"} {
			b.Run(fixture.name+"/"+parser, func(b *testing.B) {
				eventsPerSequence := len(fixture.frames)
				b.StopTimer()
				allocsPerSequence := testing.AllocsPerRun(1, func() {
					if err := runResponsesWebsocketBenchmarkSequence(fixture, parser); err != nil {
						b.Fatal(err)
					}
				})
				b.ReportAllocs()
				b.StartTimer()
				for i := 0; i < b.N; i++ {
					if err := runResponsesWebsocketBenchmarkSequence(fixture, parser); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(allocsPerSequence/float64(eventsPerSequence), "allocs/event")
				b.ReportMetric(float64(eventsPerSequence)*float64(b.N)/b.Elapsed().Seconds(), "events/s")
			})
		}
	}
}

func runResponsesWebsocketBenchmarkSequence(fixture responsesWebsocketBenchmarkFixture, parser string) error {
	var framed bytes.Buffer
	var outputItems []any
	responseID := ""
	for _, data := range fixture.frames {
		eventType, event, validJSON := decodeResponsesWebsocketEvent(data)
		switch eventType {
		case "response.output_item.done":
			if item, ok := event["item"].(map[string]any); ok {
				outputItems = append(outputItems, item)
			}
		case "response.completed":
			responseID = responseIDFromEvent(event)
		}
		if _, err := forwardResponsesWebsocketEvent(&framed, data, validJSON); err != nil {
			return err
		}
	}
	if responseID != "resp_bench" {
		return fmt.Errorf("completion response ID = %q, want resp_bench", responseID)
	}
	if fixture.name == "mixed" && len(outputItems) != 1 {
		return fmt.Errorf("captured output item count = %d, want one", len(outputItems))
	}
	switch parser {
	case "standard":
		_, err := parseStreamingResponse(&framed, nil, true)
		return err
	case "agentic":
		client := &Client{}
		_, err := client.parseAgenticStreamWithToolCallbacks(&framed, nil, nil, nil, nil)
		return err
	default:
		return fmt.Errorf("unknown Responses benchmark parser %q", parser)
	}
}
