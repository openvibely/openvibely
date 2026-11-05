package toolschema

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// Preserve the previous path as a behavioral oracle for output and error text.
func legacyNormalizeValidate(schema, input json.RawMessage, external bool) (json.RawMessage, error) {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing tool arguments")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("tool arguments must be an object")
	}
	compiled, err := cachedValidator(schema)
	if err != nil {
		if external {
			return input, nil
		}
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
	if external {
		// Valid external arguments belong to the tool server. In particular,
		// a draft-07 $ref can override a sibling type and allow explicit null.
		if validationErr := compiled.Validate(value); validationErr == nil {
			return input, nil
		} else if _, conversionErr := MarshalOpenAI(nil, schema); conversionErr != nil {
			// Only schemas supported by strict conversion can contain our
			// nullable placeholders. Do not rewrite other schema dialects.
			return nil, fmt.Errorf("invalid tool arguments: %w", validationErr)
		}
	}
	legacyNormalize(s, value)
	if err = compiled.Validate(value); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	return json.Marshal(value)
}

func legacyNormalize(s map[string]any, value any) {
	switch v := value.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		required := requiredFields(s)
		for name, raw := range props {
			child, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			val, present := v[name]
			if !present {
				continue
			}
			// Only simple non-nullable types are converted; union/ref semantics remain intact.
			kind, _ := child["type"].(string)
			if val == nil && !required[name] && kind != "" && kind != "null" {
				delete(v, name)
				continue
			}
			legacyNormalize(child, val)
		}
	case []any:
		if child, ok := s["items"].(map[string]any); ok {
			for _, item := range v {
				legacyNormalize(child, item)
			}
		}
	}
}

func TestPreparedValidationMatchesPreviousPath(t *testing.T) {
	schemas := []string{
		`{"type":"object","properties":{"optional":{"type":"string"},"needed":{"type":"integer"}},"required":["needed"],"additionalProperties":false}`,
		`{"type":"object","properties":{"nested":{"type":"object","properties":{"optional":{"type":"string"}},"additionalProperties":false},"list":{"type":"array","items":{"type":"object","properties":{"optional":{"type":"string"}},"additionalProperties":false}}},"additionalProperties":false}`,
		`{"type":"object","properties":{"optional":{"type":["string","null"]}}}`,
		`{"type":"object","properties":{"optional":{"type":"string"}},"allOf":[{"required":["optional"]}]}`,
		`{"type":"object","properties":{"optional":{"type":"string","pattern":"^(?!admin$)[a-z]+$"}}}`,
		`{"type":"object","properties":{"optional":{"$ref":"https://tools.invalid/never-fetch.json"}}}`,
		`{"type":"object","properties":{"optional":{"type":"invalid"}}}`,
		`{"type":"object","description":"` + strings.Repeat("x", maxCachedSchemaBytes) + `"}`,
		`{`, `true`, `null`,
	}
	inputs := []string{`{}`, `{"optional":null,"needed":1}`, `{"optional":7,"needed":1}`, `{"needed":null}`, `{"nested":{"optional":null},"list":[{"optional":null},{}]}`, `{"optional":"ok"}`, `{`, `{} {}`, `[]`, `null`}
	for i, raw := range schemas {
		for _, external := range []bool{true, false} {
			for _, input := range inputs {
				want, wantErr := legacyNormalizeValidate(json.RawMessage(raw), json.RawMessage(input), external)
				for repeat := 0; repeat < 2; repeat++ {
					got, err := normalizeValidate(json.RawMessage(raw), json.RawMessage(input), external)
					if string(got) != string(want) || validationErrorCategory(err) != validationErrorCategory(wantErr) {
						t.Fatalf("schema %d external=%v input=%s: got %s, %v; want %s, %v", i, external, input, got, err, want, wantErr)
					}
				}
			}
		}
	}
}

func TestValidationMetadataContentChanges(t *testing.T) {
	for _, typed := range []bool{true, false} {
		s := &Schema{Type: "object", AdditionalProperties: Pointer(false), Properties: map[string]*Schema{"value": {Type: "string"}}}
		validate := func(input string) (json.RawMessage, error) {
			raw := JSON(s, nil)
			if typed {
				return ValidateArguments(s, nil, json.RawMessage(input))
			}
			return ValidateArguments(nil, raw, json.RawMessage(input))
		}
		if got, err := validate(`{"value":null}`); err != nil || string(got) != `{}` {
			t.Fatalf("optional: %s %v", got, err)
		}
		s.Required = []string{"value"}
		if _, err := validate(`{"value":null}`); err == nil {
			t.Fatal("required change ignored")
		}
		s.Properties["value"].Type = "null"
		if got, err := validate(`{"value":null}`); err != nil || string(got) != `{"value":null}` {
			t.Fatalf("type change: %s %v", got, err)
		}
		s.Properties["value"].Type = "integer"
		if _, err := validate(`{"value":"text"}`); err == nil {
			t.Fatal("validation change ignored")
		}
	}
}

func TestConcurrentWarmNormalization(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"nested":{"type":"array","items":{"type":"object","properties":{"optional":{"type":"string"}},"additionalProperties":false}}},"additionalProperties":false}`)
	input := json.RawMessage(`{"nested":[{"optional":null}]}`)
	for _, typed := range []bool{true, false} {
		var s *Schema
		if typed {
			var err error
			s, err = ParseExternal(raw)
			if err != nil {
				t.Fatal(err)
			}
		}
		// Warm the validator without initializing the external conversion check.
		if _, err := ValidateArguments(s, raw, json.RawMessage(`{"nested":[]}`)); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					got, err := ValidateArguments(s, raw, input)
					if err != nil || string(got) != `{"nested":[{}]}` {
						t.Errorf("warm normalization: %s %v", got, err)
						return
					}
				}
			}()
		}
		wg.Wait()
	}
}

func TestValidationMetadataRetentionBounds(t *testing.T) {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}}
	for i := 0; i < 2500; i++ {
		s.Properties[fmt.Sprintf("p%d", i)] = &Schema{Type: "string"}
	}
	raw := JSON(s, nil)
	if len(raw) > maxCachedSchemaBytes {
		t.Fatal("fixture must fit source byte limit")
	}
	entry, err := cachedValidation(raw, compileValidator)
	if err != nil {
		t.Fatal(err)
	}
	if entry.metadataBytes <= maxCachedSchemaBytes {
		t.Fatal("fixture must exceed metadata limit")
	}
	oversized := []byte(`{"type":"object","description":"` + strings.Repeat("x", maxCachedSchemaBytes) + `"}`)
	if _, err := cachedValidation(oversized, compileValidator); err != nil {
		t.Fatal(err)
	}
	validators.Lock()
	defer validators.Unlock()
	for _, raw := range [][]byte{raw, oversized} {
		if validators.entries[sha256.Sum256(raw)] != nil {
			t.Fatal("oversized entry retained")
		}
	}
	total := 0
	for _, entry := range validators.entries {
		if entry.metadataBytes > maxCachedSchemaBytes {
			t.Fatal("metadata bound exceeded")
		}
		total += entry.metadataBytes
	}
	if total > maxCachedValidators*maxCachedSchemaBytes {
		t.Fatal("total metadata bound exceeded")
	}
}

// The validator reports the first failing property in map iteration order.
// Compare the stable error prefix; malformed JSON errors remain exact.
func validationErrorCategory(err error) string {
	text := fmt.Sprint(err)
	if i := strings.Index(text, "jsonschema:"); i >= 0 {
		return text[:i]
	}
	return text
}

func TestPreparedValidationErrorText(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
	for _, input := range []string{`{}`, `{"value":7}`, `{"value":null}`} {
		for _, external := range []bool{true, false} {
			_, want := legacyNormalizeValidate(raw, json.RawMessage(input), external)
			_, got := normalizeValidate(raw, json.RawMessage(input), external)
			if got == nil || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("error changed: got %v, want %v", got, want)
			}
		}
	}
}

func TestValidationMetadataReuseAndInvalidSchemas(t *testing.T) {
	raw := uniqueCacheTestSchema(`{"type":"object","title":"metadata-reuse","properties":{"optional":{"type":"string"}}}`)
	first, err := cachedValidation(raw, compileValidator)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cachedValidation(raw, compileValidator)
	if err != nil || first != second || first.normalization != second.normalization {
		t.Fatalf("metadata not reused: %v", err)
	}
	for _, raw := range [][]byte{[]byte(`{`), []byte(`{"type":"invalid"}`)} {
		if _, err := cachedValidation(raw, compileValidator); err == nil {
			t.Fatal("invalid schema accepted")
		}
		validators.Lock()
		cached := validators.entries[sha256.Sum256(raw)]
		validators.Unlock()
		if cached != nil {
			t.Fatal("invalid schema retained")
		}
	}
}
