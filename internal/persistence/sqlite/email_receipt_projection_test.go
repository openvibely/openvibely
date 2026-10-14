package sqlite_test

import (
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEmailReceiptBatchQueryShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	receipts := sqlite.NewStores(db, nil).EmailInboundReceipts
	mailbox := "imap.example.com\x00bot@example.com"
	messageKeys := make([]string, 501)
	for i := range messageKeys {
		messageKeys[i] = fmt.Sprintf("message-id:<batch-%d@example.com>", i)
	}
	require.NoError(t, receipts.Record(ctx, mailbox, messageKeys[0]))
	require.NoError(t, receipts.Record(ctx, mailbox, messageKeys[500]))

	counter.Reset()
	counter.SetEnabled(true)
	existing, err := receipts.ExistsBatch(ctx, mailbox, messageKeys)
	counter.SetEnabled(false)

	require.NoError(t, err)
	assert.Len(t, existing, 2)
	assert.Contains(t, existing, messageKeys[0])
	assert.Contains(t, existing, messageKeys[500])
	statements := counter.Statements()
	require.Len(t, statements, 2, "501 keys must use two bounded batch statements")
	for _, statement := range statements {
		assert.Contains(t, statement, "message_key IN")
	}
}
