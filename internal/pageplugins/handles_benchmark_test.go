package pageplugins

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// Keep unrelated handles live and retire each measured handle outside its latency
// sample so the population stays fixed. Parallel samples include lock contention
// from other creators and their cleanup. Report individual-call percentiles, not
// percentiles of benchmark-run averages.
func BenchmarkIssueHandle(b *testing.B) {
	for _, count := range []int{100, 1000, 5000} {
		for _, parallel := range []bool{false, true} {
			b.Run(fmt.Sprintf("live=%d/parallel=%t", count, parallel), func(b *testing.B) {
				m := NewManager(nil, "")
				r := &Resolved{Instance: models.PagePluginInstance{ID: "page", ProjectID: "project"}}
				for i := 0; i < count; i++ {
					if _, err := m.IssueHandle(r, "user"); err != nil {
						b.Fatal(err)
					}
				}
				b.Cleanup(func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					for token := range m.handles {
						m.retireHandleLocked(token)
					}
				})
				samples := make([]int64, 0, b.N)
				var mu sync.Mutex
				measure := func() int64 {
					start := time.Now()
					h, err := m.IssueHandle(r, "user")
					elapsed := time.Since(start).Nanoseconds()
					if err != nil {
						b.Error(err)
						return elapsed
					}
					m.RetireHandle(h.Token)
					return elapsed
				}
				b.ResetTimer()
				if parallel {
					b.RunParallel(func(pb *testing.PB) {
						local := make([]int64, 0)
						for pb.Next() {
							local = append(local, measure())
						}
						mu.Lock()
						samples = append(samples, local...)
						mu.Unlock()
					})
				} else {
					for i := 0; i < b.N; i++ {
						samples = append(samples, measure())
					}
				}
				b.StopTimer()
				sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
				b.ReportMetric(float64(samples[len(samples)/2]), "p50-ns")
				b.ReportMetric(float64(samples[(len(samples)-1)*95/100]), "p95-ns")
			})
		}
	}
}
