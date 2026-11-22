package geminiclient

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Streamed arguments are typed JSON-path deltas, not fragments of JSON text.
// Only completed calls are exposed to the local tool executor.
type pendingCall struct {
	index   int
	args    any
	strings map[string]bool
}

type callAssembler struct{ pending map[string]*pendingCall }

func (a *callAssembler) add(result *turnResult, part Part) error {
	call := part.FunctionCall
	if a.pending == nil {
		a.pending = make(map[string]*pendingCall)
	}
	key := call.ID
	if key == "" {
		key = "anonymous"
	}
	pending := a.pending[key]
	if pending == nil && len(call.PartialArgs) == 0 && !call.WillContinue {
		result.parts = append(result.parts, part)
		return nil
	}
	if pending == nil {
		pending = &pendingCall{index: len(result.parts), args: map[string]any{}, strings: map[string]bool{}}
		a.pending[key] = pending
		copy := *call
		copy.Args = nil
		copy.PartialArgs = nil
		part.FunctionCall = &copy
		result.parts = append(result.parts, part)
	}
	target := result.parts[pending.index].FunctionCall
	if call.Name != "" {
		if target.Name != "" && target.Name != call.Name {
			return fmt.Errorf("Gemini changed a streamed tool name")
		}
		target.Name = call.Name
	}
	if part.ThoughtSignature != "" {
		result.parts[pending.index].ThoughtSignature = part.ThoughtSignature
	}
	if len(call.Args) > 0 && string(call.Args) != "null" {
		if err := json.Unmarshal(call.Args, &pending.args); err != nil {
			return fmt.Errorf("Gemini tool arguments: %w", err)
		}
	}
	for _, delta := range call.PartialArgs {
		path, err := argumentPath(delta.JSONPath)
		if err != nil {
			return err
		}
		var value any
		count := 0
		if delta.StringValue != nil {
			value = *delta.StringValue
			count++
		}
		if delta.NumberValue != nil {
			value = *delta.NumberValue
			count++
		}
		if delta.BoolValue != nil {
			value = *delta.BoolValue
			count++
		}
		if len(delta.NullValue) > 0 {
			value = nil
			count++
		}
		if count != 1 {
			return fmt.Errorf("Gemini argument delta requires one value")
		}
		pending.args, err = setArgument(pending.args, path, value, pending.strings[delta.JSONPath])
		if err != nil {
			return err
		}
		pending.strings[delta.JSONPath] = delta.WillContinue
	}
	target.WillContinue = call.WillContinue
	if !call.WillContinue {
		for _, continued := range pending.strings {
			if continued {
				return fmt.Errorf("Gemini ended a tool call with incomplete arguments")
			}
		}
		if _, ok := pending.args.(map[string]any); !ok {
			return fmt.Errorf("Gemini tool arguments must be an object")
		}
		raw, err := json.Marshal(pending.args)
		if err != nil {
			return err
		}
		target.Args = raw
		delete(a.pending, key)
	}
	return nil
}

// Accept JSONPath member and index selectors. Reject executable expressions,
// wildcards, negative indices, and unbounded allocation requests.
func argumentPath(path string) ([]any, error) {
	if path == "" || path[0] != '$' {
		return nil, fmt.Errorf("invalid Gemini argument JSON path")
	}
	var out []any
	for rest := path[1:]; rest != ""; {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if end == 0 {
				return nil, fmt.Errorf("empty Gemini argument path member")
			}
			out = append(out, rest[:end])
			rest = rest[end:]
		case '[':
			rest = rest[1:]
			if strings.HasPrefix(rest, "\"") {
				var key string
				dec := json.NewDecoder(strings.NewReader(rest))
				if err := dec.Decode(&key); err != nil {
					return nil, fmt.Errorf("invalid Gemini argument path member")
				}
				rest = rest[dec.InputOffset():]
				if !strings.HasPrefix(rest, "]") {
					return nil, fmt.Errorf("invalid Gemini argument path")
				}
				out = append(out, key)
				rest = rest[1:]
			} else {
				end := strings.IndexByte(rest, ']')
				if end < 0 {
					return nil, fmt.Errorf("invalid Gemini argument index")
				}
				index, err := strconv.Atoi(rest[:end])
				if err != nil || index < 0 || index > 10000 {
					return nil, fmt.Errorf("invalid Gemini argument index")
				}
				out = append(out, index)
				rest = rest[end+1:]
			}
		default:
			return nil, fmt.Errorf("unsupported Gemini argument path")
		}
		if len(out) > 64 {
			return nil, fmt.Errorf("Gemini argument path too deep")
		}
	}
	return out, nil
}

func setArgument(root any, path []any, value any, appendString bool) (any, error) {
	if len(path) == 0 {
		if appendString {
			old, ok := root.(string)
			next, okNext := value.(string)
			if !ok || !okNext {
				return nil, fmt.Errorf("invalid Gemini string continuation")
			}
			return old + next, nil
		}
		return value, nil
	}
	switch key := path[0].(type) {
	case string:
		if root == nil {
			root = map[string]any{}
		}
		obj, ok := root.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Gemini argument path expects object")
		}
		v, err := setArgument(obj[key], path[1:], value, appendString)
		if err != nil {
			return nil, err
		}
		obj[key] = v
		return obj, nil
	case int:
		if root == nil {
			root = []any{}
		}
		array, ok := root.([]any)
		if !ok {
			return nil, fmt.Errorf("Gemini argument path expects array")
		}
		for len(array) <= key {
			array = append(array, nil)
		}
		v, err := setArgument(array[key], path[1:], value, appendString)
		if err != nil {
			return nil, err
		}
		array[key] = v
		return array, nil
	}
	return nil, fmt.Errorf("invalid Gemini argument path")
}
