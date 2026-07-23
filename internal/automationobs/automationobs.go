// Package automationobs records safe process-local Automation observability.
// It intentionally accepts only caller-selected scalar fields; content-bearing
// prompts, outputs, bodies, credentials, and confirmation tokens must never be
// passed here.
package automationobs

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/applog"
)

// Field is one safe structured log field.
type Field struct {
	Key   string
	Value string
}

var safeFieldKeys = map[string]struct{}{
	"activity_id": {}, "adapter_key": {}, "attempt_id": {}, "attempts": {},
	"automation_id": {}, "confirming_input_id": {}, "created": {}, "dispatch_id": {},
	"execution_id": {}, "invocation_id": {}, "limit": {}, "node_id": {},
	"project_id": {}, "reason": {}, "resource_type": {}, "state": {},
	"status": {}, "thread_id": {}, "version_id": {}, "work_item_id": {},
}

// String records one trimmed scalar field from the fixed non-content key set.
func String(key, value string) Field {
	key = strings.TrimSpace(key)
	if _, ok := safeFieldKeys[key]; !ok {
		return Field{}
	}
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return Field{Key: key, Value: value}
}

// Metric is an in-process count and numeric aggregate for one event.
type Metric struct {
	Count uint64
	Sum   int64
	Max   int64
}

var localMetrics = struct {
	sync.RWMutex
	values map[string]Metric
}{values: map[string]Metric{}}

// Event increments an event counter and emits a safe operational log line.
func Event(name string, fields ...Field) {
	record(name, 1)
	applog.Infof("[automation] event=%s%s", name, formatFields(fields))
}

// DebugEvent increments an event counter and emits only at debug level.
func DebugEvent(name string, fields ...Field) {
	record(name, 1)
	applog.Debugf("[automation] event=%s%s", name, formatFields(fields))
}

// Observe records one numeric sample and emits only at debug level.
func Observe(name string, value int64, fields ...Field) {
	record(name, value)
	applog.Debugf("[automation] metric=%s value=%d%s", name, value, formatFields(fields))
}

func record(name string, value int64) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	localMetrics.Lock()
	metric := localMetrics.values[name]
	metric.Count++
	metric.Sum += value
	if metric.Count == 1 || value > metric.Max {
		metric.Max = value
	}
	localMetrics.values[name] = metric
	localMetrics.Unlock()
}

func formatFields(fields []Field) string {
	values := make([]Field, 0, len(fields))
	for _, field := range fields {
		if field.Key == "" || field.Value == "" {
			continue
		}
		values = append(values, field)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Key < values[j].Key })
	var result strings.Builder
	for _, field := range values {
		result.WriteByte(' ')
		result.WriteString(field.Key)
		result.WriteByte('=')
		result.WriteString(fmt.Sprintf("%q", field.Value))
	}
	return result.String()
}

// Snapshot returns a copy of current process-local Automation metrics.
func Snapshot() map[string]Metric {
	localMetrics.RLock()
	defer localMetrics.RUnlock()
	result := make(map[string]Metric, len(localMetrics.values))
	for name, metric := range localMetrics.values {
		result[name] = metric
	}
	return result
}

// ResetForTest clears process-local metrics. Production code must not call it.
func ResetForTest() {
	localMetrics.Lock()
	localMetrics.values = map[string]Metric{}
	localMetrics.Unlock()
}
