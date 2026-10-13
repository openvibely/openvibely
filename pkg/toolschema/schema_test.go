package toolschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictAndNormalizeNestedArguments(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"category":{"type":"string","enum":["backlog"]},"status":{"type":"string","enum":["pending"]},"items":{"type":"array","items":{"type":"object","properties":{"enabled":{"type":"boolean"},"limit":{"type":"integer","minimum":1}},"required":["enabled"],"additionalProperties":false}}},"required":["category"],"additionalProperties":false}`)
	for _, anthropic := range []bool{false, true} {
		wire, cost, err := Strict(schema, anthropic)
		if err != nil {
			t.Fatal(err)
		}
		if cost != 3 {
			t.Fatalf("union cost %d", cost)
		}
		if !strings.Contains(string(wire), `"pending",null`) {
			t.Fatalf("enum missing null: %s", wire)
		}
		if strings.Contains(string(schema), "null") {
			t.Fatal("source mutated")
		}
	}
	for _, input := range []string{`{"category":"backlog","status":null,"items":[{"enabled":false,"limit":null}]}`, `{"category":"backlog","items":[{"enabled":false}]}`} {
		got, err := NormalizeValidate(schema, json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"category":"backlog","items":[{"enabled":false}]}` {
			t.Fatalf("got %s", got)
		}
	}
	for _, input := range []string{`{}`, `{"category":null}`, `{"category":"wrong"}`, `{"category":"backlog","items":[{"enabled":false,"limit":0}]}`, `{"category":"backlog","status":7}`, `{"category":"backlog","extra":true}`, `null`, `{} {}`} {
		if _, err := NormalizeValidate(schema, json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestArbitraryObjectsAndExplicitNullsPreserved(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"metadata":{"type":"object"},"clear":{"type":["string","null"]}}}`)
	if _, _, err := Strict(schema, false); err == nil {
		t.Fatal("arbitrary object converted")
	}
	input := json.RawMessage(`{"metadata":{"x":null,"enabled":false},"clear":null}`)
	got, err := NormalizeValidate(schema, input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"x":null`) || !strings.Contains(string(got), `"clear":null`) {
		t.Fatalf("lost values: %s", got)
	}
}

func TestStrictRejectsUnsupportedSchemas(t *testing.T) {
	for _, schema := range []string{`{"type":"object","additionalProperties":true}`, `{"type":"object","properties":{"x":{"$ref":"https://example.com/schema"}}}`, `{"type":"object","properties":{"x":{}}}`} {
		if _, _, err := Strict(json.RawMessage(schema), false); err == nil {
			t.Fatalf("accepted %s", schema)
		}
	}
}

func TestStrictConstantRemainsRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean","const":true}},"required":["confirm"],"additionalProperties":false}`)
	for _, anthropic := range []bool{false, true} {
		wire, _, err := Strict(schema, anthropic)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NormalizeValidate(wire, json.RawMessage(`{"confirm":false}`)); err == nil {
			t.Fatal("constant weakened")
		}
		if _, err := NormalizeValidate(wire, json.RawMessage(`{"confirm":null}`)); err == nil {
			t.Fatal("required confirmation made nullable")
		}
	}
}

func TestOpenRootSchemaRemainsNonStrict(t *testing.T) {
	for _, anthropic := range []bool{false, true} {
		schema := json.RawMessage(`{"type":"object"}`)
		if _, _, err := Strict(schema, anthropic); err == nil {
			t.Fatal("open root converted to an empty closed object")
		}
		input := json.RawMessage(`{"custom":"value","metadata":{"x":null}}`)
		if _, err := NormalizeValidate(schema, input); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Strict(json.RawMessage(`{"type":"object","additionalProperties":false}`), anthropic); err != nil {
			t.Fatalf("explicitly closed empty object: %v", err)
		}
	}
}

func TestEmptyPropertiesRemainOpen(t *testing.T) {
	for _, anthropic := range []bool{false, true} {
		for _, raw := range []string{`{"type":"object","properties":{}}`, `{"type":"object","properties":{"data":{"type":"object","properties":{}}}}`} {
			if _, _, err := Strict(json.RawMessage(raw), anthropic); err == nil {
				t.Fatalf("converted open object: %s", raw)
			}
		}
		if _, _, err := Strict(json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), anthropic); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStrictPreservesLargeIntegers(t *testing.T) {
	for _, anthropic := range []bool{false, true} {
		for _, constraint := range []string{`"enum":[9007199254740993]`, `"const":9007199254740993`, `"minimum":9007199254740993,"maximum":9007199254740993`} {
			raw := json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer",` + constraint + `}},"required":["id"],"additionalProperties":false}`)
			wire, _, err := Strict(raw, anthropic)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(wire), "9007199254740993") || strings.Contains(string(wire), "9007199254740992") {
				t.Fatalf("number changed: %s", wire)
			}
			for _, schema := range []json.RawMessage{raw, wire} {
				if _, err := NormalizeValidate(schema, json.RawMessage(`{"id":9007199254740993}`)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestDeclaredPropertiesDoNotCloseOpenObjects(t *testing.T) {
	for _, anthropic := range []bool{false, true} {
		for _, raw := range []string{
			`{"type":"object","properties":{"label":{"type":"string"}}}`,
			`{"type":"object","properties":{"data":{"type":"object","properties":{"label":{"type":"string"}}}},"additionalProperties":false}`,
			`{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"}}}}},"additionalProperties":false}`,
		} {
			if _, _, err := Strict(json.RawMessage(raw), anthropic); err == nil {
				t.Fatalf("closed open schema: %s", raw)
			}
		}
	}
}
