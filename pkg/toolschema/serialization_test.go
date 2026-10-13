package toolschema

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestTypedProviderSchemasPreserveHandlerContract(t *testing.T) {
	s := &Schema{Type: "object", AdditionalProperties: Pointer(false), Required: []string{"enabled"}, Properties: map[string]*Schema{
		"enabled":  {Type: "boolean"},
		"category": {Type: "string", Enum: []any{"backlog"}},
		"items": {Type: "array", Items: &Schema{Type: "object", AdditionalProperties: Pointer(false), Required: []string{"name"}, Properties: map[string]*Schema{
			"name": {Type: "string"}, "limit": {Type: "integer", Minimum: Number("1")},
		}}},
	}}
	before := string(JSON(s, nil))
	openai, err := MarshalOpenAI(s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, cost, err := MarshalAnthropic(s, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if cost.Optional != 3 || cost.Unions != 0 {
		t.Fatalf("Anthropic cost: %+v", cost)
	}
	var oa, an map[string]any
	if err := json.Unmarshal(openai, &oa); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(anthropic, &an); err != nil {
		t.Fatal(err)
	}
	if len(oa["required"].([]any)) != 3 || !reflect.DeepEqual(an["required"], []any{"enabled"}) {
		t.Fatal("provider requiredness conflated")
	}
	category := func(m map[string]any) map[string]any {
		return m["properties"].(map[string]any)["category"].(map[string]any)
	}
	if !reflect.DeepEqual(category(oa)["enum"], []any{"backlog", nil}) || !reflect.DeepEqual(category(an)["enum"], []any{"backlog"}) {
		t.Fatal("provider nullability conflated")
	}
	// Validate without normalization to test what each provider can actually emit.
	validate := func(raw json.RawMessage, input string) error {
		var value any
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			return err
		}
		c := newCompilerForTest(t, raw)
		return c.Validate(value)
	}
	if err := validate(anthropic, `{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	if err := validate(openai, `{"enabled":false}`); err == nil {
		t.Fatal("OpenAI accepted omitted optional placeholders")
	}
	if err := validate(openai, `{"enabled":false,"category":null,"items":[{"name":"a","limit":null}]}`); err != nil {
		t.Fatal(err)
	}
	if err := validate(anthropic, `{"enabled":false,"category":null}`); err == nil {
		t.Fatal("Anthropic optional field became nullable")
	}
	if _, err := NormalizeValidate(JSON(s, nil), json.RawMessage(`{"enabled":false,"items":[{"name":"a","limit":0}]}`)); err == nil {
		t.Fatal("handler constraint lost")
	}
	for _, marshal := range []func() (json.RawMessage, error){func() (json.RawMessage, error) { return MarshalOpenAI(s, nil, false) }, func() (json.RawMessage, error) { b, _, e := MarshalAnthropic(s, nil, false); return b, e }} {
		raw, err := marshal()
		if err != nil || string(raw) != before {
			t.Fatalf("non-strict schema changed: %s %v", raw, err)
		}
	}
	if string(JSON(s, nil)) != before {
		t.Fatal("shared definition mutated")
	}
}

func TestExternalUnsupportedSchemaRemainsUnchanged(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"object","properties":{"data":{"type":"object"}},"additionalProperties":false}`),
		json.RawMessage(`{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"null"}]}},"additionalProperties":false}`),
		json.RawMessage(`{"type":"object","properties":{"value":{"$ref":"https://example.invalid/schema"}},"additionalProperties":false}`),
	} {
		before := string(raw)
		if _, err := MarshalOpenAI(nil, raw, true); err == nil {
			t.Fatal("unsupported schema adapted")
		}
		if _, _, err := MarshalAnthropic(nil, raw, true); err == nil {
			t.Fatal("unsupported schema adapted")
		}
		a, err := MarshalOpenAI(nil, raw, false)
		if err != nil || string(a) != before {
			t.Fatal("raw fallback changed")
		}
		b, _, err := MarshalAnthropic(nil, raw, false)
		if err != nil || string(b) != before {
			t.Fatal("raw fallback changed")
		}
	}
}

func TestExternalConstantsPreserved(t *testing.T) {
	for _, constant := range []string{"null", "false", "0", "9007199254740993"} {
		raw := json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer","const":` + constant + `}},"required":["value"],"additionalProperties":false}`)
		for _, anthropic := range []bool{false, true} {
			b, _, err := strictForTest(raw, anthropic)
			if err != nil {
				t.Fatal(err)
			}
			var s struct {
				Properties map[string]struct {
					Enum []json.RawMessage `json:"enum"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(b, &s); err != nil {
				t.Fatal(err)
			}
			if len(s.Properties["value"].Enum) != 1 || string(s.Properties["value"].Enum[0]) != constant {
				t.Fatalf("constant lost: %s", b)
			}
		}
	}
}

func newCompilerForTest(t *testing.T, raw json.RawMessage) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("https://tools.invalid/test", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("https://tools.invalid/test")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
