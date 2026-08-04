package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSlackInboundReceiptHandoffCommitHonorsContextDeadline(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "slack-receipt-deadline.db")
	db, err := database.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=DELETE`)
	require.NoError(t, err)

	reader, err := sql.Open("sqlite", dbPath+"?_loc=UTC")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	reader.SetMaxOpenConns(1)
	_, err = reader.Exec(`PRAGMA journal_mode=DELETE`)
	require.NoError(t, err)

	readTx, err := reader.Begin()
	require.NoError(t, err)
	rows, err := readTx.Query(`SELECT id FROM projects`)
	require.NoError(t, err)
	require.True(t, rows.Next())
	defer rows.Close()
	defer readTx.Rollback()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = NewSlackInboundReceiptRepo(db).WithHandoff(ctx, "T1|D1|commit-deadline|U1", func(SQLExecutor) error {
		return nil
	})
	require.Error(t, err)
	require.Less(t, time.Since(started), 250*time.Millisecond, "receipt COMMIT must not outlive the pre-ACK context")
}

func TestSlackInboundReceiptHandoffRollbackHonorsContextDeadline(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "slack-receipt-rollback-deadline.db")
	db, err := database.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	receipts := NewSlackInboundReceiptRepo(db)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = receipts.WithHandoff(ctx, "T1|D1|rollback-deadline|U1", func(SQLExecutor) error {
		<-ctx.Done()
		return ctx.Err()
	})
	require.Error(t, err)
	require.Less(t, time.Since(started), 250*time.Millisecond, "receipt ROLLBACK must not outlive the pre-ACK context")

	exists, err := receipts.Exists(context.Background(), "T1|D1|rollback-deadline|U1")
	require.NoError(t, err)
	require.False(t, exists)
}
