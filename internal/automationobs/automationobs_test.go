package automationobs

import (
	"strings"
	"testing"
)

func TestStringAllowsOnlyBoundedNonContentFields(t *testing.T) {
	if field := String("prompt", "secret content"); field != (Field{}) {
		t.Fatalf("content-bearing field was accepted: %#v", field)
	}
	field := String("automation_id", strings.Repeat("a", 300))
	if field.Key != "automation_id" || len(field.Value) != 256 {
		t.Fatalf("safe field was not bounded: %#v", field)
	}
}
