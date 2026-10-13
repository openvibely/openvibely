package toolschema

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Bound retention of dynamically supplied schemas. Keys use contents, not
// pointers, so changes to request-scoped schemas cannot reuse stale validators.
const maxCachedValidators = 128
const maxCachedSchemaBytes = 120000

var validators = struct {
	sync.Mutex
	entries map[[32]byte]*jsonschema.Schema
	order   [][32]byte
}{entries: make(map[[32]byte]*jsonschema.Schema)}

func cachedValidator(raw []byte) (*jsonschema.Schema, error) {
	if len(raw) > maxCachedSchemaBytes {
		return compileValidator(raw)
	}
	key := sha256.Sum256(raw)
	validators.Lock()
	defer validators.Unlock()
	if schema := validators.entries[key]; schema != nil {
		return schema, nil
	}
	schema, err := compileValidator(raw)
	if err != nil {
		return nil, err
	}
	if len(validators.order) == maxCachedValidators {
		delete(validators.entries, validators.order[0])
		validators.order = validators.order[1:]
	}
	validators.entries[key] = schema
	validators.order = append(validators.order, key)
	return schema, nil
}

func compileValidator(raw []byte) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	// Never fetch a model/tool supplied URL.
	c.LoadURL = func(string) (io.ReadCloser, error) { return nil, fmt.Errorf("external schema references are disabled") }
	if err := c.AddResource("https://tools.invalid/schema.json", bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return c.Compile("https://tools.invalid/schema.json")
}
