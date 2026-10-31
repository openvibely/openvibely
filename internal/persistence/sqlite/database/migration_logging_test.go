package database

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestMigrationLoggingAndFailureRecovery(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !strings.Contains(output.String(), "[migration] complete elapsed=") {
		t.Fatalf("missing completion log: %s", output.String())
	}
	if err := goose.DownTo(db, ".", 219); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE conversation_items (collision INTEGER)"); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := initializeSQLite(db); err == nil {
		t.Fatal("expected migration failure")
	}
	if !strings.Contains(output.String(), "[migration] failed elapsed=") || strings.Contains(output.String(), "[migration] complete") {
		t.Fatalf("incorrect failure log: %s", output.String())
	}
	version, err := goose.GetDBVersion(db)
	if err != nil || version != 219 {
		t.Fatalf("version=%d error=%v", version, err)
	}
	if _, err := db.Exec("DROP TABLE conversation_items"); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := initializeSQLite(db); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stage=replay frames", "stage=checkpoints", "stage=execution output", "[migration] complete elapsed="} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q: %s", want, output.String())
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
}
