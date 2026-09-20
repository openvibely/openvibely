package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func benchmarkSeedCustomPersonalities(b *testing.B, repo *repository.CustomPersonalityRepo, n int) {
	b.Helper()
	ctx := context.Background()
	desc := strings.Repeat("desc ", 20)
	prompt := "Benchmark custom personality prompt that is long enough to store."
	for i := 0; i < n; i++ {
		p := &models.CustomPersonality{
			Name:         fmt.Sprintf("Bench %05d", i),
			Key:          fmt.Sprintf("bench_%05d", i),
			Description:  desc,
			SystemPrompt: prompt,
		}
		if err := repo.Create(ctx, p); err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
	}
}

func BenchmarkListPersonalitiesRuntimePage(b *testing.B) {
	for _, n := range []int{0, 25, 500, 5000} {
		b.Run(fmt.Sprintf("custom_%d", n), func(b *testing.B) {
			db := testutil.NewTestDB(b)
			repo := repository.NewCustomPersonalityRepo(db)
			benchmarkSeedCustomPersonalities(b, repo, n)
			ctx := context.Background()
			page, err := ListPersonalitiesRuntimePage(ctx, repo, PersonalityListDefaultLimit, 0)
			if err != nil {
				b.Fatal(err)
			}
			formatted := FormatPersonalityListPage(page, "default", true)
			b.ReportMetric(float64(page.Returned), "returned")
			b.ReportMetric(float64(page.Total), "total")
			b.ReportMetric(float64(len(formatted)), "format_bytes")
			b.ResetTimer()
			var allocs uint64
			for i := 0; i < b.N; i++ {
				page, err = ListPersonalitiesRuntimePage(ctx, repo, PersonalityListDefaultLimit, 0)
				if err != nil {
					b.Fatal(err)
				}
				formatted = FormatPersonalityListPage(page, "default", true)
				allocs += uint64(len(formatted))
			}
			b.ReportMetric(float64(allocs)/float64(b.N), "avg_format_bytes")
			if page.Returned > PersonalityListDefaultLimit {
				b.Fatalf("page not bounded: returned=%d", page.Returned)
			}
			if n >= 500 && len(formatted) > 20*1024 {
				b.Fatalf("formatted output too large for default page: %d bytes", len(formatted))
			}
		})
	}
}
