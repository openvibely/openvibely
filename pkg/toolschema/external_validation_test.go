package toolschema

import (
	"encoding/json"
	"testing"
)

func TestExternalUnsupportedValidationPreservesInput(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","pattern":"^(?!admin$)[a-z]+$"},"optional":{"type":"string"}},"required":["code"]}`)
	input := json.RawMessage(`{"code":"alice","optional":null}`)
	got, err := ValidateArguments(nil, schema, input)
	if err != nil || string(got) != string(input) {
		t.Fatalf("fallback = %s, %v", got, err)
	}
	for _, bad := range []string{`{`, `{} {}`, `[]`, `null`} {
		if _, err := ValidateArguments(nil, schema, json.RawMessage(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	typed, err := ParseExternal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateArguments(typed, nil, input); err == nil {
		t.Fatal("built-in schema error ignored")
	}
	supported := json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}},"required":["code"]}`)
	if _, err := ValidateArguments(nil, supported, json.RawMessage(`{"code":7}`)); err == nil {
		t.Fatal("validation failure ignored")
	}
}

func TestExternalNullNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, schema, input, want string
		invalid                   bool
	}{
		{name: "reference", schema: `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","definitions":{"nullable":{"type":["string","null"]}},"properties":{"clear":{"$ref":"#/definitions/nullable","type":"string"}}}`, input: `{"clear":null}`, want: `{"clear":null}`},
		{name: "strict placeholder", schema: `{"type":"object","properties":{"optional":{"type":"string"}},"additionalProperties":false}`, input: `{"optional":null}`, want: `{}`},
		{name: "unsupported composition", schema: `{"type":"object","properties":{"optional":{"type":"string"}},"allOf":[{"required":["optional"]}]}`, input: `{"optional":null}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateArguments(nil, json.RawMessage(tc.schema), json.RawMessage(tc.input))
			if tc.invalid {
				if err == nil {
					t.Fatalf("accepted invalid input: %s", got)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}
