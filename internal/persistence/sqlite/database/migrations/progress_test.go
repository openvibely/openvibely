package migrations

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestMigrationProgressReportsCountsWithoutClaimingCommit(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	advance, finish := migrationProgress("test replay")
	advance()
	advance()
	finish() // Also waits for the logging goroutine to stop.
	for _, want := range []string{"stage=test replay started", "processed=2", "elapsed=", "commit pending"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %s", want, output.String())
		}
	}
}
