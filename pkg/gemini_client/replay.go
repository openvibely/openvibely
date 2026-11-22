package geminiclient

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

// DecodeReplayContent retains tool response values as immutable JSON views.
// The regular protocol decoder remains available for callers needing mutable
// messages. Replay clones share views and copy mutable containers.
func DecodeReplayContent(raw json.RawMessage) (Content, error) {
	fields, err := jsonview.FoldedFields(raw)
	if err != nil {
		return Content{}, err
	}
	var result Content
	if role, ok := fields["role"]; ok {
		if err := json.Unmarshal(role, &result.Role); err != nil {
			return Content{}, err
		}
	}
	var parts []json.RawMessage
	if value, ok := fields["parts"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		parts, err = jsonview.Array(value)
		if err != nil {
			return Content{}, err
		}
		result.Parts = make([]Part, len(parts))
	}

	for i, rawPart := range parts {
		var response replayResponse
		var part struct {
			*Part
			Response *replayResponse `json:"functionResponse"`
		}
		part.Part = &result.Parts[i]
		part.Response = &response
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return Content{}, err
		}
		if response.value != nil {
			result.Parts[i].FunctionResponse = response.value
		}
	}
	return result, nil
}

type replayResponse struct{ value *FunctionResponse }

func (r *replayResponse) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	f := &FunctionResponse{}
	err := jsonview.Object(raw, func(key string, value json.RawMessage) error {
		switch strings.ToLower(key) {
		case "name":
			return json.Unmarshal(value, &f.Name)
		case "id":
			return json.Unmarshal(value, &f.ID)
		case "scheduling":
			return json.Unmarshal(value, &f.Scheduling)
		case "parts":
			return json.Unmarshal(value, &f.Parts)
		case "response":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				f.Response = nil
				return nil
			}
			f.Response = map[string]any{}
			return jsonview.Object(value, func(key string, raw json.RawMessage) error {
				view, err := jsonview.Borrow(raw)
				if err == nil {
					f.Response[key] = view
				}
				return err
			})
		}
		return nil
	})
	if err == nil {
		r.value = f
	}
	return err
}
