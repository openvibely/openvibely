package toolschema

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
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
	done  chan struct{}
	entry *validatorEntry
	err   error
}

var validators = struct {
	sync.Mutex
	entries map[[32]byte]*validatorEntry
	order   [][32]byte
	pending map[[32]byte]*validatorCompilation
}{
	entries: make(map[[32]byte]*validatorEntry),
	pending: make(map[[32]byte]*validatorCompilation),
}

func cachedValidator(raw []byte) (*jsonschema.Schema, error) {
	return cachedValidatorWithCompiler(raw, compileValidator)
}

// validatorEntry owns immutable normalization metadata alongside its validator.
// Metadata is capped per entry as well as by the entry count.
type validatorEntry struct {
	schema               *jsonschema.Schema
	normalization        *normalizationNode
	metadataBytes        int
	parseErr             error
	conversionOnce       sync.Once
	canNormalizeExternal bool
}

// Check strict-conversion support only when external validation needs null
// normalization. The once result is published safely without retaining raw JSON.
func (entry *validatorEntry) supportsNormalization(raw []byte) bool {
	entry.conversionOnce.Do(func() {
		_, err := MarshalOpenAI(nil, raw)
		entry.canNormalizeExternal = err == nil
	})
	return entry.canNormalizeExternal
}

func cachedValidatorWithCompiler(raw []byte, compile func([]byte) (*jsonschema.Schema, error)) (*jsonschema.Schema, error) {
	entry, err := cachedValidation(raw, compile)
	return entry.schema, err
}

func prepareValidation(raw []byte, compile func([]byte) (*jsonschema.Schema, error)) (*validatorEntry, error) {
	entry := &validatorEntry{}
	var parsed map[string]any
	entry.parseErr = json.Unmarshal(raw, &parsed)
	if entry.parseErr != nil {
		return entry, entry.parseErr
	}
	entry.normalization, entry.metadataBytes = prepareNormalization(parsed)
	var err error
	entry.schema, err = compile(raw)
	return entry, err
}

func cachedValidation(raw []byte, compile func([]byte) (*jsonschema.Schema, error)) (*validatorEntry, error) {
	if len(raw) > maxCachedSchemaBytes {
		return prepareValidation(raw, compile)
	}
	key := sha256.Sum256(raw)
	validators.Lock()
	if entry := validators.entries[key]; entry != nil {
		validators.Unlock()
		return entry, nil
	}
	if pending := validators.pending[key]; pending != nil {
		validators.Unlock()
		<-pending.done
		return pending.entry, pending.err
	}
	pending := &validatorCompilation{done: make(chan struct{})}
	validators.pending[key] = pending
	validators.Unlock()
	entry, err := prepareValidation(raw, compile)
	validators.Lock()
	if err == nil && entry.metadataBytes <= maxCachedSchemaBytes {
		if len(validators.order) == maxCachedValidators {
			delete(validators.entries, validators.order[0])
			validators.order = validators.order[1:]
		}
		validators.entries[key] = entry
		validators.order = append(validators.order, key)
	}
	pending.entry = entry
	pending.err = err
	delete(validators.pending, key)
	close(pending.done)
	validators.Unlock()
	return entry, err
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
