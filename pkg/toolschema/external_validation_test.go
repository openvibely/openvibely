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
