package automationobs

import (
	"reflect"
	"strings"
	"testing"
)

func TestStringAllowsOnlyBoundedNonContentFields(t *testing.T) {
	if field := String("prompt", "secret content"); field != (Field{}) {
		t.Fatalf("content-bearing field was accepted: %#v", field)
	}
	field := String("automation_id", strings.Repeat("a", 300))
	if field.Key != "automation_id" || len(field.Value) != 256 {
		t.Fatalf("safe field was not bounded: %#v", field)
	}
}

func TestMetricsRecordSnapshotAndReset(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Event(" dispatch.started ", String("project_id", " project-1 "), String("prompt", "secret"))
	DebugEvent("dispatch.started")
	Observe("dispatch.latency_ms", 12, String("dispatch_id", "dispatch-1"))
	Observe("dispatch.latency_ms", 30)
	Observe(" ", 100)

	got := Snapshot()
	wantStarted := Metric{Count: 2, Sum: 2, Max: 1}
	if !reflect.DeepEqual(got["dispatch.started"], wantStarted) {
		t.Fatalf("dispatch.started metric = %#v, want %#v", got["dispatch.started"], wantStarted)
	}
	wantLatency := Metric{Count: 2, Sum: 42, Max: 30}
	if !reflect.DeepEqual(got["dispatch.latency_ms"], wantLatency) {
		t.Fatalf("dispatch.latency_ms metric = %#v, want %#v", got["dispatch.latency_ms"], wantLatency)
	}

	got["dispatch.started"] = Metric{}
	if fresh := Snapshot()["dispatch.started"]; !reflect.DeepEqual(fresh, wantStarted) {
		t.Fatalf("Snapshot should return a copy, got fresh metric %#v", fresh)
	}

	ResetForTest()
	if len(Snapshot()) != 0 {
		t.Fatalf("ResetForTest did not clear metrics: %#v", Snapshot())
	}
}

func TestFormatFieldsSortsAndSkipsUnsafeEmptyFields(t *testing.T) {
	got := formatFields([]Field{
		String("status", "running"),
		{},
		String("project_id", ""),
		String("automation_id", "auto-1"),
	})
	want := ` automation_id="auto-1" status="running"`
	if got != want {
		t.Fatalf("formatFields() = %q, want %q", got, want)
	}
}
