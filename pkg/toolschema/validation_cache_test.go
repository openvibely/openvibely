package toolschema

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func TestValidatorCacheReuseAndSchemaChanges(t *testing.T) {
	raw := []byte(`{"type":"object","properties":{"value":{"type":"string"}}}`)
	first, err := cachedValidator(raw)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cachedValidator(raw)
			if err != nil || got != first {
				t.Errorf("validator not reused: %v", err)
			}
		}()
	}
	wg.Wait()
	changed := []byte(`{"type":"object","properties":{"value":{"type":"integer"}}}`)
	if _, err := ValidateArguments(nil, changed, json.RawMessage(`{"value":"text"}`)); err == nil {
		t.Fatal("changed schema used stale validator")
	}
	for i := 0; i < maxCachedValidators+1; i++ {
		if _, err := cachedValidator([]byte(fmt.Sprintf(`{"type":"object","title":"cache-test-%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	validators.Lock()
	defer validators.Unlock()
	if len(validators.entries) > maxCachedValidators {
		t.Fatal("cache exceeded bound")
	}
	if validators.entries[sha256.Sum256(raw)] != nil {
		t.Fatal("old entry not evicted")
	}
}
