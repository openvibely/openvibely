package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type inboundReceiptHarness struct {
	name        string
	successKey  string
	rollbackKey string
	timeoutKey  string
	handoff     func(context.Context, string, func(SQLExecutor) error) (bool, error)
	exists      func(context.Context, string) (bool, error)
}

func inboundReceiptHarnesses(db *sql.DB) []inboundReceiptHarness {
	email := NewEmailInboundReceiptRepo(db)
	slack := NewSlackInboundReceiptRepo(db)
	return []inboundReceiptHarness{
		{
			name:        "email",
			successKey:  "message-1",
			rollbackKey: "message-rollback",
			timeoutKey:  "message-timeout",
			handoff: func(ctx context.Context, key string, persist func(SQLExecutor) error) (bool, error) {
				return email.WithHandoff(ctx, "inbox@example.com", key, persist)
			},
			exists: func(ctx context.Context, key string) (bool, error) {
				return email.Exists(ctx, "inbox@example.com", key)
			},
		},
		{
			name:        "slack",
			successKey:  "T1|D1|message-1|U1",
			rollbackKey: "T1|D1|message-rollback|U1",
			timeoutKey:  "T1|D1|timeout|U1",
			handoff: func(ctx context.Context, key string, persist func(SQLExecutor) error) (bool, error) {
				return slack.WithHandoff(ctx, key, persist)
			},
			exists: func(ctx context.Context, key string) (bool, error) {
				return slack.Exists(ctx, key)
			},
		},
	}
}

func TestInboundReceiptHandoffTypedReposShareAtomicIdempotentBehavior(t *testing.T) {
	db := testutil.NewTestDB(t)

	for _, tt := range inboundReceiptHarnesses(db) {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			persistCalls := 0
			already, err := tt.handoff(ctx, tt.successKey, func(exec SQLExecutor) error {
				persistCalls++
				_, err := exec.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, ?)`, fmt.Sprintf("%s-handoff", tt.name), tt.successKey)
				return err
			})
			require.NoError(t, err)
			require.False(t, already)
			require.Equal(t, 1, persistCalls)

			exists, err := tt.exists(ctx, tt.successKey)
			require.NoError(t, err)
			require.True(t, exists)

			already, err = tt.handoff(ctx, tt.successKey, func(SQLExecutor) error {
				persistCalls++
				return nil
			})
			require.NoError(t, err)
			require.True(t, already)
			require.Equal(t, 1, persistCalls)

			already, err = tt.handoff(ctx, tt.rollbackKey, func(SQLExecutor) error {
				persistCalls++
				return errors.New("persist failed")
			})
			require.ErrorContains(t, err, "persist failed")
			require.False(t, already)
			require.Equal(t, 2, persistCalls)

			exists, err = tt.exists(ctx, tt.rollbackKey)
			require.NoError(t, err)
			require.False(t, exists)
		})
	}
}

func TestInboundReceiptHandoffTypedReposRollbackOnContextTimeout(t *testing.T) {
	db := testutil.NewTestDB(t)

	for _, tt := range inboundReceiptHarnesses(db) {
		t.Run(tt.name, func(t *testing.T) {
			baseCtx := context.Background()
			ctx, cancel := context.WithTimeout(baseCtx, 40*time.Millisecond)
			defer cancel()

			started := time.Now()
			already, err := tt.handoff(ctx, tt.timeoutKey, func(SQLExecutor) error {
				<-ctx.Done()
				return ctx.Err()
			})
			require.Error(t, err)
			require.False(t, already)
			require.Less(t, time.Since(started), 250*time.Millisecond, "receipt rollback must honor the handoff context")

			exists, err := tt.exists(baseCtx, tt.timeoutKey)
			require.NoError(t, err)
			require.False(t, exists)
		})
	}
}
