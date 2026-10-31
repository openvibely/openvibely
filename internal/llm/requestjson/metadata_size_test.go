package requestjson

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMetadataSizeMatchesAssemblyAndValidation(t *testing.T) {
	field, err := PrepareRaw("messages", json.RawMessage(`[ {"content":"<>&"} ]`))
	require.NoError(t, err)
	tools, err := PrepareRaw("tools", json.RawMessage(`[ {"name":"tool"} ]`))
	require.NoError(t, err)
	for _, meta := range []any{
		map[string]any{}, map[string]any{"messages": nil, "<key>": "é"},
		Metadata{Value: map[string]any{"model": "model"}, Fields: map[string]*Field{"tools": tools}},
		Metadata{Value: map[string]any{}, Fields: map[string]*Field{"messages": tools}},
		Metadata{Value: map[string]any{}, Fields: map[string]*Field{"other": field}},
		Metadata{Value: map[string]any{}, Fields: map[string]*Field{"tools": nil}},
		Metadata{Value: map[string]any{"tools": nil}, Fields: map[string]*Field{"tools": tools}},
		map[string]any{"messages": []any{}}, nil, []any{},
	} {
		size, serr := field.Size(meta)
		body, err := field.Marshal(meta)
		require.Equal(t, err == nil, serr == nil)
		if err == nil {
			require.Equal(t, len(body), size)
		}
	}
}
