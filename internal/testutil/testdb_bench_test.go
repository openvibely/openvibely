package testutil

import (
	"testing"
)

// BenchmarkNewTestDB measures warm fixture creation and teardown after a
// one-time schema initialization warm-up. It reports ns/op, B/op, and
// allocs/op for creating a fresh, isolated database, exercising it, and closing
// it — the exact per-fixture cost paid by every DB-backed test.
func BenchmarkNewTestDB(b *testing.B) {
	// Warm up: run the one-time migration + serialization so the measured loop
	// only pays the per-fixture clone cost.
	schemaOnce.Do(initSchema)
	if schemaErr != nil {
		b.Fatalf("failed to initialize test schema: %v", schemaErr)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db := buildTestDB(b)
		// Touch seeded data to confirm the fixture is usable, mirroring how
		// real tests immediately query the database.
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM agent_configs WHERE is_default = 1").Scan(&count); err != nil {
			b.Fatalf("query fixture: %v", err)
		}
		db.Close()
	}
}
