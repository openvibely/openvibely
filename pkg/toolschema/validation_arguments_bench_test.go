package toolschema

import (
	"encoding/json"
	"fmt"
	"testing"
)

func BenchmarkValidateArguments(b *testing.B) {
	for _, count := range []int{1, 40, 500} {
		s := &Schema{Type: "object", AdditionalProperties: Pointer(false), Properties: map[string]*Schema{}}
		for i := 0; i < count; i++ {
			s.Properties[fmt.Sprintf("field-%d", i)] = &Schema{Type: "string"}
		}
		raw := JSON(s, nil)
		for _, typed := range []bool{true, false} {
			name := "raw"
			if typed {
				name = "typed"
			}
			b.Run(fmt.Sprintf("%s/%d", name, count), func(b *testing.B) {
				var schema *Schema
				if typed {
					schema = s
				}
				input := json.RawMessage(`{"field-0":"value"}`)
				if _, err := ValidateArguments(schema, raw, input); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := ValidateArguments(schema, raw, input); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
