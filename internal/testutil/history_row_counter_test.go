package testutil

import "testing"

func TestHistoryReturnedRowCounter(t *testing.T) {
	db, counter := NewStatementCountingTestDB(t)
	read := func() {
		rows, err := db.Query("SELECT 'one' UNION ALL SELECT 'two'")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	counter.Reset()
	counter.SetEnabled(true)
	read()
	if got := counter.ReturnedRows(); got != 2 {
		t.Fatalf("rows=%d, want 2", got)
	}
	counter.SetEnabled(false)
	read()
	if got := counter.ReturnedRows(); got != 2 {
		t.Fatalf("disabled counter changed: %d", got)
	}
	counter.Reset()
	if got := counter.ReturnedRows(); got != 0 {
		t.Fatalf("reset rows=%d", got)
	}
}
