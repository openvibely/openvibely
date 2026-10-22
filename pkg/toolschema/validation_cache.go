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

type validatorCompilation struct {
	done   chan struct{}
	schema *jsonschema.Schema
	err    error
}

var validators = struct {
	sync.Mutex
	entries map[[32]byte]*jsonschema.Schema
	order   [][32]byte
	pending map[[32]byte]*validatorCompilation
}{
	entries: make(map[[32]byte]*jsonschema.Schema),
	pending: make(map[[32]byte]*validatorCompilation),
}

func cachedValidator(raw []byte) (*jsonschema.Schema, error) {
	return cachedValidatorWithCompiler(raw, compileValidator)
}

func cachedValidatorWithCompiler(raw []byte, compile func([]byte) (*jsonschema.Schema, error)) (*jsonschema.Schema, error) {
	if len(raw) > maxCachedSchemaBytes {
		return compile(raw)
	}

	key := sha256.Sum256(raw)
	validators.Lock()
	if schema := validators.entries[key]; schema != nil {
		validators.Unlock()
		return schema, nil
	}
	if pending := validators.pending[key]; pending != nil {
		validators.Unlock()
		<-pending.done
		return pending.schema, pending.err
	}

	pending := &validatorCompilation{done: make(chan struct{})}
	validators.pending[key] = pending
	validators.Unlock()

	schema, err := compile(raw)

	validators.Lock()
	if err == nil {
		if len(validators.order) == maxCachedValidators {
			delete(validators.entries, validators.order[0])
			validators.order = validators.order[1:]
		}
		validators.entries[key] = schema
		validators.order = append(validators.order, key)
	}
	pending.schema = schema
	pending.err = err
	delete(validators.pending, key)
	close(pending.done)
	validators.Unlock()
	return schema, err
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
