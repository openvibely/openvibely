package database

import (
	"github.com/pressly/goose/v3"
	"path/filepath"
	"testing"
)

func TestUpgradeRestartSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	fresh, err := NewReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Upgraded {
		t.Fatal("fresh database must not restart")
	}
	// Put a temporary database into the previous released schema.
	if err := goose.DownTo(fresh.Writer, ".", 219); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := NewReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	if !upgraded.Upgraded {
		t.Fatal("committed upgrade must request restart")
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Upgraded {
		t.Fatal("reopen must not cause a restart loop")
	}
}

func TestFailedUpgradeDoesNotReturnRestartSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failed.db")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := goose.DownTo(db, ".", 219); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE conversation_items (collision INTEGER)"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := initializeSQLiteReport(db)
	if err == nil || upgraded {
		t.Fatalf("upgrade=%t error=%v", upgraded, err)
	}
	version, err := goose.GetDBVersion(db)
	if err != nil || version != 219 {
		t.Fatalf("version=%d error=%v", version, err)
	}
	if _, err := db.Exec("DROP TABLE conversation_items"); err != nil {
		t.Fatal(err)
	}
	upgraded, err = initializeSQLiteReport(db)
	if err != nil || !upgraded {
		t.Fatalf("retry upgrade=%t error=%v", upgraded, err)
	}
}
