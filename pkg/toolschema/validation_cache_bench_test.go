package toolschema

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var benchmarkSchemaSequence atomic.Uint64

func BenchmarkValidatorCacheConcurrentColdDistinct(b *testing.B) {
	const workers = 8
	coldTemplate := largeBenchmarkSchema()
	b.ReportAllocs()
	b.ResetTimer()
	for batch := 0; batch < b.N; batch++ {
		var wg sync.WaitGroup
		wg.Add(workers)
		for worker := 0; worker < workers; worker++ {
			raw := []byte(strings.Replace(coldTemplate, "benchmark-cold", fmt.Sprintf("benchmark-cold-%d-%d", benchmarkSchemaSequence.Add(1), worker), 1))
			go func() {
				defer wg.Done()
				if _, err := cachedValidator(raw); err != nil {
					b.Errorf("compile validator: %v", err)
				}
			}()
		}
		wg.Wait()
	}
	b.StopTimer()
	b.ReportMetric(float64(workers)*float64(b.N)*1e9/float64(b.Elapsed().Nanoseconds()), "validations/s")
}

func BenchmarkValidatorCacheConcurrentWarmHits(b *testing.B) {
	const workers = 8
	raw := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"title":"benchmark-warm-shared"}`)
	if _, err := cachedValidator(raw); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for batch := 0; batch < b.N; batch++ {
		var wg sync.WaitGroup
		wg.Add(workers)
		for worker := 0; worker < workers; worker++ {
			go func() {
				defer wg.Done()
				if _, err := cachedValidator(raw); err != nil {
					b.Errorf("lookup validator: %v", err)
				}
			}()
		}
		wg.Wait()
	}
	b.StopTimer()
	b.ReportMetric(float64(workers)*float64(b.N)*1e9/float64(b.Elapsed().Nanoseconds()), "validations/s")
}

func BenchmarkValidatorCacheWarmHitsAlongsideColdMiss(b *testing.B) {
	const warmLookups = 8
	warmRaw := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"title":"benchmark-paired-warm"}`)
	if _, err := cachedValidator(warmRaw); err != nil {
		b.Fatal(err)
	}
	coldTemplate := largeBenchmarkSchema()
	var warmLatencyNanos atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	for batch := 0; batch < b.N; batch++ {
		coldRaw := []byte(strings.Replace(coldTemplate, "benchmark-cold", fmt.Sprintf("benchmark-cold-%d-%d", batch, benchmarkSchemaSequence.Add(1)), 1))
		coldStarted := make(chan struct{})
		coldDone := make(chan error, 1)
		go func() {
			close(coldStarted)
			_, err := cachedValidator(coldRaw)
			coldDone <- err
		}()
		<-coldStarted
		// Let the cold caller enter compilation before measuring cache hits.
		time.Sleep(250 * time.Microsecond)
		var wg sync.WaitGroup
		wg.Add(warmLookups)
		for lookup := 0; lookup < warmLookups; lookup++ {
			go func() {
				defer wg.Done()
				started := time.Now()
				if _, err := cachedValidator(warmRaw); err != nil {
					b.Errorf("lookup warm validator: %v", err)
				}
				warmLatencyNanos.Add(uint64(time.Since(started).Nanoseconds()))
			}()
		}
		wg.Wait()
		if err := <-coldDone; err != nil {
			b.Errorf("compile cold validator: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(warmLatencyNanos.Load())/float64(b.N*warmLookups), "warm-hit-ns/op")
}

func largeBenchmarkSchema() string {
	var builder strings.Builder
	builder.WriteString(`{"type":"object","title":"benchmark-cold","properties":{`)
	for property := 0; property < 500; property++ {
		if property > 0 {
			builder.WriteByte(',')
		}
		fmt.Fprintf(&builder, `"field-%d":{"type":"string"}`, property)
	}
	builder.WriteString(`}}`)
	return builder.String()
}
