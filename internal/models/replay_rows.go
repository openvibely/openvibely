package models

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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
