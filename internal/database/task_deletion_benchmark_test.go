package database

import (
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkTaskDeletionLargeHistory measures the indexed cascade separately
// from the correctness suite. Run with:
//
//	go test ./internal/database -run '^$' -bench '^BenchmarkTaskDeletionLargeHistory$' -benchtime=3x
func BenchmarkTaskDeletionLargeHistory(b *testing.B) {
	b.StopTimer()
	benchmarkDir := b.TempDir()
	for i := 0; i < b.N; i++ {
		db, err := New(filepath.Join(benchmarkDir, fmt.Sprintf("task-delete-%d.db", i)))
		if err != nil {
			b.Fatal(err)
		}
		seedTaskDeletionLargeHistory(b, db)

		b.StartTimer()
		_, err = db.Exec(`DELETE FROM tasks WHERE id = 'delete-parent'`)
		b.StopTimer()
		if err != nil {
			db.Close()
			b.Fatal(err)
		}
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
