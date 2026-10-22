package toolschema

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestConcurrentDistinctSchemaMissesCompileInParallel(t *testing.T) {
	raws := [][]byte{
		[]byte(`{"type":"object","properties":{"value":{"type":"string"}},"title":"parallel-string"}`),
		[]byte(`{"type":"object","properties":{"value":{"type":"integer"}},"title":"parallel-integer"}`),
	}
	started := make(chan struct{}, len(raws))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	type result struct {
		schema *jsonschema.Schema
		err    error
	}
	results := make(chan result, len(raws))
	for _, raw := range raws {
		raw := raw
		go func() {
			schema, err := cachedValidatorWithCompiler(raw, func(raw []byte) (*jsonschema.Schema, error) {
				started <- struct{}{}
				<-release
				return compileValidator(raw)
			})
			results <- result{schema: schema, err: err}
		}()
	}
	for range raws {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("distinct schema compiles were serialized")
		}
	}
	unblock()
	for range raws {
		result := <-results
		if result.err != nil || result.schema == nil {
			t.Fatalf("compile distinct schema: %v", result.err)
		}
	}

	if _, err := ValidateArguments(nil, raws[0], json.RawMessage(`{"value":"text"}`)); err != nil {
		t.Fatalf("string schema rejected its value: %v", err)
	}
	if _, err := ValidateArguments(nil, raws[1], json.RawMessage(`{"value":7}`)); err != nil {
		t.Fatalf("integer schema rejected its value: %v", err)
	}
	if _, err := ValidateArguments(nil, raws[0], json.RawMessage(`{"value":7}`)); err == nil {
		t.Fatal("string schema accepted an integer")
	}
	if _, err := ValidateArguments(nil, raws[1], json.RawMessage(`{"value":"text"}`)); err == nil {
		t.Fatal("integer schema accepted a string")
	}
}

func TestConcurrentSameSchemaMissesShareCompilation(t *testing.T) {
	raw := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"title":"same-schema-flight"}`)
	const callers = 16
	var compileCalls atomic.Int32
	compileStarted := make(chan struct{}, callers)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for caller := 0; caller < callers; caller++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_, err := cachedValidatorWithCompiler(raw, func(raw []byte) (*jsonschema.Schema, error) {
				compileCalls.Add(1)
				compileStarted <- struct{}{}
				<-release
				return compileValidator(raw)
			})
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	<-compileStarted
	time.Sleep(10 * time.Millisecond)
	unblock()
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("same-schema lookup failed: %v", err)
		}
	}
	if got := compileCalls.Load(); got != 1 {
		t.Fatalf("same schema compiled %d times, want 1", got)
	}
}

func TestWarmCacheHitDoesNotWaitForColdCompilation(t *testing.T) {
	warmRaw := []byte(`{"type":"object","title":"warm-during-cold"}`)
	if _, err := cachedValidator(warmRaw); err != nil {
		t.Fatal(err)
	}
	coldRaw := []byte(`{"type":"object","title":"cold-during-warm-hit"}`)
	compileStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	coldDone := make(chan error, 1)
	go func() {
		_, err := cachedValidatorWithCompiler(coldRaw, func(raw []byte) (*jsonschema.Schema, error) {
			close(compileStarted)
			<-release
			return compileValidator(raw)
		})
		coldDone <- err
	}()
	<-compileStarted

	warmDone := make(chan error, 1)
	go func() {
		_, err := cachedValidator(warmRaw)
		warmDone <- err
	}()
	select {
	case err := <-warmDone:
		if err != nil {
			t.Fatalf("warm lookup failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("warm cache hit waited for unrelated compilation")
	}
	unblock()
	if err := <-coldDone; err != nil {
		t.Fatalf("cold compile failed: %v", err)
	}
}

func TestFailedSchemaCompilationCanBeRetried(t *testing.T) {
	raw := []byte(`{"type":"object","title":"failed-schema-retry"}`)
	const callers = 8
	wantErr := errors.New("synthetic compilation failure")
	var compileCalls atomic.Int32
	compileStarted := make(chan struct{}, callers)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for caller := 0; caller < callers; caller++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_, err := cachedValidatorWithCompiler(raw, func([]byte) (*jsonschema.Schema, error) {
				compileCalls.Add(1)
				compileStarted <- struct{}{}
				<-release
				return nil, wantErr
			})
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	<-compileStarted
	time.Sleep(10 * time.Millisecond)
	unblock()
	done.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, wantErr) {
			t.Errorf("got error %v, want %v", err, wantErr)
		}
	}
	if got := compileCalls.Load(); got != 1 {
		t.Fatalf("failed schema compiled %d times, want 1", got)
	}

	key := sha256.Sum256(raw)
	validators.Lock()
	cached := validators.entries[key]
	validators.Unlock()
	if cached != nil {
		t.Fatal("failed compilation was cached")
	}
	if _, err := cachedValidator(raw); err != nil {
		t.Fatalf("valid retry failed: %v", err)
	}
}

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
