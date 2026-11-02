package models

import (
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"reflect"
	"sort"
	"strings"
	"unsafe"
)

// ReplayRows keeps persisted JSON items separate until a consumer decodes them.
// It is immutable and may be shared by copied execution histories.
type ReplayRows struct {
	Format    string
	ItemsKey  string
	NullItems bool
	Envelope  map[string]json.RawMessage
	Items     []json.RawMessage
}

// DecodeTranscript decodes individual stored rows directly into a provider's
// message slice. Unusual/custom JSON targets retain the legacy decoding path.
func (m ExecutionReplayMessage) DecodeTranscript(dst any) error {
	if m.TranscriptJSON != "" || m.Rows == nil {
		return json.Unmarshal([]byte(m.TranscriptJSON), dst)
	}
	r := m.Rows
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("replay decode requires non-nil pointer")
	}
	if _, ok := dst.(json.Unmarshaler); ok {
		return json.Unmarshal([]byte(m.FullTranscriptJSON()), dst)
	}
	target := v.Elem()
	if (r.Format == "array" && target.Kind() == reflect.Struct) || (r.Format == "envelope" && target.Kind() == reflect.Slice && target.Type().Elem().Kind() != reflect.Uint8) {
		return &json.UnmarshalTypeError{Value: r.Format, Type: target.Type()}
	}
	if r.Format == "array" && target.Kind() == reflect.Slice && target.Type().Elem().Kind() != reflect.Uint8 {
		return decodeReplayItems(target, r.Items, r.NullItems)
	}
	if r.Format == "envelope" && target.Kind() == reflect.Struct {
		// Decode metadata through encoding/json to preserve field/tag semantics.
		raw, err := json.Marshal(r.Envelope)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, dst); err != nil {
			return err
		}
		typ := target.Type()
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).Anonymous {
				return json.Unmarshal([]byte(m.FullTranscriptJSON()), dst)
			}
		}
		for i := 0; i < target.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if strings.EqualFold(name, r.ItemsKey) && target.Field(i).CanSet() {
				_, custom := target.Field(i).Addr().Interface().(json.Unmarshaler)
				if custom || target.Field(i).Kind() != reflect.Slice || target.Field(i).Type().Elem().Kind() == reflect.Uint8 {
					return json.Unmarshal([]byte(m.FullTranscriptJSON()), dst)
				}
				return decodeReplayItems(target.Field(i), r.Items, r.NullItems)
			}
		}
		// Unknown envelope fields are ignored just like encoding/json.
		return nil
	}
	return json.Unmarshal([]byte(m.FullTranscriptJSON()), dst)
}

func decodeReplayItems(dst reflect.Value, items []json.RawMessage, null bool) error {
	if null && len(items) == 0 {
		dst.SetZero()
		return nil
	}
	out := reflect.MakeSlice(dst.Type(), len(items), len(items))
	for i, raw := range items {
		if dst.Type().Elem() == reflect.TypeFor[json.RawMessage]() && json.Valid(raw) {
			out.Index(i).Set(reflect.ValueOf(raw))
			continue
		}
		if err := json.Unmarshal(raw, out.Index(i).Addr().Interface()); err != nil {
			return err
		}
	}
	dst.Set(out)
	return nil
}

// FullTranscriptJSON is the compatibility boundary for exports and operations
// that genuinely require serialized provider history, not ordinary restore.
func (m ExecutionReplayMessage) FullTranscriptJSON() string {
	if m.TranscriptJSON != "" || m.Rows == nil {
		return m.TranscriptJSON
	}
	r := m.Rows
	if r.Format == "none" {
		return ""
	}
	if r.Format == "opaque" {
		if len(r.Items) > 0 {
			return string(r.Items[0])
		}
		return ""
	}
	var b strings.Builder
	size := 2
	for _, raw := range r.Items {
		size += len(raw) + 1
	}
	b.Grow(size)
	if r.Format == "envelope" {
		raw, _ := json.Marshal(r.Envelope)
		if len(raw) == 0 || string(raw) == "null" {
			raw = []byte("{}")
		}
		b.Write(raw[:len(raw)-1])
		if len(raw) > 2 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(r.ItemsKey)
		b.Write(key)
		b.WriteByte(':')
	}
	if r.NullItems && len(r.Items) == 0 {
		b.WriteString("null")
	} else {
		b.WriteByte('[')
		for i, raw := range r.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(raw)
		}
		b.WriteByte(']')
	}
	if r.Format == "envelope" {
		b.WriteByte('}')
	}
	return b.String()
}

// MarshalJSON preserves the original wire/storage contract for checkpoint and
// external consumers. Rows never appear as an additional serialized copy.
func (m ExecutionReplayMessage) MarshalJSON() ([]byte, error) {
	type wire struct{ UserContent, AssistantContent, ReasoningContent, TranscriptJSON string }
	return json.Marshal(wire{m.UserContent, m.AssistantContent, m.ReasoningContent, m.FullTranscriptJSON()})
}

// TranscriptBytes counts the compatibility representation without constructing it.
func (m ExecutionReplayMessage) TranscriptBytes() int {
	if m.TranscriptJSON != "" || m.Rows == nil {
		return len(m.TranscriptJSON)
	}
	r := m.Rows
	switch r.Format {
	case "none":
		return 0
	case "opaque":
		if len(r.Items) > 0 {
			return len(r.Items[0])
		}
		return 0
	}
	n := 2
	if r.NullItems && len(r.Items) == 0 {
		n = 4
	} else {
		for i, item := range r.Items {
			n += len(item)
			if i > 0 {
				n++
			}
		}
	}
	if r.Format == "envelope" {
		raw, _ := json.Marshal(r.Envelope)
		if len(raw) == 0 || string(raw) == "null" {
			raw = []byte("{}")
		}
		n += len(raw)
		if len(raw) > 2 {
			n++
		}
		key, _ := json.Marshal(r.ItemsKey)
		n += len(key) + 1
	}
	return n
}

// ReplayReplacesHistory describes the stored format, not the selected provider.
// Responses inputs are full snapshots; messages/arrays are incremental exchanges.
func ReplayReplacesHistory(format, key string, count int) bool {
	return format == "envelope" && key == "responses_input" && count > 0
}

// ReplayArray borrows envelope items for metadata inspection. It never decodes
// message text or images, and serialized replacements take precedence over rows.
func (m ExecutionReplayMessage) ReplayArray(key string) ([]json.RawMessage, error) {
	if m.TranscriptJSON == "" && m.Rows != nil {
		if m.Rows.Format == "envelope" && strings.EqualFold(m.Rows.ItemsKey, key) {
			return m.Rows.Items, nil
		}
		if m.Rows.Format == "array" || m.Rows.Format == "none" {
			return nil, nil
		}
		if m.Rows.Format == "envelope" {
			// Metadata maps serialize in sorted key order; preserve the decoder's
			// last case-insensitive match without serializing unrelated fields.
			var keys []string
			for k := range m.Rows.Envelope {
				if strings.EqualFold(k, key) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			var items []json.RawMessage
			for _, k := range keys {
				var err error
				items, err = jsonview.Array(m.Rows.Envelope[k])
				if err != nil {
					return nil, err
				}
			}
			return items, nil
		}
	}
	raw := m.TranscriptJSON
	if raw == "" {
		raw = m.FullTranscriptJSON()
	}
	// A top-level array cannot contain an envelope field. Full validation
	// remains the responsibility of the consumer that decodes those items.
	if trimmed := strings.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		return nil, nil
	}
	var items []json.RawMessage
	// Object only reads this view; returned slices share immutable string storage.
	err := jsonview.Object(unsafe.Slice(unsafe.StringData(raw), len(raw)), func(k string, v json.RawMessage) error {
		if strings.EqualFold(k, key) {
			var err error
			items, err = jsonview.Array(v)
			return err
		}
		return nil
	})
	return items, err
}

// HasResponsesInput inspects snapshot metadata without decoding content.
func (m ExecutionReplayMessage) HasResponsesInput() bool {
	items, err := m.ReplayArray("responses_input")
	return err == nil && len(items) > 0
}

// EffectiveReplayHistory retains the latest complete snapshot and subsequent
// incremental exchanges. Callers may keep the original execution text for recovery.
func EffectiveReplayHistory(history []Execution) []Execution {
	for i := len(history) - 1; i >= 0; i-- {
		for j := len(history[i].ReplayMessages) - 1; j >= 0; j-- {
			if history[i].ReplayMessages[j].HasResponsesInput() {
				result := append([]Execution(nil), history[i:]...)
				result[0].ReplayMessages = result[0].ReplayMessages[j:]
				return result
			}
		}
	}
	return history
}

// HasTranscript checks presence without counting or assembling provider items.
// A serialized replacement takes precedence over an older row representation.
func (m ExecutionReplayMessage) HasTranscript() bool {
	if m.TranscriptJSON != "" {
		return strings.TrimSpace(m.TranscriptJSON) != ""
	}
	return m.Rows != nil && m.Rows.Format != "none" && (m.Rows.Format != "opaque" || len(m.Rows.Items) != 0)
}
