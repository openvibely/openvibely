package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	netmail "net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	messagemail "github.com/emersion/go-message/mail"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailInboundReceiptHandoffIsAtomicAndIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	receipts := repository.NewEmailInboundReceiptRepo(db)
	mailbox, messageKey := "imap.example.com\x00bot@example.com", "message-id:<atomic@example.com>"

	alreadyHandedOff, err := receipts.WithHandoff(ctx, mailbox, messageKey, func(repository.SQLExecutor) error {
		return fmt.Errorf("simulated durable row failure")
	})
	require.Error(t, err)
	assert.False(t, alreadyHandedOff)
	exists, err := receipts.Exists(ctx, mailbox, messageKey)
	require.NoError(t, err)
	assert.False(t, exists, "failed durable work must roll back its receipt")

	persistCalls := 0
	alreadyHandedOff, err = receipts.WithHandoff(ctx, mailbox, messageKey, func(repository.SQLExecutor) error {
		persistCalls++
		return nil
	})
	require.NoError(t, err)
	assert.False(t, alreadyHandedOff)

	alreadyHandedOff, err = receipts.WithHandoff(ctx, mailbox, messageKey, func(repository.SQLExecutor) error {
		persistCalls++
		return nil
	})
	require.NoError(t, err)
	assert.True(t, alreadyHandedOff)
	assert.Equal(t, 1, persistCalls, "a receipt retry must not repeat durable work")
}

type countingEmailInboundReceiptStore struct {
	inner            emailInboundReceiptStore
	existsCalls      int
	recordCalls      int
	withHandoffCalls int
}

func (s *countingEmailInboundReceiptStore) Exists(ctx context.Context, mailboxAddress, messageKey string) (bool, error) {
	s.existsCalls++
	return s.inner.Exists(ctx, mailboxAddress, messageKey)
}

func (s *countingEmailInboundReceiptStore) Record(ctx context.Context, mailboxAddress, messageKey string) error {
	s.recordCalls++
	return s.inner.Record(ctx, mailboxAddress, messageKey)
}

func (s *countingEmailInboundReceiptStore) WithHandoff(ctx context.Context, mailboxAddress, messageKey string, persist func(repository.SQLExecutor) error) (bool, error) {
	s.withHandoffCalls++
	return s.inner.WithHandoff(ctx, mailboxAddress, messageKey, persist)
}

type emailPollReceiptTestHarness struct {
	ctx                  context.Context
	svc                  *EmailService
	project              *models.Project
	agent                *models.LLMConfig
	taskRepo             *repository.TaskRepo
	execRepo             *repository.ExecutionRepo
	threadInputRepo      *repository.ThreadInputRepo
	emailTaskContextRepo *repository.EmailTaskContextRepo
	receipts             *countingEmailInboundReceiptStore
}

func newEmailPollReceiptTestHarness(t *testing.T) *emailPollReceiptTestHarness {
	t.Helper()
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	project := &models.Project{Name: "Email Receipt Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "alice@example.com", project.ID))
	agent := &models.LLMConfig{Name: "Email Receipt Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), NewTaskService(taskRepo, attachmentRepo, nil), llmSvc, nil, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	svc.SetThreadInputRepo(threadInputRepo)
	svc.SetChannelChatRunner(func(context.Context, ChannelChatRunRequest) {})
	receipts := &countingEmailInboundReceiptStore{inner: repository.NewEmailInboundReceiptRepo(db)}
	svc.emailInboundReceiptStore = receipts
	return &emailPollReceiptTestHarness{ctx: ctx, svc: svc, project: project, agent: agent, taskRepo: taskRepo, execRepo: execRepo, threadInputRepo: threadInputRepo, emailTaskContextRepo: emailTaskContextRepo, receipts: receipts}
}

func TestEmailPollOnceUsesPollSnapshotForSelfAddress(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "old@example.com"))

	client := newFakeEmailIMAPClient(
		testIMAPMessageWithBody(1, "self one", "bot@example.com", "first"),
		testIMAPMessageWithBody(2, "self two", "bot@example.com", "second"),
	)
	svc := &EmailService{settingsRepo: settingsRepo}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	counter.Reset()
	counter.SetEnabled(true)
	svc.pollOnce(ctx, EmailRuntimeConfig{Address: " Bot@Example.com ", IMAPHost: "imap.example.com"})
	counter.SetEnabled(false)

	assert.Empty(t, counter.Statements(), "the poll snapshot must avoid per-message settings queries")
	assert.Equal(t, []uint32{1, 2}, client.seenIDs(), "self-sent messages must still be ignored and acknowledged")
}

func TestEmailPollOncePreservesFilteringAndAcknowledgement(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "old@example.com"))
	messageWithHeaders := func(id uint32, subject, envelopeFrom, headerFrom, extraHeaders, body string) *imap.Message {
		msg := testIMAPMessageWithBody(id, subject, envelopeFrom, body)
		msg.Body = testIMAPBodySections(fmt.Sprintf("From: %s\r\nSubject: %s\r\nMessage-ID: <message-%d@example.com>\r\n%sContent-Type: text/plain; charset=utf-8\r\n\r\n%s", headerFrom, subject, id, extraHeaders, body))
		return msg
	}
	client := newFakeEmailIMAPClient(
		messageWithHeaders(1, "self", "bot@example.com", "Display Name <BOT@example.com>", "", "self body"),
		messageWithHeaders(2, "automated", "noreply@example.com", "noreply@example.com", "Auto-Submitted: auto-generated\r\n", "automated body"),
		messageWithHeaders(3, "list", "user@example.com", "user@example.com", "List-Unsubscribe: <mailto:unsubscribe@example.com>\r\n", "list body"),
		messageWithHeaders(4, "bulk", "user@example.com", "user@example.com", "Precedence: bulk\r\n", "bulk body"),
		messageWithHeaders(5, "junk", "user@example.com", "user@example.com", "Precedence: junk\r\n", "junk body"),
		messageWithHeaders(6, "empty", "user@example.com", "user@example.com", "", ""),
		messageWithHeaders(7, "ordinary", "user@example.com", "user@example.com", "", "ordinary body"),
	)
	svc := &EmailService{settingsRepo: settingsRepo}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	counter.Reset()
	counter.SetEnabled(true)
	svc.pollOnce(ctx, EmailRuntimeConfig{Address: " Bot@Example.com ", IMAPHost: "imap.example.com"})
	counter.SetEnabled(false)

	assert.Equal(t, []uint32{1, 2, 3, 4, 5, 6}, client.seenIDs(), "ignored messages must be acknowledged while ordinary messages remain retryable")
	assert.Equal(t, [][]uint32{{1, 2, 3, 4, 5, 6}}, client.storeBatches())
	assert.Empty(t, counter.Statements(), "the poll snapshot must supply the self address without settings reads")
}

func TestEmailPollOnceDoesNotDeduplicateDistinctIdenticalMessagesWithoutMessageID(t *testing.T) {
	db := testutil.NewTestDB(t)
	client := newFakeEmailIMAPClient(
		testIMAPMessageWithoutMessageID(1, 101, "same", "alice@example.com", "identical body"),
		testIMAPMessageWithoutMessageID(2, 102, "same", "alice@example.com", "identical body"),
	)
	client.uidValidity = 77
	processed := 0
	svc := &EmailService{emailInboundReceiptStore: repository.NewEmailInboundReceiptRepo(db)}
	svc.processIncomingMessageFn = func(_ context.Context, _ EmailInboundMessage) bool {
		processed++
		return true
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(context.Background(), EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"})

	assert.Equal(t, 2, processed)
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
}

func TestEmailPollOnceBatchesReceiptRecoveryWithNewHandoff(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}
	receipts := repository.NewEmailInboundReceiptRepo(db)
	mailboxIdentity := emailMailboxIdentity(cfg)
	require.NoError(t, receipts.Record(ctx, mailboxIdentity, "message-id:<message-1@example.com>"))
	client := newFakeEmailIMAPClient(
		testIMAPMessage(1, "already handed off", "alice@example.com"),
		testIMAPMessage(2, "new handoff", "alice@example.com"),
	)
	processed := 0
	svc := &EmailService{emailInboundReceiptStore: receipts}
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool {
		processed++
		return true
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(ctx, cfg)

	assert.Equal(t, 1, processed, "receipt recovery must skip durable work for the first message")
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	assert.Equal(t, 1, client.storeCalls)
	assert.Equal(t, [][]uint32{{1, 2}}, client.storeBatches())
	assert.Equal(t, 1, client.fullBodyFetches)
	assert.Equal(t, [][]uint32{{2}}, client.fullBodyFetchIDs, "already-receipted messages must be excluded from the full fetch")
}

func TestEmailPollOnceUsesMIMEMessageIDBeforeEnvelopeAndUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}
	receipts := repository.NewEmailInboundReceiptRepo(db)
	mailboxIdentity := emailMailboxIdentity(cfg)
	require.NoError(t, receipts.Record(ctx, mailboxIdentity, "message-id:<message-1@example.com>"))
	message := testIMAPMessageWithBody(1, "already handed off", "alice@example.com", "body")
	message.Envelope.MessageId = "<envelope-identity@example.com>"
	client := newFakeEmailIMAPClient(message)
	client.uidValidity = 77
	processed := 0
	svc := &EmailService{emailInboundReceiptStore: receipts}
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool {
		processed++
		return true
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(ctx, cfg)

	assert.Zero(t, processed)
	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Equal(t, 1, client.fetchCount)
	assert.Equal(t, [][]uint32{{1}}, client.fetchIDs)
	assert.Equal(t, [][]imap.FetchItem{{imap.FetchEnvelope, imap.FetchUid, emailMessageIDHeaderSection().FetchItem()}}, client.fetchItems)
	assert.Zero(t, client.fullBodyFetches, "MIME Message-ID from bounded headers must deduplicate before BODY[]")
}

func TestEmailPollOnceUsesMIMEMessageIDAfterUIDMetadataFallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}
	receipts := &countingEmailInboundReceiptStore{inner: repository.NewEmailInboundReceiptRepo(db)}
	mailboxIdentity := emailMailboxIdentity(cfg)
	require.NoError(t, receipts.Record(ctx, mailboxIdentity, "message-id:<message-1@example.com>"))
	receipts.recordCalls = 0
	message := testIMAPMessageWithBody(1, "MIME identity", "alice@example.com", "body")
	message.Envelope.MessageId = ""
	message.Uid = 101
	client := newFakeEmailIMAPClient(message)
	client.uidValidity = 77
	client.metadataMessageIDOmissions = map[uint32]bool{1: true}
	processed := 0
	svc := &EmailService{emailInboundReceiptStore: receipts}
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool {
		processed++
		return true
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(ctx, cfg)

	assert.Zero(t, processed, "a MIME Message-ID discovered after UID fallback must still deduplicate")
	assert.Equal(t, 2, receipts.existsCalls, "the final MIME key must be checked after the UID fallback")
	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Zero(t, receipts.recordCalls, "receipt recovery must not write a UID-keyed duplicate receipt")
}
func TestEmailPollOnceRecordsMIMEMessageIDAfterUIDMetadataFallback(t *testing.T) {
	h := newEmailPollReceiptTestHarness(t)
	message := testIMAPMessageWithBody(1, "first", "alice@example.com", "start a new chat")
	message.Envelope.MessageId = ""
	message.Uid = 101
	client := newFakeEmailIMAPClient(message)
	client.uidValidity = 77
	client.metadataMessageIDOmissions = map[uint32]bool{1: true}
	client.storeFailures = 1
	h.svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}

	h.svc.pollOnce(h.ctx, cfg)

	assert.Empty(t, client.seenIDs(), "store failure leaves the canonical handoff unread for acknowledgement retry")
	assert.Equal(t, 2, h.receipts.existsCalls, "the first poll checks the provisional UID and then the canonical MIME key")
	assert.Equal(t, 1, h.receipts.withHandoffCalls)
	assert.Zero(t, h.receipts.recordCalls, "a successful WithHandoff must not use the provisional UID Record path")
	assert.Equal(t, [][]uint32{{1}}, client.fullBodyFetchIDs, "the canonical key must be discovered from the full MIME fetch after metadata falls back to UID")
	canonicalKey := "message-id:<message-1@example.com>"
	provisionalKey := "imap-uid:77:101"
	canonicalExists, err := h.receipts.inner.Exists(h.ctx, emailMailboxIdentity(cfg), canonicalKey)
	require.NoError(t, err)
	assert.True(t, canonicalExists, "the durable handoff must be recorded under the MIME Message-ID")
	provisionalExists, err := h.receipts.inner.Exists(h.ctx, emailMailboxIdentity(cfg), provisionalKey)
	require.NoError(t, err)
	assert.False(t, provisionalExists, "the provisional UID must not receive the durable handoff receipt")
	tasks, err := h.taskRepo.ListByProject(h.ctx, h.project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	h.svc.pollOnce(h.ctx, cfg)

	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Equal(t, 4, h.receipts.existsCalls, "receipt recovery checks the same provisional and canonical identities")
	assert.Equal(t, 1, h.receipts.withHandoffCalls, "receipt recovery must not repeat the durable first-turn handoff")
	assert.Zero(t, h.receipts.recordCalls)
	tasks, err = h.taskRepo.ListByProject(h.ctx, h.project.ID, "")
	require.NoError(t, err)
	assert.Len(t, tasks, 1)
}

func TestEmailPollOnceSkipsReceiptedLargeMIMEBodyBeforeParsing(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}
	receipts := &countingEmailInboundReceiptStore{inner: repository.NewEmailInboundReceiptRepo(db)}
	mailboxIdentity := emailMailboxIdentity(cfg)
	require.NoError(t, receipts.Record(ctx, mailboxIdentity, "message-id:<message-1@example.com>"))
	receipts.recordCalls = 0
	message := testIMAPMessageWithMIME(1, "large receipt", "alice@example.com", strings.Repeat("x", 256*1024), bytes.Repeat([]byte("a"), 16*1024))
	client := newFakeEmailIMAPClient(message)
	processed := 0
	svc := &EmailService{emailInboundReceiptStore: receipts}
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool {
		processed++
		return true
	}
	svc.parseIMAPMessageFn = func(msg *imap.Message, section *imap.BodySectionName, skipAttachments bool) (EmailInboundMessage, error) {
		client.parseAttempts++
		return parseIMAPMessage(msg, section, skipAttachments)
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(ctx, cfg)

	assert.Zero(t, processed)
	assert.Zero(t, client.fullBodyFetches, "a receipted large MIME message must not request BODY[]")
	assert.Zero(t, client.fullBodyBytes)
	assert.Zero(t, client.parseAttempts)
	assert.Zero(t, receipts.recordCalls)
	assert.Equal(t, []uint32{1}, client.seenIDs())
}

func TestEmailPollOncePreservesUnresolvedMIMEAttachmentAndOrdering(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}
	receipts := repository.NewEmailInboundReceiptRepo(db)
	mailboxIdentity := emailMailboxIdentity(cfg)
	require.NoError(t, receipts.Record(ctx, mailboxIdentity, "message-id:<message-1@example.com>"))
	client := newFakeEmailIMAPClient(
		testIMAPMessage(1, "already handled", "alice@example.com"),
		testIMAPMessageWithMIME(2, "new MIME", "alice@example.com", "new body", []byte("attachment payload")),
		testIMAPMessageWithBody(3, "new plain", "alice@example.com", "plain body"),
	)
	processed := make([]EmailInboundMessage, 0, 2)
	svc := &EmailService{emailInboundReceiptStore: receipts}
	svc.processIncomingMessageFn = func(_ context.Context, msg EmailInboundMessage) bool {
		processed = append(processed, msg)
		return true
	}
	svc.parseIMAPMessageFn = func(msg *imap.Message, section *imap.BodySectionName, skipAttachments bool) (EmailInboundMessage, error) {
		client.parseAttempts++
		return parseIMAPMessage(msg, section, skipAttachments)
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(ctx, cfg)

	require.Len(t, processed, 2)
	assert.Equal(t, []string{"new MIME", "new plain"}, []string{processed[0].Subject, processed[1].Subject})
	assert.Equal(t, "new body", processed[0].Body)
	require.Len(t, processed[0].Attachments, 1)
	assert.Equal(t, "payload-2.bin", processed[0].Attachments[0].FileName)
	assert.Equal(t, "application/octet-stream", processed[0].Attachments[0].ContentType)
	assert.Equal(t, []byte("attachment payload"), processed[0].Attachments[0].Data)
	assert.Equal(t, [][]uint32{{2, 3}}, client.fullBodyFetchIDs)
	assert.Equal(t, int64(2), client.parseAttempts)
	assert.Equal(t, []uint32{1, 2, 3}, client.seenIDs())
	assert.Equal(t, [][]uint32{{1, 2, 3}}, client.storeBatches())
}
func TestEmailPollOnceDoesNotRepeatDurableHandoffAfterStoreFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	client := newFakeEmailIMAPClient(
		testIMAPMessage(1, "durable-one", "alice@example.com"),
		testIMAPMessage(2, "durable-two", "alice@example.com"),
	)
	client.storeFailures = 1
	receipts := repository.NewEmailInboundReceiptRepo(db)
	processed := 0
	newService := func() *EmailService {
		svc := &EmailService{emailInboundReceiptStore: receipts}
		svc.processIncomingMessageFn = func(_ context.Context, _ EmailInboundMessage) bool {
			processed++
			return true
		}
		svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
		return svc
	}
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}

	newService().pollOnce(context.Background(), cfg)
	require.Empty(t, client.seenIDs())
	require.Equal(t, 2, processed)
	assert.Equal(t, [][]uint32{{1, 2}}, client.storeBatches())

	newService().pollOnce(context.Background(), cfg)
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	assert.Equal(t, 2, processed)
	assert.Equal(t, [][]uint32{{1, 2}, {1, 2}}, client.storeBatches())
	assert.Equal(t, 1, client.fullBodyFetches, "receipt recovery must not fetch full MIME bodies again")
}

func TestEmailPollOnceSkipsPostSuccessRecordAfterFirstTurnWithHandoff(t *testing.T) {
	h := newEmailPollReceiptTestHarness(t)
	client := newFakeEmailIMAPClient(testIMAPMessageWithBody(1, "first", "alice@example.com", "start a new chat"))
	client.storeFailures = 1
	h.svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}

	h.svc.pollOnce(h.ctx, cfg)

	assert.Empty(t, client.seenIDs(), "store failure leaves the handed-off message unread for acknowledgement retry")
	assert.Equal(t, 1, h.receipts.existsCalls)
	assert.Equal(t, 1, h.receipts.withHandoffCalls)
	assert.Zero(t, h.receipts.recordCalls, "successful WithHandoff must not be followed by a redundant Record")
	tasks, err := h.taskRepo.ListByProject(h.ctx, h.project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	h.svc.pollOnce(h.ctx, cfg)

	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Equal(t, 2, h.receipts.existsCalls)
	assert.Equal(t, 1, h.receipts.withHandoffCalls, "receipt recovery must not repeat durable first-turn work")
	assert.Zero(t, h.receipts.recordCalls)
	tasks, err = h.taskRepo.ListByProject(h.ctx, h.project.ID, "")
	require.NoError(t, err)
	assert.Len(t, tasks, 1)
}

func TestEmailPollOnceOptimizedSnapshotPreservesAuthorizedHandoffAndReceiptRecovery(t *testing.T) {
	h := newEmailPollReceiptTestHarness(t)
	require.NoError(t, h.svc.settingsRepo.Set(h.ctx, EmailSettingAddress, "old@example.com"))
	client := newFakeEmailIMAPClient(
		testIMAPMessageWithBody(1, "self", "BOT@example.com", "self body"),
		testIMAPMessageWithBody(2, "authorized", "alice@example.com", "start a new chat"),
	)
	client.storeFailures = 1
	h.svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
	cfg := EmailRuntimeConfig{Address: " Bot@Example.COM ", IMAPHost: "imap.example.com"}

	// Keep the hook unset so pollOnce takes the normalized snapshot branch for
	// both the self-filter and the authorized durable handoff.
	require.Nil(t, h.svc.processIncomingMessageFn)
	h.svc.pollOnce(h.ctx, cfg)

	assert.Empty(t, client.seenIDs(), "STORE failure must leave both handled messages unread for retry")
	assert.Equal(t, 1, h.receipts.withHandoffCalls, "the authorized message must use the durable handoff path")
	assert.Equal(t, 1, h.receipts.recordCalls, "the self-filtered message must record its receipt")
	tasks, err := h.taskRepo.ListByProject(h.ctx, h.project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	h.svc.pollOnce(h.ctx, cfg)

	assert.Equal(t, []uint32{1, 2}, client.seenIDs(), "receipt recovery must acknowledge both messages together")
	assert.Equal(t, 4, h.receipts.existsCalls, "receipt recovery must check both canonical message identities")
	assert.Equal(t, 1, h.receipts.withHandoffCalls, "receipt recovery must not repeat durable handoff")
	assert.Equal(t, 1, h.receipts.recordCalls, "receipt recovery must not repeat the self-message receipt")
	tasks, err = h.taskRepo.ListByProject(h.ctx, h.project.ID, "")
	require.NoError(t, err)
	assert.Len(t, tasks, 1, "receipt recovery must not create a duplicate task")
}

func TestEmailPollOnceSkipsPostSuccessRecordAfterQueuedWithHandoff(t *testing.T) {
	h := newEmailPollReceiptTestHarness(t)
	rootMessageID := "<queue-root@example.com>"
	sessionKey := EmailSessionKey("alice@example.com", rootMessageID, "", "", "Queue thread")
	agentID := h.agent.ID
	activeTask := &models.Task{ProjectID: h.project.ID, Title: "Queue thread", Prompt: "root", Category: models.CategoryChat, Status: models.StatusRunning, CreatedVia: models.TaskOriginEmail, AgentID: &agentID}
	require.NoError(t, h.taskRepo.Create(h.ctx, activeTask))
	require.NoError(t, h.emailTaskContextRepo.Upsert(h.ctx, &models.EmailTaskContext{TaskID: activeTask.ID, EmailFrom: "alice@example.com", EmailMessageID: rootMessageID, EmailSubject: "Queue thread", EmailSessionKey: sessionKey}))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: h.agent.ID, Status: models.ExecRunning, PromptSent: "root"}
	require.NoError(t, h.execRepo.Create(h.ctx, activeExec))
	client := newFakeEmailIMAPClient(testIMAPReplyWithBody(1, "Queue thread", "alice@example.com", rootMessageID, "queued follow-up"))
	h.svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	h.svc.pollOnce(h.ctx, EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"})

	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Equal(t, 1, h.receipts.existsCalls)
	assert.Equal(t, 1, h.receipts.withHandoffCalls)
	assert.Zero(t, h.receipts.recordCalls, "successful queued WithHandoff must not be followed by a redundant Record")
	inputs, err := h.threadInputRepo.ListPendingForChat(h.ctx, h.project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Contains(t, inputs[0].Content, "queued follow-up")
}

func TestEmailPollOnceMixedBatchRetriesRealTaskCreationFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	projects, err := projectRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, projects)
	project := &projects[0]
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	agent := &models.LLMConfig{Name: "Email Poll Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"))
	require.NoError(t, func() error {
		_, err := db.Exec(`CREATE TRIGGER fail_email_task_insert BEFORE INSERT ON tasks
			WHEN NEW.prompt LIKE '%transient task failure%'
			BEGIN SELECT RAISE(FAIL, 'transient task failure'); END`)
		return err
	}())

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	taskSvc := NewTaskService(taskRepo, attachmentRepo, nil)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, nil, emailAuthRepo, emailTaskContextRepo)
	svc.SetChannelChatRunner(func(context.Context, ChannelChatRunRequest) {})
	svc.SetEmailInboundReceiptRepo(repository.NewEmailInboundReceiptRepo(db))
	client := newFakeEmailIMAPClient(
		testIMAPMessageWithBody(1, "successful", "alice@example.com", "normal request"),
		testIMAPMessageWithBody(2, "retry", "alice@example.com", "transient task failure"),
	)
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
	cfg := EmailRuntimeConfig{Address: "bot@example.com"}

	svc.pollOnce(ctx, cfg)
	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Equal(t, [][]uint32{{1}}, client.storeBatches())
	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Contains(t, tasks[0].Prompt, "normal request")

	_, err = db.Exec(`DROP TRIGGER fail_email_task_insert`)
	require.NoError(t, err)
	client.setMessage(testIMAPMessageWithBody(2, "retry", "alice@example.com", "transient task failure"))
	svc.pollOnce(ctx, cfg)
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	assert.Equal(t, [][]uint32{{1}, {2}}, client.storeBatches())
	tasks, err = taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	assert.Len(t, tasks, 2)
}

func TestEmailPollOnceMixedBatchRetriesRealQueueWriteFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	projects, err := projectRepo.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, projects)
	project := &projects[0]
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	agent := &models.LLMConfig{Name: "Email Queue Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"))

	rootMessageID := "<queue-root@example.com>"
	sessionKey := EmailSessionKey("alice@example.com", rootMessageID, "", "", "Queue thread")
	activeTask := &models.Task{ProjectID: project.ID, Title: "Queue thread", Prompt: "root", Category: models.CategoryChat, Status: models.StatusRunning, CreatedVia: models.TaskOriginEmail, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "root"}
	require.NoError(t, execRepo.Create(ctx, activeExec))
	require.NoError(t, emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: activeTask.ID, EmailFrom: "alice@example.com", EmailMessageID: rootMessageID, EmailSubject: "Queue thread", EmailSessionKey: sessionKey}))
	require.NoError(t, func() error {
		_, err := db.Exec(`CREATE TRIGGER fail_email_queue_insert BEFORE INSERT ON thread_inputs
			WHEN NEW.content LIKE '%transient queue failure%'
			BEGIN SELECT RAISE(FAIL, 'transient queue failure'); END`)
		return err
	}())

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), NewTaskService(taskRepo, attachmentRepo, nil), llmSvc, nil, emailAuthRepo, emailTaskContextRepo)
	svc.SetThreadInputRepo(threadInputRepo)
	svc.SetChannelChatRunner(func(context.Context, ChannelChatRunRequest) {})
	svc.SetEmailInboundReceiptRepo(repository.NewEmailInboundReceiptRepo(db))
	client := newFakeEmailIMAPClient(
		testIMAPReplyWithBody(1, "Queue thread", "alice@example.com", rootMessageID, "normal queued request"),
		testIMAPReplyWithBody(2, "Queue thread", "alice@example.com", rootMessageID, "transient queue failure"),
	)
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
	cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}

	svc.pollOnce(ctx, cfg)
	assert.Equal(t, []uint32{1}, client.seenIDs())
	inputs, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	assert.Contains(t, inputs[0].Content, "normal queued request")

	_, err = db.Exec(`DROP TRIGGER fail_email_queue_insert`)
	require.NoError(t, err)
	client.setMessage(testIMAPReplyWithBody(2, "Queue thread", "alice@example.com", rootMessageID, "transient queue failure"))
	svc.pollOnce(ctx, cfg)
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	assert.Equal(t, [][]uint32{{1}, {2}}, client.storeBatches())
	inputs, err = threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 2)
}

func TestEmailPollOnceBatchesAcknowledgements(t *testing.T) {
	client := newFakeEmailIMAPClient(
		testIMAPMessage(1, "first", "alice@example.com"),
		testIMAPMessage(2, "second", "alice@example.com"),
	)
	svc := &EmailService{}
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool {
		return true
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(context.Background(), EmailRuntimeConfig{})

	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	assert.Equal(t, 1, client.storeCalls)
	assert.Equal(t, [][]uint32{{1, 2}}, client.storeBatches())
}

func TestEmailPollOnceAcknowledgesOnlySuccessfulMessagesAndRetriesFailures(t *testing.T) {
	client := newFakeEmailIMAPClient(
		testIMAPMessage(1, "success", "alice@example.com"),
		testIMAPMessage(2, "retry", "alice@example.com"),
	)
	svc := &EmailService{}
	attempts := map[string]int{}
	svc.processIncomingMessageFn = func(_ context.Context, msg EmailInboundMessage) bool {
		attempts[msg.Subject]++
		return msg.Subject == "success" || attempts[msg.Subject] > 1
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(context.Background(), EmailRuntimeConfig{})
	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Equal(t, [][]uint32{{1}}, client.storeBatches())
	assert.Equal(t, map[string]int{"success": 1, "retry": 1}, attempts)

	svc.pollOnce(context.Background(), EmailRuntimeConfig{})
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	assert.Equal(t, [][]uint32{{1}, {2}}, client.storeBatches())
	assert.Equal(t, map[string]int{"success": 1, "retry": 2}, attempts)
}

func BenchmarkEmailAcknowledgements(b *testing.B) {
	for _, messageCount := range []int{10, 100, 1000} {
		for _, batched := range []bool{false, true} {
			name := "PerMessage"
			if batched {
				name = "Batched"
			}
			b.Run(fmt.Sprintf("%s/%d", name, messageCount), func(b *testing.B) {
				client := newFakeEmailIMAPClient()
				client.storeDelay = 100 * time.Microsecond
				ids := make([]uint32, messageCount)
				for i := range ids {
					ids[i] = uint32(i + 1)
				}

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if batched {
						if err := storeSeen(client, ids); err != nil {
							b.Fatal(err)
						}
						continue
					}
					for _, id := range ids {
						if err := storeSeen(client, []uint32{id}); err != nil {
							b.Fatal(err)
						}
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(client.storeCalls)/float64(b.N), "store_calls/op")
			})
		}
	}
}

type benchmarkEmailInboundReceiptStore struct {
	received         map[string]struct{}
	existsCalls      int
	recordCalls      int
	withHandoffCalls int
}

func (s *benchmarkEmailInboundReceiptStore) Exists(_ context.Context, _, messageKey string) (bool, error) {
	s.existsCalls++
	_, ok := s.received[messageKey]
	return ok, nil
}

func (s *benchmarkEmailInboundReceiptStore) Record(_ context.Context, _, _ string) error {
	s.recordCalls++
	return nil
}

func (s *benchmarkEmailInboundReceiptStore) WithHandoff(_ context.Context, _, _ string, _ func(repository.SQLExecutor) error) (bool, error) {
	s.withHandoffCalls++
	return false, nil
}

func newEmailPollBenchmarkFixture(messageCount, receiptedCount int) (*fakeEmailIMAPClient, *benchmarkEmailInboundReceiptStore, *EmailService) {
	return newEmailPollBenchmarkFixtureWithMIME(messageCount, receiptedCount, 4*1024, 0)
}

func newEmailPollBenchmarkFixtureWithMIME(messageCount, receiptedCount, bodySize, attachmentSize int) (*fakeEmailIMAPClient, *benchmarkEmailInboundReceiptStore, *EmailService) {
	messages := make([]*imap.Message, 0, messageCount)
	received := make(map[string]struct{}, receiptedCount)
	body := strings.Repeat("x", bodySize)
	attachment := bytes.Repeat([]byte("a"), attachmentSize)
	for i := 0; i < messageCount; i++ {
		id := uint32(i + 1)
		message := testIMAPMessageWithMIME(id, fmt.Sprintf("message-%d", id), "alice@example.com", body, attachment)
		if attachmentSize == 0 {
			message = testIMAPMessageWithBody(id, fmt.Sprintf("message-%d", id), "alice@example.com", body)
		}
		messages = append(messages, message)
		if i < receiptedCount {
			received[fmt.Sprintf("message-id:<message-%d@example.com>", id)] = struct{}{}
		}
	}
	client := newFakeEmailIMAPClient(messages...)
	client.uidValidity = 77
	receipts := &benchmarkEmailInboundReceiptStore{received: received}
	svc := &EmailService{emailInboundReceiptStore: receipts}
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool { return true }
	svc.parseIMAPMessageFn = func(msg *imap.Message, section *imap.BodySectionName, skipAttachments bool) (EmailInboundMessage, error) {
		client.parseAttempts++
		inbound, err := parseIMAPMessage(msg, section, skipAttachments)
		if err == nil {
			client.parsedMessages++
		}
		return inbound, err
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
	return client, receipts, svc
}

func benchmarkEmailPollOnce(b *testing.B, messageCount, receiptedRate, bodySize, attachmentSize int) {
	b.Helper()
	b.ReportAllocs()
	var fetchCalls, fetchIDs, fetchItems, fullBodyFetches int
	var fullBodyBytes, parseAttempts, parsedMessages, receiptLookups, storeCalls int64
	var elapsed time.Duration
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		receiptedCount := messageCount * receiptedRate / 100
		client, receipts, svc := newEmailPollBenchmarkFixtureWithMIME(messageCount, receiptedCount, bodySize, attachmentSize)
		cfg := EmailRuntimeConfig{Address: "bot@example.com", IMAPHost: "imap.example.com"}
		b.StartTimer()
		started := time.Now()
		svc.pollOnce(context.Background(), cfg)
		elapsed += time.Since(started)
		b.StopTimer()

		fetchCalls += len(client.fetchItems)
		for _, ids := range client.fetchIDs {
			fetchIDs += len(ids)
		}
		for _, items := range client.fetchItems {
			fetchItems += len(items)
		}
		fullBodyFetches += client.fullBodyFetches
		fullBodyBytes += client.fullBodyBytes
		parseAttempts += client.parseAttempts
		parsedMessages += client.parsedMessages
		receiptLookups += int64(receipts.existsCalls)
		storeCalls += int64(client.storeCalls)
	}

	b.ReportMetric(float64(fetchCalls)/float64(b.N), "fetch_calls/op")
	b.ReportMetric(float64(fetchIDs)/float64(b.N), "fetch_ids/op")
	b.ReportMetric(float64(fetchItems)/float64(b.N), "fetch_items/op")
	b.ReportMetric(float64(fetchCalls+int(storeCalls))/float64(b.N), "imap_commands/op")
	b.ReportMetric(float64(fullBodyFetches)/float64(b.N), "full_body_fetches/op")
	b.ReportMetric(float64(fullBodyBytes)/float64(b.N), "full_body_bytes/op")
	b.ReportMetric(float64(parseAttempts)/float64(b.N), "parse_attempts/op")
	b.ReportMetric(float64(parsedMessages)/float64(b.N), "parsed_messages/op")
	b.ReportMetric(float64(receiptLookups)/float64(b.N), "receipt_lookups/op")
	b.ReportMetric(float64(storeCalls)/float64(b.N), "store_calls/op")
	b.ReportMetric(float64(elapsed)/float64(b.N), "elapsed_ns/op")
}

func BenchmarkEmailPollOnceReceiptDeduplication(b *testing.B) {
	profiles := []struct {
		name           string
		bodySize       int
		attachmentSize int
	}{
		{name: "Plain4KiB", bodySize: 4 * 1024},
		{name: "MIME256KiBWith16KiBAttachment", bodySize: 256 * 1024, attachmentSize: 16 * 1024},
	}
	for _, profile := range profiles {
		profile := profile
		for _, messageCount := range []int{10, 100, 1000} {
			messageCount := messageCount
			for _, mix := range []struct {
				name          string
				receiptedRate int
			}{
				{name: "0%", receiptedRate: 0},
				{name: "50%", receiptedRate: 50},
				{name: "100%", receiptedRate: 100},
			} {
				mix := mix
				name := fmt.Sprintf("%s/%d/%s", profile.name, messageCount, mix.name)
				b.Run(name, func(b *testing.B) {
					benchmarkEmailPollOnce(b, messageCount, mix.receiptedRate, profile.bodySize, profile.attachmentSize)
				})
			}
		}
	}
}

type emailPollAddressSnapshotBenchmarkFixture struct {
	db           *sql.DB
	counter      *testutil.SQLStatementCounter
	settingsRepo *repository.SettingsRepo
	svc          *EmailService
	client       *fakeEmailIMAPClient
	cfg          EmailRuntimeConfig
}

func newEmailPollAddressSnapshotBenchmarkFixture(tb testing.TB, messageCount int) *emailPollAddressSnapshotBenchmarkFixture {
	tb.Helper()
	db, counter := testutil.NewStatementCountingTestDB(tb)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	if err := settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"); err != nil {
		tb.Fatalf("save email address: %v", err)
	}
	fixture := &emailPollAddressSnapshotBenchmarkFixture{
		db:           db,
		counter:      counter,
		settingsRepo: settingsRepo,
		client:       newFakeEmailIMAPClient(emailPollSelfMessages(messageCount)...),
		cfg:          EmailRuntimeConfig{Address: " Bot@Example.com ", IMAPHost: "imap.example.com"},
	}
	fixture.svc = &EmailService{settingsRepo: settingsRepo}
	fixture.svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) {
		return fixture.client, nil
	}
	return fixture
}

func emailPollSelfMessages(messageCount int) []*imap.Message {
	messages := make([]*imap.Message, 0, messageCount)
	body := strings.Repeat("x", 4*1024)
	for i := 1; i <= messageCount; i++ {
		messages = append(messages, testIMAPMessageWithBody(uint32(i), fmt.Sprintf("self-%d", i), "bot@example.com", body))
	}
	return messages
}

func (f *emailPollAddressSnapshotBenchmarkFixture) reset() {
	for id := range f.client.messages {
		f.client.seen[id] = false
	}
	f.client.fetchCount = 0
	f.client.fullBodyFetches = 0
	f.client.fullBodyFetchIDs = nil
	f.client.fullBodyBytes = 0
	f.client.parseAttempts = 0
	f.client.parsedMessages = 0
	f.client.fetchItems = nil
	f.client.fetchIDs = nil
	f.client.storeCalls = 0
	f.client.storedIDs = nil
}

func (f *emailPollAddressSnapshotBenchmarkFixture) useAddressSnapshot(legacy bool) {
	f.svc.processIncomingMessageFn = nil
	if legacy {
		svc := f.svc
		f.svc.processIncomingMessageFn = func(ctx context.Context, msg EmailInboundMessage) bool {
			return svc.processIncomingMessage(ctx, msg, "", "").handled
		}
	}
}

func (f *emailPollAddressSnapshotBenchmarkFixture) poll(ctx context.Context) {
	f.svc.pollOnce(ctx, f.cfg)
}

const emailPollAddressPointQuery = "SELECT value FROM app_settings WHERE key = ?"

func isEmailPollAddressPointQuery(query string) bool {
	return strings.EqualFold(strings.TrimSpace(query), emailPollAddressPointQuery)
}

type emailPollAddressSnapshotMeasurement struct {
	medianWall            time.Duration
	bytesPerRun           float64
	allocsPerRun          float64
	totalStatements       int64
	appSettingsStatements int64
}

func medianEmailPollFloat(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	return ordered[len(ordered)/2]
}

func emailPollAllocatedBytes(tb testing.TB, runs int, poll func()) float64 {
	tb.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		poll()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}

func measureEmailPollAddressSnapshot(tb testing.TB, messageCount int, legacy bool) emailPollAddressSnapshotMeasurement {
	tb.Helper()
	fixture := newEmailPollAddressSnapshotBenchmarkFixture(tb, messageCount)
	fixture.useAddressSnapshot(legacy)
	ctx := context.Background()
	var totalStatements atomic.Int64
	var appSettingsStatements atomic.Int64
	fixture.counter.SetObserver(func(_ context.Context, query string) {
		totalStatements.Add(1)
		if isEmailPollAddressPointQuery(query) {
			appSettingsStatements.Add(1)
		}
	})
	defer fixture.counter.SetObserver(nil)

	const (
		medianSamples = 5
		timingRuns    = 3
	)
	poll := func() {
		fixture.reset()
		fixture.poll(ctx)
	}
	poll()
	totalStatements.Store(0)
	appSettingsStatements.Store(0)
	wallSamples := make([]float64, medianSamples)
	bytesSamples := make([]float64, medianSamples)
	allocSamples := make([]float64, medianSamples)
	for sample := range wallSamples {
		var elapsed time.Duration
		for i := 0; i < timingRuns; i++ {
			started := time.Now()
			poll()
			elapsed += time.Since(started)
		}
		wallSamples[sample] = float64(elapsed) / float64(timingRuns)
		bytesSamples[sample] = emailPollAllocatedBytes(tb, timingRuns, poll)
		allocSamples[sample] = testing.AllocsPerRun(timingRuns, poll)
	}

	totalStatements.Store(0)
	appSettingsStatements.Store(0)
	poll()
	require.Len(tb, fixture.client.seenIDs(), messageCount, "measured poll must acknowledge every self-sent message")
	return emailPollAddressSnapshotMeasurement{
		medianWall:            time.Duration(medianEmailPollFloat(wallSamples)),
		bytesPerRun:           medianEmailPollFloat(bytesSamples),
		allocsPerRun:          medianEmailPollFloat(allocSamples),
		totalStatements:       totalStatements.Load(),
		appSettingsStatements: appSettingsStatements.Load(),
	}
}

func TestEmailPollOnceAddressSnapshotPerformance(t *testing.T) {
	for _, messageCount := range []int{1, 10, 100} {
		t.Run(fmt.Sprintf("%d messages", messageCount), func(t *testing.T) {
			baseline := measureEmailPollAddressSnapshot(t, messageCount, true)
			candidate := measureEmailPollAddressSnapshot(t, messageCount, false)
			t.Logf("median baseline: wall=%s bytes=%.0f B/op allocs=%.0f statements=%d app_settings=%d; candidate: wall=%s bytes=%.0f B/op allocs=%.0f statements=%d app_settings=%d", baseline.medianWall, baseline.bytesPerRun, baseline.allocsPerRun, baseline.totalStatements, baseline.appSettingsStatements, candidate.medianWall, candidate.bytesPerRun, candidate.allocsPerRun, candidate.totalStatements, candidate.appSettingsStatements)

			require.Equal(t, int64(messageCount), baseline.totalStatements)
			require.Equal(t, int64(messageCount), baseline.appSettingsStatements)
			require.Zero(t, candidate.totalStatements)
			require.Zero(t, candidate.appSettingsStatements)
			switch messageCount {
			case 1:
				require.LessOrEqual(t, float64(candidate.medianWall), float64(baseline.medianWall)*1.05, "one-message poll wall time must not regress by more than 5%%")
			case 100:
				require.LessOrEqual(t, float64(candidate.medianWall), float64(baseline.medianWall)*0.95, "100-message poll wall time must improve by at least 5%%")
			}
		})
	}
}

func BenchmarkEmailPollAddressSnapshot(b *testing.B) {
	for _, messageCount := range []int{1, 10, 100} {
		for _, mode := range []struct {
			name   string
			legacy bool
		}{
			{name: "baseline", legacy: true},
			{name: "candidate", legacy: false},
		} {
			mode := mode
			b.Run(fmt.Sprintf("%d/%s", messageCount, mode.name), func(b *testing.B) {
				fixture := newEmailPollAddressSnapshotBenchmarkFixture(b, messageCount)
				fixture.useAddressSnapshot(mode.legacy)
				ctx := context.Background()
				var totalStatements atomic.Int64
				var appSettingsStatements atomic.Int64
				fixture.counter.SetObserver(func(_ context.Context, query string) {
					totalStatements.Add(1)
					if isEmailPollAddressPointQuery(query) {
						appSettingsStatements.Add(1)
					}
				})
				fixture.reset()
				fixture.poll(ctx)
				require.Len(b, fixture.client.seenIDs(), messageCount, "warm poll must acknowledge every self-sent message")
				totalStatements.Store(0)
				appSettingsStatements.Store(0)
				fixture.counter.Reset()
				durations := make([]time.Duration, 0, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					fixture.reset()
					started := time.Now()
					fixture.poll(ctx)
					durations = append(durations, time.Since(started))
				}
				b.StopTimer()
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				b.ReportMetric(float64(durations[len(durations)/2].Nanoseconds()), "median_wall_ns/op")
				b.ReportMetric(float64(totalStatements.Load())/float64(b.N), "sql_statements/op")
				b.ReportMetric(float64(appSettingsStatements.Load())/float64(b.N), "app_settings_statements/op")
			})
		}
	}
}

const (
	emailPollContentionMessageCount = 100
	emailPollContentionQueries      = 16
	emailPollContentionSamples      = 5
	emailPollContentionHold         = 10 * time.Millisecond
	emailPollContentionP95Jitter    = time.Millisecond
)

const emailPollContentionQuery = "SELECT COUNT(*) FROM projects"

// emailPollContentionReceiptStore holds a real SQLite query open after the first
// message reaches the receipt-record boundary. Both the legacy and snapshot
// paths invoke Record at that same pollOnce boundary, so their unrelated-query
// waits share the same database hold while legacy address reads are queued.
type emailPollContentionReceiptStore struct {
	db       *sql.DB
	acquired chan struct{}
	release  <-chan struct{}
	finished chan struct{}
	abort    <-chan struct{}
	once     sync.Once
	queryErr error
}

func (s *emailPollContentionReceiptStore) Exists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (s *emailPollContentionReceiptStore) holdCommonQuery() {
	rows, err := s.db.QueryContext(context.Background(), emailPollContentionQuery)
	s.queryErr = err
	close(s.acquired)
	if err != nil {
		close(s.finished)
		return
	}
	select {
	case <-s.release:
	case <-s.abort:
	}
	_ = rows.Close()
	close(s.finished)
}

func (s *emailPollContentionReceiptStore) Record(_ context.Context, _, _ string) error {
	s.once.Do(func() { go s.holdCommonQuery() })
	<-s.acquired
	return s.queryErr
}

func (s *emailPollContentionReceiptStore) WithHandoff(_ context.Context, _, _ string, _ func(repository.SQLExecutor) error) (bool, error) {
	return false, nil
}

func measureEmailPollAddressSnapshotContention(tb testing.TB, legacy bool) []time.Duration {
	tb.Helper()
	fixture := newEmailPollAddressSnapshotBenchmarkFixture(tb, emailPollContentionMessageCount)
	fixture.useAddressSnapshot(legacy)
	ctx := context.Background()
	waits := make([]time.Duration, 0, emailPollContentionSamples*emailPollContentionQueries)
	for sample := 0; sample < emailPollContentionSamples; sample++ {
		fixture.client = newFakeEmailIMAPClient(emailPollSelfMessages(emailPollContentionMessageCount)...)
		fixture.reset()
		fixture.useAddressSnapshot(legacy)
		acquired := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		abort := make(chan struct{})
		releaseReceipt := func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}
		receipts := &emailPollContentionReceiptStore{
			db:       fixture.db,
			acquired: acquired,
			release:  release,
			finished: finished,
			abort:    abort,
		}
		fixture.svc.emailInboundReceiptStore = receipts
		pointQueries := atomic.Int64{}
		secondLegacyQuery := make(chan struct{})
		var secondLegacyQueryOnce sync.Once
		fixture.settingsRepo.SetQueryObserver(func(query string) {
			if !legacy || !isEmailPollAddressPointQuery(query) {
				return
			}
			if pointQueries.Add(1) == 2 {
				secondLegacyQueryOnce.Do(func() { close(secondLegacyQuery) })
			}
		})

		cleanup := func() {
			releaseReceipt()
			select {
			case <-finished:
			case <-time.After(time.Second):
			}
			close(abort)
			fixture.settingsRepo.SetQueryObserver(nil)
			fixture.svc.emailInboundReceiptStore = nil
		}
		defer cleanup()

		pollDone := make(chan struct{})
		go func() {
			fixture.poll(ctx)
			close(pollDone)
		}()
		select {
		case <-acquired:
		case <-time.After(time.Second):
			tb.Fatalf("poll did not acquire the common SQLite contention query")
		}
		require.NoError(tb, receipts.queryErr)

		queryReady := make(chan struct{}, emailPollContentionQueries)
		startQueries := make(chan struct{})
		queryWaits := make(chan time.Duration, emailPollContentionQueries)
		queryErrors := make(chan error, emailPollContentionQueries)
		// Snapshot before launching competitors. The ready/start barrier proves all
		// competitors are queued behind the same held database connection.
		waitCount := fixture.db.Stats().WaitCount
		var queries sync.WaitGroup
		queries.Add(emailPollContentionQueries)
		for i := 0; i < emailPollContentionQueries; i++ {
			go func() {
				defer queries.Done()
				queryReady <- struct{}{}
				<-startQueries
				started := time.Now()
				var projectCount int
				err := fixture.db.QueryRowContext(ctx, emailPollContentionQuery).Scan(&projectCount)
				if err != nil {
					queryErrors <- err
					return
				}
				queryWaits <- time.Since(started)
			}()
		}
		for i := 0; i < emailPollContentionQueries; i++ {
			<-queryReady
		}
		close(startQueries)
		require.Eventually(tb, func() bool {
			return fixture.db.Stats().WaitCount >= waitCount+emailPollContentionQueries
		}, time.Second, time.Millisecond, "all unrelated queries must queue behind the common poll query")

		started := time.Now()
		if legacy {
			select {
			case <-secondLegacyQuery:
			case <-time.After(time.Second):
				tb.Fatalf("legacy poll did not queue a per-message app_settings query")
			}
			require.Equal(tb, 1, fixture.db.Stats().InUse, "the common SQLite hold must still own the only connection while the legacy settings query is issued")
		}
		if remaining := emailPollContentionHold - time.Since(started); remaining > 0 {
			timer := time.NewTimer(remaining)
			<-timer.C
		}
		// Release the common database hold, then let the real poll finish before
		// collecting competitor waits. This keeps legacy address reads and
		// competing queries in the same post-release queue rather than measuring
		// competitors after legacy processing has already ended.
		releaseReceipt()
		select {
		case <-pollDone:
		case <-time.After(time.Second):
			tb.Fatalf("poll did not finish after contention release")
		}
		queries.Wait()
		select {
		case <-finished:
		case <-time.After(time.Second):
			tb.Fatalf("common SQLite contention query did not finish")
		}
		for i := 0; i < emailPollContentionQueries; i++ {
			select {
			case err := <-queryErrors:
				tb.Fatalf("unrelated SQLite query failed: %v", err)
			case wait := <-queryWaits:
				waits = append(waits, wait)
			}
		}
	}
	return waits
}

func emailPollDurationPercentile(values []time.Duration, percentile int) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[(len(sorted)-1)*percentile/100]
}

func TestEmailPollAddressSnapshotContentionDoesNotRegressP95(t *testing.T) {
	baseline := measureEmailPollAddressSnapshotContention(t, true)
	candidate := measureEmailPollAddressSnapshotContention(t, false)
	baselineMedian := emailPollDurationPercentile(baseline, 50)
	baselineP95 := emailPollDurationPercentile(baseline, 95)
	candidateMedian := emailPollDurationPercentile(candidate, 50)
	candidateP95 := emailPollDurationPercentile(candidate, 95)
	t.Logf("unrelated query wait: baseline median=%s p95=%s; candidate median=%s p95=%s (allowed p95 jitter=%s)", baselineMedian, baselineP95, candidateMedian, candidateP95, emailPollContentionP95Jitter)
	require.LessOrEqual(t, candidateP95, baselineP95+emailPollContentionP95Jitter, "poll snapshot reuse must not materially regress unrelated SQLite query p95 wait")
}

func TestEmailPollOnceLeavesParseFailuresUnread(t *testing.T) {
	client := newFakeEmailIMAPClient(testIMAPMessage(1, "malformed", ""))
	svc := &EmailService{}
	processed := 0
	svc.processIncomingMessageFn = func(context.Context, EmailInboundMessage) bool {
		processed++
		return true
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }

	svc.pollOnce(context.Background(), EmailRuntimeConfig{})
	svc.pollOnce(context.Background(), EmailRuntimeConfig{})

	assert.Empty(t, client.seenIDs())
	assert.Zero(t, client.storeCalls)
	assert.Zero(t, processed)
	assert.Equal(t, 4, client.fetchCount)
	assert.Equal(t, 2, client.fullBodyFetches)
}

type fakeEmailIMAPClient struct {
	messages                   map[uint32]*imap.Message
	seen                       map[uint32]bool
	fetchCount                 int
	fullBodyFetches            int
	fullBodyFetchIDs           [][]uint32
	fullBodyBytes              int64
	parseAttempts              int64
	parsedMessages             int64
	fetchItems                 [][]imap.FetchItem
	fetchIDs                   [][]uint32
	storeCalls                 int
	storedIDs                  [][]uint32
	storeFailures              int
	storeDelay                 time.Duration
	uidValidity                uint32
	metadataMessageIDOmissions map[uint32]bool
	bodyData                   map[uint32]map[*imap.BodySectionName][]byte
}

func newFakeEmailIMAPClient(messages ...*imap.Message) *fakeEmailIMAPClient {
	client := &fakeEmailIMAPClient{
		messages: make(map[uint32]*imap.Message),
		seen:     make(map[uint32]bool),
		bodyData: make(map[uint32]map[*imap.BodySectionName][]byte),
	}
	for _, msg := range messages {
		client.setMessage(msg)
	}
	return client
}

func (c *fakeEmailIMAPClient) setMessage(msg *imap.Message) {
	c.messages[msg.SeqNum] = msg
	if len(msg.Body) == 0 {
		delete(c.bodyData, msg.SeqNum)
		return
	}
	bodyData := make(map[*imap.BodySectionName][]byte, len(msg.Body))
	freshBody := make(map[*imap.BodySectionName]imap.Literal, len(msg.Body))
	for section, body := range msg.Body {
		data, _ := io.ReadAll(body)
		bodyData[section] = data
		freshBody[section] = bytes.NewReader(data)
	}
	c.bodyData[msg.SeqNum] = bodyData
	msg.Body = freshBody
}

func testIMAPMessage(id uint32, subject, from string) *imap.Message {
	msg := &imap.Message{SeqNum: id, Envelope: &imap.Envelope{Subject: subject, MessageId: fmt.Sprintf("<message-%d@example.com>", id)}}
	if from != "" {
		parts := strings.SplitN(from, "@", 2)
		msg.Envelope.From = []*imap.Address{{MailboxName: parts[0], HostName: parts[1]}}
	}
	return msg
}

func testIMAPMessageWithoutMessageID(id, uid uint32, subject, from, body string) *imap.Message {
	parts := strings.SplitN(from, "@", 2)
	raw := fmt.Sprintf("From: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", from, subject, body)
	return &imap.Message{
		SeqNum:   id,
		Uid:      uid,
		Envelope: &imap.Envelope{Subject: subject, From: []*imap.Address{{MailboxName: parts[0], HostName: parts[1]}}},
		Body:     testIMAPBodySections(raw),
	}
}

func testIMAPBodySections(raw string) map[*imap.BodySectionName]imap.Literal {
	header := raw
	if headerEnd := strings.Index(header, "\r\n\r\n"); headerEnd >= 0 {
		header = header[:headerEnd+len("\r\n\r\n")]
	}
	responseSection := emailMessageIDHeaderSection()
	responseSection.Peek = false
	return map[*imap.BodySectionName]imap.Literal{
		&imap.BodySectionName{}: bytes.NewBufferString(raw),
		responseSection:         bytes.NewBufferString(header),
	}
}

func testIMAPMessageWithBody(id uint32, subject, from, body string) *imap.Message {
	msg := testIMAPMessage(id, subject, from)
	raw := fmt.Sprintf("From: %s\r\nSubject: %s\r\nMessage-ID: <message-%d@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", from, subject, id, body)
	msg.Body = testIMAPBodySections(raw)
	return msg
}

func testIMAPMessageWithMIME(id uint32, subject, from, body string, attachment []byte) *imap.Message {
	msg := testIMAPMessage(id, subject, from)
	boundary := fmt.Sprintf("BOUND-%d", id)
	var raw strings.Builder
	fmt.Fprintf(&raw, "From: %s\r\nSubject: %s\r\nMessage-ID: <message-%d@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", from, subject, id, boundary)
	fmt.Fprintf(&raw, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, body)
	if len(attachment) > 0 {
		encoded := base64.StdEncoding.EncodeToString(attachment)
		fmt.Fprintf(&raw, "--%s\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"payload-%d.bin\"\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary, id)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			raw.WriteString(encoded[i:end] + "\r\n")
		}
	}
	fmt.Fprintf(&raw, "--%s--\r\n", boundary)
	msg.Body = testIMAPBodySections(raw.String())
	return msg
}

func testIMAPReplyWithBody(id uint32, subject, from, references, body string) *imap.Message {
	msg := testIMAPMessage(id, subject, from)
	raw := fmt.Sprintf("From: %s\r\nSubject: %s\r\nMessage-ID: <message-%d@example.com>\r\nReferences: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", from, subject, id, references, body)
	msg.Body = testIMAPBodySections(raw)
	return msg
}

func testIMAPReplyInReplyToOnlyWithBody(id uint32, subject, from, inReplyTo, body string) *imap.Message {
	msg := testIMAPMessage(id, subject, from)
	raw := fmt.Sprintf("From: %s\r\nSubject: %s\r\nMessage-ID: <message-%d@example.com>\r\nIn-Reply-To: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", from, subject, id, inReplyTo, body)
	msg.Body = testIMAPBodySections(raw)
	return msg
}

func (c *fakeEmailIMAPClient) Login(string, string) error { return nil }
func (c *fakeEmailIMAPClient) Select(string, bool) (*imap.MailboxStatus, error) {
	return &imap.MailboxStatus{UidValidity: c.uidValidity}, nil
}
func (c *fakeEmailIMAPClient) Search(*imap.SearchCriteria) ([]uint32, error) {
	var ids []uint32
	for id := uint32(1); id <= uint32(len(c.messages)); id++ {
		if !c.seen[id] {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (c *fakeEmailIMAPClient) Fetch(seqset *imap.SeqSet, items []imap.FetchItem, ch chan *imap.Message) error {
	c.fetchCount++
	c.fetchItems = append(c.fetchItems, append([]imap.FetchItem(nil), items...))
	var fetchedIDs []uint32
	for id := range c.messages {
		if seqset.Contains(id) {
			fetchedIDs = append(fetchedIDs, id)
		}
	}
	sort.Slice(fetchedIDs, func(i, j int) bool { return fetchedIDs[i] < fetchedIDs[j] })
	c.fetchIDs = append(c.fetchIDs, append([]uint32(nil), fetchedIDs...))
	fullBody := false
	for _, item := range items {
		if item == (&imap.BodySectionName{}).FetchItem() {
			fullBody = true
			break
		}
	}
	if fullBody {
		c.fullBodyFetches++
		c.fullBodyFetchIDs = append(c.fullBodyFetchIDs, append([]uint32(nil), fetchedIDs...))
		for _, id := range fetchedIDs {
			if body := c.messages[id].GetBody(&imap.BodySectionName{}); body != nil {
				if sized, ok := body.(interface{ Len() int }); ok {
					c.fullBodyBytes += int64(sized.Len())
				}
			}
		}
	}
	defer close(ch)
	for _, id := range fetchedIDs {
		message := c.messages[id]
		if bodyData := c.bodyData[id]; len(bodyData) > 0 {
			copyMessage := *message
			copyMessage.Body = make(map[*imap.BodySectionName]imap.Literal, len(bodyData))
			for section, data := range bodyData {
				copyMessage.Body[section] = bytes.NewReader(data)
			}
			message = &copyMessage
		}
		if !fullBody && c.metadataMessageIDOmissions[id] {
			metadataSection := emailMessageIDHeaderSection()
			metadataSection.Peek = false
			copyMessage := *message
			copyMessage.Body = make(map[*imap.BodySectionName]imap.Literal, len(message.Body))
			for section, body := range message.Body {
				if metadataSection.Equal(section) {
					continue
				}
				copyMessage.Body[section] = body
			}
			message = &copyMessage
		}
		ch <- message
	}
	return nil
}
func (c *fakeEmailIMAPClient) Store(seqset *imap.SeqSet, _ imap.StoreItem, _ interface{}, _ chan *imap.Message) error {
	c.storeCalls++
	var storedIDs []uint32
	for id := uint32(1); id <= uint32(len(c.messages)); id++ {
		if seqset.Contains(id) {
			storedIDs = append(storedIDs, id)
		}
	}
	if len(storedIDs) > 0 {
		c.storedIDs = append(c.storedIDs, storedIDs)
	}
	if c.storeDelay > 0 {
		time.Sleep(c.storeDelay)
	}
	if c.storeFailures > 0 {
		c.storeFailures--
		return fmt.Errorf("transient store failure")
	}
	for _, id := range storedIDs {
		c.seen[id] = true
	}
	return nil
}
func (c *fakeEmailIMAPClient) Logout() error { return nil }
func (c *fakeEmailIMAPClient) storeBatches() [][]uint32 {
	batches := make([][]uint32, len(c.storedIDs))
	for i, ids := range c.storedIDs {
		batches[i] = append([]uint32(nil), ids...)
	}
	return batches
}
func (c *fakeEmailIMAPClient) seenIDs() []uint32 {
	var ids []uint32
	for id := uint32(1); id <= uint32(len(c.messages)); id++ {
		if c.seen[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestChannelChatIngressReportsTaskAndQueuePersistenceFailures(t *testing.T) {
	t.Run("task creation", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		taskRepo := repository.NewTaskRepo(db, nil)
		execRepo := repository.NewExecutionRepo(db)
		require.NoError(t, db.Close())

		handedOff := false
		handled, _ := runChannelChatFirstTurn(context.Background(), channelChatIngressFirstTurnOptions{
			Platform:         "email",
			ProjectID:        "project",
			Message:          "hello",
			Task:             &models.Task{Title: "email"},
			Agent:            &models.LLMConfig{ID: "agent"},
			TaskRepo:         taskRepo,
			ExecRepo:         execRepo,
			OnDurableHandoff: func() { handedOff = true },
		})
		assert.True(t, handled)
		assert.False(t, handedOff)
	})

	t.Run("queue write", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		threadInputRepo := repository.NewThreadInputRepo(db)
		require.NoError(t, db.Close())

		handedOff := false
		handled := runChannelChatQueuedInput(context.Background(), channelChatIngressQueueOptions{
			Platform:         "email",
			ProjectID:        "project",
			ActiveExecID:     "execution",
			AgentID:          "agent",
			Message:          "hello",
			ThreadInputRepo:  threadInputRepo,
			OnDurableHandoff: func() { handedOff = true },
		})
		assert.True(t, handled)
		assert.False(t, handedOff)
	})
}

func TestEmailService_ProcessIncomingUsesFreshNormalizedAddress(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, " Bot@Example.com "))
	svc := &EmailService{settingsRepo: settingsRepo}

	assert.True(t, svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "BOT@example.com", Body: "self"}), "normalized direct-call self filter must ignore the message")

	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "new@example.com"))
	assert.False(t, svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "bot@example.com", Body: "not self anymore"}), "direct calls must use the fresh address snapshot")
	assert.True(t, svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "NEW@EXAMPLE.COM", Body: "self again"}), "the new normalized address must be used")

	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, ""))
	assert.False(t, svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "new@example.com", Body: "removed"}), "removed settings must not leave a stale direct-call address")
}

func TestEmailProcessIncomingAcknowledgesIntentionalIgnores(t *testing.T) {
	svc := &EmailService{}
	assert.True(t, svc.ProcessIncoming(context.Background(), EmailInboundMessage{FromAddress: "noreply@example.com", Body: "automated"}))

	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	require.NoError(t, projectRepo.Create(context.Background(), &models.Project{Name: "Email"}))
	svc = NewEmailService(nil, projectRepo, repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), &TaskService{}, &LLMService{}, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	assert.True(t, svc.ProcessIncoming(context.Background(), EmailInboundMessage{FromAddress: "unauthorized@example.com", Subject: "request", Body: "hello"}))
}

func TestNormalizeEmailPasswordForProvider(t *testing.T) {
	assert.Equal(t, "abcdefghijklmnop", NormalizeEmailPasswordForProvider(EmailProviderGmail, " abcd efgh ijkl mnop "))
	assert.Equal(t, "abc def", NormalizeEmailPasswordForProvider(EmailProviderCustom, " abc def "))
}

func TestEmailService_LoadConfigNormalizesSavedProviderAppPassword(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingProvider, EmailProviderGmail))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingPassword, "abcd efgh ijkl mnop"))
	svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))

	cfg, err := svc.loadConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "abcdefghijklmnop", cfg.Password)
}

func TestEmailService_IgnoresUnauthorizedAutomatedAndSelfSentMessages(t *testing.T) {
	assert.True(t, isIgnoredEmail(EmailInboundMessage{FromAddress: "bot@example.com", Subject: "self", Body: "hello"}, "bot@example.com"))
	assert.True(t, isIgnoredEmail(EmailInboundMessage{FromAddress: "noreply@example.com", Subject: "auto", Body: "hello"}, "bot@example.com"))
	assert.True(t, isIgnoredEmail(EmailInboundMessage{FromAddress: "user@example.com", Subject: "list", Body: "hello", ListUnsub: "<mailto:unsubscribe@example.com>"}, "bot@example.com"))
	assert.True(t, isIgnoredEmail(EmailInboundMessage{FromAddress: "user@example.com", Subject: "bulk", Body: "hello", Precedence: "bulk"}, "bot@example.com"))
	assert.False(t, isIgnoredEmail(EmailInboundMessage{FromAddress: "alice@example.com", Subject: "ok", Body: "hello"}, "bot@example.com"))
}

func TestEmailService_AuthorizationRequiresConfiguredSender(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	project := &models.Project{Name: "Email Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	svc := NewEmailService(settingsRepo, projectRepo, repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, emailAuthRepo, repository.NewEmailTaskContextRepo(db))
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)

	assert.Empty(t, svc.resolveAuthorizedProject(ctx, "alice@example.com"))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "Alice@Example.com", AddedBy: "test"}))
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "alice@example.com", project.ID))
	assert.Equal(t, project.ID, svc.resolveAuthorizedProject(ctx, "alice@example.com"))
	assert.Empty(t, svc.resolveAuthorizedProject(ctx, "bob@example.com"))
}

func TestParseIMAPMessageCapturesInReplyToWithoutReferences(t *testing.T) {
	msg := testIMAPReplyInReplyToOnlyWithBody(2, "Re: Root", "Alice <alice@example.com>", "<root@example.com>", "follow up")

	inbound, err := parseIMAPMessage(msg, &imap.BodySectionName{}, false)

	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", inbound.FromAddress)
	assert.Equal(t, "<message-2@example.com>", inbound.MessageID)
	assert.Equal(t, "<root@example.com>", inbound.InReplyTo)
	assert.Empty(t, inbound.References)
	assert.Equal(t, "follow up", inbound.Body)
}

func TestEmailThreadingHelpers(t *testing.T) {
	msg := EmailInboundMessage{FromName: "Alice", FromAddress: "alice@example.com", Subject: "Deploy question", Body: "What now?", MessageID: "<m2@example.com>", References: "<root@example.com> <m1@example.com>", InReplyTo: "<ignored@example.com>"}
	assert.Equal(t, "[Email from: Alice <alice@example.com>]\n[Subject: Deploy question]\n\nWhat now?", BuildEmailPrompt(msg))
	assert.Equal(t, "email:alice@example.com:<root@example.com>", EmailSessionKey("Alice@Example.com", msg.MessageID, msg.References, msg.InReplyTo, msg.Subject))
	assert.Equal(t, "email:alice@example.com:<root@example.com>", EmailSessionKey("alice@example.com", "<reply@example.com>", "", "<root@example.com>", "Deploy question"))
	assert.Equal(t, "Re: Deploy question", replySubject("Deploy question"))
	assert.Equal(t, "Re: Deploy question", replySubject("Re: Deploy question"))
	assert.Equal(t, "<root@example.com> <m1@example.com> <m2@example.com>", appendEmailReference(msg.References, msg.MessageID))
	assert.NotEqual(t, EmailSessionKey("alice@example.com", "", "", "", "Subject A"), EmailSessionKey("alice@example.com", "", "", "", "Subject B"))
}

func TestAppendEmailReferenceBoundsLongChainsAndRetainsRootAndLatest(t *testing.T) {
	refs := testEmailReferenceChain(80)
	latest := "<reply@example.com>"

	got := appendEmailReference(refs, latest)
	ids := strings.Fields(got)
	require.LessOrEqual(t, len(ids), 32)
	assert.Equal(t, "<thread-000@example.com>", ids[0])
	assert.Equal(t, latest, ids[len(ids)-1])
}

func TestDefaultEmailSendMailFoldsBoundedReferencesForSMTPDelivery(t *testing.T) {
	host, port, received := startTestSMTPServer(t)
	references := appendEmailReference(testEmailReferenceChain(80), "<reply@example.com>")
	inReplyTo := "<" + strings.Repeat("a", emailMaxMessageIDLength-len("<@example.com>")) + "@example.com>"
	cfg := EmailRuntimeConfig{Address: "bot@example.com", Password: "secret", SMTPHost: host, SMTPPort: port}

	require.NoError(t, defaultEmailSendMail(context.Background(), cfg, "alice@example.com", "Thread", "completed", "<outbound@example.com>", inReplyTo, references))
	wire := <-received

	headers, _, found := bytes.Cut(wire, []byte("\r\n\r\n"))
	require.True(t, found)
	for _, line := range bytes.Split(headers, []byte("\r\n")) {
		assert.LessOrEqual(t, len(line)+len("\r\n"), 998, "header line exceeds RFC 5322 limit: %q", line)
	}
	message, err := netmail.ReadMessage(bytes.NewReader(wire))
	require.NoError(t, err)
	assert.Equal(t, "<outbound@example.com>", message.Header.Get("Message-ID"))
	assert.Contains(t, message.Header.Get("References"), "<thread-000@example.com>")
	assert.Contains(t, message.Header.Get("References"), "<reply@example.com>")
	assert.Equal(t, inReplyTo, message.Header.Get("In-Reply-To"))
	assert.True(t, bytes.Contains(headers, []byte("References: ")))
	assert.True(t, bytes.Contains(headers, []byte("\r\n ")))
}

func TestDefaultEmailSendMailRejectsInjectedReplyHeaders(t *testing.T) {
	host, port, received := startTestSMTPServer(t)
	cfg := EmailRuntimeConfig{Address: "bot@example.com", Password: "secret", SMTPHost: host, SMTPPort: port}

	require.NoError(t, defaultEmailSendMail(context.Background(), cfg, "alice@example.com", "Thread", "completed", "<outbound@example.com>\r\nBcc: victim@example.com", "<reply@example.com>\r\nBcc: victim@example.com", "<root@example.com>\r\nBcc: victim@example.com"))
	wire := <-received
	message, err := netmail.ReadMessage(bytes.NewReader(wire))
	require.NoError(t, err)
	assert.Empty(t, message.Header.Get("Message-ID"))
	assert.Empty(t, message.Header.Get("In-Reply-To"))
	assert.Empty(t, message.Header.Get("References"))
	assert.Empty(t, message.Header.Get("Bcc"))
}

func TestEmailService_ReplyPathsBoundReferenceChains(t *testing.T) {
	paths := []struct {
		name string
		send func(*EmailService, models.Task)
	}{
		{name: "task completion to thread", send: func(svc *EmailService, _ models.Task) {
			svc.SendTaskCompletionToThread(context.Background(), "alice@example.com", "<reply@example.com>", testEmailReferenceChain(80), "Question", "Task", "done", "")
		}},
		{name: "chat response", send: func(svc *EmailService, task models.Task) {
			svc.SendChatResponse(context.Background(), task, "done", "")
		}},
		{name: "task completion notification", send: func(svc *EmailService, task models.Task) {
			svc.SendTaskCompletionNotification(context.Background(), task, "done", "")
		}},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			settingsRepo := repository.NewSettingsRepo(db)
			for key, value := range map[string]string{EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret", EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com", EmailSettingSendResponses: "true"} {
				require.NoError(t, settingsRepo.Set(ctx, key, value))
			}
			projectRepo := repository.NewProjectRepo(db)
			projects, err := projectRepo.List(ctx)
			require.NoError(t, err)
			task := models.Task{ProjectID: projects[0].ID, Title: "Email task", Prompt: "request", Category: models.CategoryActive, Status: models.StatusCompleted, CreatedVia: models.TaskOriginEmail}
			require.NoError(t, repository.NewTaskRepo(db, nil).Create(ctx, &task))
			contextRepo := repository.NewEmailTaskContextRepo(db)
			require.NoError(t, contextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: task.ID, EmailFrom: "alice@example.com", EmailMessageID: "<reply@example.com>", EmailReferences: testEmailReferenceChain(80), EmailSubject: "Question"}))
			svc := NewEmailService(settingsRepo, projectRepo, repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), contextRepo)
			var inReplyTo, references string
			svc.sendMail = func(_ context.Context, _ EmailRuntimeConfig, _, _, _, _, gotInReplyTo, gotReferences string) error {
				inReplyTo, references = gotInReplyTo, gotReferences
				return nil
			}

			path.send(svc, task)
			ids := strings.Fields(references)
			require.NotEmpty(t, ids)
			assert.LessOrEqual(t, len(ids), 32)
			assert.Equal(t, "<thread-000@example.com>", ids[0])
			assert.Equal(t, "<reply@example.com>", ids[len(ids)-1])
			assert.Equal(t, "<reply@example.com>", inReplyTo)
		})
	}
}

func testEmailReferenceChain(count int) string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("<thread-%03d@example.com>", i)
	}
	return strings.Join(ids, " ")
}

func startTestSMTPServer(t *testing.T) (string, int, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		reader := textproto.NewReader(br)
		writer := bufio.NewWriter(conn)
		writeResponse := func(response string) {
			_, _ = writer.WriteString(response)
			_ = writer.Flush()
		}
		writeResponse("220 test SMTP\r\n")
		for {
			line, err := reader.ReadLine()
			if err != nil {
				return
			}
			command := strings.ToUpper(strings.Fields(line)[0])
			switch command {
			case "EHLO", "HELO":
				writeResponse("250-test SMTP\r\n250-AUTH PLAIN\r\n250 OK\r\n")
			case "AUTH":
				writeResponse("235 authenticated\r\n")
			case "MAIL", "RCPT":
				writeResponse("250 OK\r\n")
			case "DATA":
				writeResponse("354 End data with <CR><LF>.<CR><LF>\r\n")
				var data bytes.Buffer
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					if strings.HasPrefix(line, "..") {
						line = line[1:]
					}
					data.WriteString(line)
				}
				received <- data.Bytes()
				writeResponse("250 queued\r\n")
			case "QUIT":
				writeResponse("221 bye\r\n")
				return
			default:
				writeResponse("250 OK\r\n")
			}
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", address.Port, received
}

func TestEmailService_UsesThreadScopedSessionForInReplyToOnlyActiveChatAndHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	project := &models.Project{Name: "Email In-Reply-To Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "alice@example.com", project.ID))
	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	priorTask := &models.Task{ProjectID: project.ID, Title: "Prior Completed", Prompt: "prior", Category: models.CategoryChat, Status: models.StatusCompleted, CreatedVia: models.TaskOriginEmail, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, priorTask))
	require.NoError(t, emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: priorTask.ID, EmailFrom: "alice@example.com", EmailMessageID: "<root-prior@example.com>", EmailSubject: "Root", EmailSessionKey: "email:alice@example.com:<root@example.com>"}))
	priorExec := &models.Execution{TaskID: priorTask.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "root prior"}
	require.NoError(t, execRepo.Create(ctx, priorExec))
	otherTask := &models.Task{ProjectID: project.ID, Title: "Other Completed", Prompt: "other", Category: models.CategoryChat, Status: models.StatusCompleted, CreatedVia: models.TaskOriginEmail, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, otherTask))
	require.NoError(t, emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: otherTask.ID, EmailFrom: "alice@example.com", EmailMessageID: "<other@example.com>", EmailSubject: "Other", EmailSessionKey: "email:alice@example.com:<other@example.com>"}))
	otherExec := &models.Execution{TaskID: otherTask.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "other prior"}
	require.NoError(t, execRepo.Create(ctx, otherExec))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, workerSvc, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	threadInputRepo := repository.NewThreadInputRepo(db)
	svc.SetThreadInputRepo(threadInputRepo)
	var runReq ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runReq = req })
	svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "alice@example.com", Subject: "Root", Body: "continue root", MessageID: "<reply@example.com>", InReplyTo: "<root@example.com>"})

	require.NotEmpty(t, runReq.ExecID)
	require.Equal(t, "email:alice@example.com:<root@example.com>", runReq.ReplyContext.EmailSessionKey)
	require.Len(t, runReq.ChatHistory, 1)
	require.Equal(t, priorExec.ID, runReq.ChatHistory[0].ID)
	require.NotEqual(t, otherExec.ID, runReq.ChatHistory[0].ID)
	pending, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, pending)

	svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "alice@example.com", Subject: "Root", Body: "queue root", MessageID: "<reply-2@example.com>", InReplyTo: "<root@example.com>"})

	pending, err = threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "email:alice@example.com:<root@example.com>", pending[0].EmailSessionKey)
	require.Equal(t, "<reply-2@example.com>", pending[0].EmailMessageID)
	require.Empty(t, pending[0].EmailReferences)
}

func TestEmailService_UsesOutboundResponseIDForActiveChatQueue(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	project := &models.Project{Name: "Email Outbound Active Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "alice@example.com", project.ID))
	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	sessionKey := "email:alice@example.com:<root@example.com>"
	activeTask := &models.Task{ProjectID: project.ID, Title: "Root Thread", Prompt: "root", Category: models.CategoryChat, Status: models.StatusRunning, CreatedVia: models.TaskOriginEmail, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	require.NoError(t, emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: activeTask.ID, EmailFrom: "alice@example.com", EmailMessageID: "<root@example.com>", EmailSubject: "Root", EmailSessionKey: sessionKey}))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "root active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))
	require.NoError(t, emailTaskContextRepo.RecordOutboundMessageRef(ctx, project.ID, "Alice@Example.com", "<bot-response@example.com>", sessionKey))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, workerSvc, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	threadInputRepo := repository.NewThreadInputRepo(db)
	svc.SetThreadInputRepo(threadInputRepo)
	var runReq ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runReq = req })

	require.True(t, svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "alice@example.com", Subject: "Root", Body: "queue behind active", MessageID: "<followup@example.com>", InReplyTo: "<bot-response@example.com>"}))

	require.Empty(t, runReq.ExecID)
	pending, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, sessionKey, pending[0].EmailSessionKey)
	assert.Equal(t, "<followup@example.com>", pending[0].EmailMessageID)
	assert.Equal(t, activeExec.ID, pending[0].RunExecutionID)
}

func TestEmailService_UsesOutboundResponseIDForCompletedHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	project := &models.Project{Name: "Email Outbound History Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	otherProject := &models.Project{Name: "Other Email Project"}
	require.NoError(t, projectRepo.Create(ctx, otherProject))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "alice@example.com", project.ID))
	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	sessionKey := "email:alice@example.com:<root@example.com>"
	priorTask := &models.Task{ProjectID: project.ID, Title: "Prior Completed", Prompt: "prior", Category: models.CategoryChat, Status: models.StatusCompleted, CreatedVia: models.TaskOriginEmail, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, priorTask))
	require.NoError(t, emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: priorTask.ID, EmailFrom: "alice@example.com", EmailMessageID: "<root@example.com>", EmailSubject: "Root", EmailSessionKey: sessionKey}))
	priorExec := &models.Execution{TaskID: priorTask.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "root prior"}
	require.NoError(t, execRepo.Create(ctx, priorExec))
	require.NoError(t, emailTaskContextRepo.RecordOutboundMessageRef(ctx, project.ID, "alice@example.com", "<bot-response@example.com>", sessionKey))
	require.NoError(t, emailTaskContextRepo.RecordOutboundMessageRef(ctx, otherProject.ID, "alice@example.com", "<foreign-bot-response@example.com>", "email:alice@example.com:<foreign@example.com>"))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, workerSvc, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	var runReq ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runReq = req })

	require.True(t, svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "alice@example.com", Subject: "Root", Body: "continue from bot response", MessageID: "<followup@example.com>", InReplyTo: "<bot-response@example.com>"}))

	require.NotEmpty(t, runReq.ExecID)
	require.Equal(t, sessionKey, runReq.ReplyContext.EmailSessionKey)
	require.Len(t, runReq.ChatHistory, 1)
	assert.Equal(t, priorExec.ID, runReq.ChatHistory[0].ID)

	runReq = ChannelChatRunRequest{}
	require.True(t, svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "alice@example.com", Subject: "Unknown Root", Body: "unknown bot response", MessageID: "<unknown-followup@example.com>", InReplyTo: "<foreign-bot-response@example.com>"}))

	require.NotEmpty(t, runReq.ExecID)
	assert.Equal(t, "email:alice@example.com:<foreign-bot-response@example.com>", runReq.ReplyContext.EmailSessionKey)
	assert.Empty(t, runReq.ChatHistory)
}

func TestEmailService_UsesThreadScopedSessionForActiveChatAndHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	project := &models.Project{Name: "Email Session Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "alice@example.com", project.ID))
	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	activeTask := &models.Task{ProjectID: project.ID, Title: "Root Thread", Prompt: "root", Category: models.CategoryChat, Status: models.StatusRunning, CreatedVia: models.TaskOriginEmail, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	require.NoError(t, emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: activeTask.ID, EmailFrom: "alice@example.com", EmailMessageID: "<root@example.com>", EmailSubject: "Root", EmailSessionKey: "email:alice@example.com:<root@example.com>"}))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "root active"}
	require.NoError(t, execRepo.Create(ctx, activeExec))
	completedTask := &models.Task{ProjectID: project.ID, Title: "Other Completed", Prompt: "other", Category: models.CategoryChat, Status: models.StatusCompleted, CreatedVia: models.TaskOriginEmail, AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, completedTask))
	require.NoError(t, emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: completedTask.ID, EmailFrom: "alice@example.com", EmailMessageID: "<other@example.com>", EmailSubject: "Other", EmailSessionKey: "email:alice@example.com:<other@example.com>"}))
	completedExec := &models.Execution{TaskID: completedTask.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "other prior"}
	require.NoError(t, execRepo.Create(ctx, completedExec))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, workerSvc, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))
	var runReq ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runReq = req })
	svc.ProcessIncoming(ctx, EmailInboundMessage{FromAddress: "alice@example.com", Subject: "Other", Body: "continue other", MessageID: "<other-2@example.com>", References: "<other@example.com>"})

	require.NotEmpty(t, runReq.ExecID)
	require.Equal(t, "email:alice@example.com:<other@example.com>", runReq.ReplyContext.EmailSessionKey)
	require.Len(t, runReq.ChatHistory, 1)
	require.Equal(t, completedExec.ID, runReq.ChatHistory[0].ID)
	pending, err := repository.NewThreadInputRepo(db).ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestEmailService_SendResponsesDisabledSkipsReplies(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingPassword, "secret"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingIMAPHost, "imap.example.com"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingSMTPHost, "smtp.example.com"))
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingSendResponses, "false"))
	svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	called := false
	svc.sendMail = func(context.Context, EmailRuntimeConfig, string, string, string, string, string, string) error {
		called = true
		return nil
	}
	svc.SendTaskCompletionToThread(ctx, "alice@example.com", "<m@example.com>", "", "Question", "Task", "ok", "")
	assert.False(t, called)
}

func TestEmailService_SendTaskCompletionPreservesThreading(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	for k, v := range map[string]string{EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret", EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com", EmailSettingSendResponses: "true"} {
		require.NoError(t, settingsRepo.Set(ctx, k, v))
	}
	svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	var gotTo, gotSubject, gotInReplyTo, gotRefs string
	svc.sendMail = func(_ context.Context, _ EmailRuntimeConfig, to, subject, body, messageID, inReplyTo, references string) error {
		gotTo, gotSubject, gotInReplyTo, gotRefs = to, subject, inReplyTo, references
		assert.Contains(t, body, "Task completed")
		return nil
	}
	svc.SendTaskCompletionToThread(ctx, "alice@example.com", "<m@example.com>", "<root@example.com>", "Question", "Task", "done", "")
	assert.Equal(t, "alice@example.com", gotTo)
	assert.Equal(t, "Re: Question", gotSubject)
	assert.Equal(t, "<m@example.com>", gotInReplyTo)
	assert.Equal(t, "<root@example.com> <m@example.com>", gotRefs)
}

func TestEmailService_SendChatResponseRecordsOutboundMessageRef(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	for k, v := range map[string]string{EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret", EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com", EmailSettingSendResponses: "true"} {
		require.NoError(t, settingsRepo.Set(ctx, k, v))
	}
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Email Outbound Alias Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	taskRepo := repository.NewTaskRepo(db, nil)
	task := models.Task{ProjectID: project.ID, Title: "Email chat", Prompt: "request", Category: models.CategoryChat, Status: models.StatusCompleted, CreatedVia: models.TaskOriginEmail}
	require.NoError(t, taskRepo.Create(ctx, &task))
	contextRepo := repository.NewEmailTaskContextRepo(db)
	sessionKey := "email:alice@example.com:<root@example.com>"
	require.NoError(t, contextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: task.ID, EmailFrom: "alice@example.com", EmailMessageID: "<root@example.com>", EmailSubject: "Root", EmailSessionKey: sessionKey}))
	svc := NewEmailService(settingsRepo, projectRepo, repository.NewLLMConfigRepo(db), taskRepo, repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), contextRepo)
	var outboundMessageID string
	svc.sendMail = func(_ context.Context, _ EmailRuntimeConfig, _, _, _, messageID, _, _ string) error {
		outboundMessageID = messageID
		return nil
	}

	svc.SendChatResponse(ctx, task, "done", "")

	require.NotEmpty(t, outboundMessageID)
	resolved, err := contextRepo.ResolveOutboundMessageSessionKey(ctx, project.ID, "ALICE@example.com", outboundMessageID)
	require.NoError(t, err)
	assert.Equal(t, sessionKey, resolved)
}

func TestEmailService_SendOutboundMessage_NewEmailUsesSMTPWithoutReplyHeaders(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	for k, v := range map[string]string{EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret", EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com"} {
		require.NoError(t, settingsRepo.Set(ctx, k, v))
	}
	svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	var gotTo, gotSubject, gotBody, gotInReplyTo, gotRefs string
	svc.sendMail = func(_ context.Context, _ EmailRuntimeConfig, to, subject, body, messageID, inReplyTo, references string) error {
		gotTo, gotSubject, gotBody, gotInReplyTo, gotRefs = to, subject, body, inReplyTo, references
		return nil
	}
	res := svc.SendOutboundMessage(ctx, "Person <Person@Example.com>", "", "hello")
	require.True(t, res.OK)
	require.Equal(t, "person@example.com", gotTo)
	require.Equal(t, "OpenVibely", gotSubject)
	require.Equal(t, "hello", gotBody)
	require.Empty(t, gotInReplyTo)
	require.Empty(t, gotRefs)
}

func TestEmailService_OutboundPathsLoadOneCurrentSettingsSnapshot(t *testing.T) {
	tests := []struct {
		name string
		send func(*EmailService, models.Task)
	}{
		{name: "direct outbound", send: func(svc *EmailService, _ models.Task) {
			result := svc.SendOutboundMessage(context.Background(), "Person <person@example.com>", "Notice", "body")
			require.True(t, result.OK, result.Error)
		}},
		{name: "thread reply", send: func(svc *EmailService, _ models.Task) {
			svc.SendTaskCompletionToThread(context.Background(), "person@example.com", "<message@example.com>", "<root@example.com>", "Question", "Task", "done", "")
		}},
		{name: "chat reply", send: func(svc *EmailService, task models.Task) {
			svc.SendChatResponse(context.Background(), task, "done", "")
		}},
		{name: "task notification", send: func(svc *EmailService, task models.Task) {
			svc.SendTaskCompletionNotification(context.Background(), task, "done", "")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			settingsRepo := repository.NewSettingsRepo(db)
			for key, value := range map[string]string{
				EmailSettingProvider: EmailProviderCustom, EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret",
				EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com", EmailSettingSMTPPort: "2525", EmailSettingSendResponses: "true",
			} {
				require.NoError(t, settingsRepo.Set(ctx, key, value))
			}
			projectRepo := repository.NewProjectRepo(db)
			projects, err := projectRepo.List(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, projects)
			taskRepo := repository.NewTaskRepo(db, nil)
			task := models.Task{ProjectID: projects[0].ID, Title: "Email task", Prompt: "request", Category: models.CategoryActive, Status: models.StatusCompleted, CreatedVia: models.TaskOriginEmail}
			require.NoError(t, taskRepo.Create(ctx, &task))
			contextRepo := repository.NewEmailTaskContextRepo(db)
			require.NoError(t, contextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: task.ID, EmailFrom: "person@example.com", EmailMessageID: "<message@example.com>", EmailReferences: "<root@example.com>", EmailSubject: "Question"}))
			svc := NewEmailService(settingsRepo, projectRepo, repository.NewLLMConfigRepo(db), taskRepo, repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), contextRepo)

			var settingsSelects atomic.Int64
			settingsRepo.SetQueryObserver(func(query string) {
				if strings.Contains(query, "FROM app_settings") {
					settingsSelects.Add(1)
				}
			})
			var smtpCalls atomic.Int64
			svc.sendMail = func(_ context.Context, cfg EmailRuntimeConfig, to, subject, body, messageID, inReplyTo, references string) error {
				smtpCalls.Add(1)
				assert.Equal(t, "smtp.example.com", cfg.SMTPHost)
				assert.Equal(t, 2525, cfg.SMTPPort)
				assert.Equal(t, "person@example.com", to)
				if tt.name != "direct outbound" {
					assert.Equal(t, "Re: Question", subject)
					assert.Equal(t, "<message@example.com>", inReplyTo)
					assert.Equal(t, "<root@example.com> <message@example.com>", references)
				}
				return nil
			}

			tt.send(svc, task)
			assert.Equal(t, int64(1), smtpCalls.Load())
			assert.Equal(t, int64(1), settingsSelects.Load())
		})
	}
}

func TestEmailService_SettingsSnapshotsArePartialFreshAndPerOperation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	for key, value := range map[string]string{
		EmailSettingProvider: EmailProviderGmail,
		EmailSettingAddress:  " Bot@Example.com ",
		EmailSettingPassword: "abcd efgh ijkl mnop",
	} {
		require.NoError(t, settingsRepo.Set(ctx, key, value))
	}
	svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	var configs []EmailRuntimeConfig
	svc.sendMail = func(_ context.Context, cfg EmailRuntimeConfig, _, _, _, _, _, _ string) error {
		configs = append(configs, cfg)
		return nil
	}

	require.True(t, svc.SendOutboundMessage(ctx, "person@example.com", "first", "body").OK)
	require.Len(t, configs, 1)
	assert.Equal(t, "bot@example.com", configs[0].Address)
	assert.Equal(t, "abcdefghijklmnop", configs[0].Password)
	assert.Equal(t, "smtp.gmail.com", configs[0].SMTPHost)
	assert.Equal(t, 587, configs[0].SMTPPort)
	assert.Equal(t, 15*time.Second, configs[0].PollInterval)
	assert.True(t, configs[0].SendResponses)
	assert.False(t, configs[0].SkipAttachments)
	assert.True(t, configs[0].MarkExistingSeenOnStart)

	for key, value := range map[string]string{
		EmailSettingProvider: EmailProviderCustom, EmailSettingPassword: "custom secret", EmailSettingIMAPHost: "imap.changed.example.com",
		EmailSettingSMTPHost: "smtp.changed.example.com", EmailSettingIMAPPort: "1993", EmailSettingSMTPPort: "2465",
	} {
		require.NoError(t, settingsRepo.Set(ctx, key, value))
	}
	require.True(t, svc.SendOutboundMessage(ctx, "person@example.com", "second", "body").OK)
	require.Len(t, configs, 2)
	assert.Equal(t, EmailProviderCustom, configs[1].Provider)
	assert.Equal(t, "custom secret", configs[1].Password)
	assert.Equal(t, "imap.changed.example.com", configs[1].IMAPHost)
	assert.Equal(t, 1993, configs[1].IMAPPort)
	assert.Equal(t, "smtp.changed.example.com", configs[1].SMTPHost)
	assert.Equal(t, 2465, configs[1].SMTPPort)

	for _, key := range emailRuntimeSettingKeys {
		require.NoError(t, settingsRepo.Set(ctx, key, ""))
	}
	removed := svc.SendOutboundMessage(ctx, "person@example.com", "third", "body")
	assert.False(t, removed.OK)
	assert.Contains(t, removed.Error, "not fully configured")
	assert.Len(t, configs, 2, "removed settings must be visible without a stale SMTP handoff")
}

func TestEmailService_SettingsSnapshotHonorsCancellation(t *testing.T) {
	db := testutil.NewTestDB(t)
	svc := NewEmailService(repository.NewSettingsRepo(db), repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	called := false
	svc.sendMail = func(context.Context, EmailRuntimeConfig, string, string, string, string, string, string) error {
		called = true
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := svc.SendOutboundMessage(ctx, "person@example.com", "subject", "body")
	assert.False(t, result.OK)
	assert.False(t, called)
}

func TestEmailService_SendOutboundMessage_ValidationAndMissingConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	svc := NewEmailService(repository.NewSettingsRepo(db), repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	invalid := svc.SendOutboundMessage(context.Background(), "not-an-email", "Subject", "body")
	require.False(t, invalid.OK)
	require.Contains(t, invalid.Error, "invalid email recipient")
	missing := svc.SendOutboundMessage(context.Background(), "person@example.com", "Subject", "body")
	require.False(t, missing.OK)
	require.Contains(t, missing.Error, "email channel is not fully configured")
}

func TestSettingsQueryAcquiredObserverRunsWhileConnectionIsHeld(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, EmailSettingAddress, "bot@example.com"))

	acquired := make(chan struct{})
	release := make(chan struct{})
	settingsRepo.SetQueryAcquiredObserver(func(query string) {
		if strings.Contains(query, "FROM app_settings") {
			close(acquired)
			<-release
		}
	})

	loadDone := make(chan error, 1)
	go func() {
		_, err := settingsRepo.GetMany(ctx, emailRuntimeSettingKeys)
		loadDone <- err
	}()
	<-acquired

	waitCount := db.Stats().WaitCount
	competingDone := make(chan error, 1)
	go func() {
		var value string
		competingDone <- db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", EmailSettingAddress).Scan(&value)
	}()
	require.Eventually(t, func() bool { return db.Stats().WaitCount > waitCount }, time.Second, time.Millisecond,
		"competing query must queue behind the settings statement before the observer releases it")
	close(release)
	require.NoError(t, <-loadDone)
	require.NoError(t, <-competingDone)
}

func BenchmarkEmailServiceSettingsBurst(b *testing.B) {
	db := testutil.NewTestDB(b)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	for key, value := range map[string]string{
		EmailSettingProvider: EmailProviderCustom, EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret",
		EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com", EmailSettingSendResponses: "true",
	} {
		require.NoError(b, settingsRepo.Set(ctx, key, value))
	}
	svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	svc.sendMail = func(context.Context, EmailRuntimeConfig, string, string, string, string, string, string) error {
		return nil
	}
	var settingsSelects atomic.Int64
	settingsRepo.SetQueryObserver(func(query string) {
		if strings.Contains(query, "FROM app_settings") {
			settingsSelects.Add(1)
		}
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for n := 0; n < 100; n++ {
			require.True(b, svc.SendOutboundMessage(ctx, "person@example.com", "Notice", "body").OK)
		}
		for n := 0; n < 100; n++ {
			svc.SendTaskCompletionToThread(ctx, "person@example.com", "<message@example.com>", "<root@example.com>", "Question", "Task", "done", "")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(settingsSelects.Load())/float64(b.N), "settings_selects/op")
}

func BenchmarkEmailServiceSettingsContention(b *testing.B) {
	const (
		emailOperations  = 100
		unrelatedQueries = 100
	)
	b.Run("current", func(b *testing.B) {
		db := testutil.NewTestDB(b)
		ctx := context.Background()
		settingsRepo := repository.NewSettingsRepo(db)
		for key, value := range map[string]string{
			EmailSettingProvider: EmailProviderCustom, EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret",
			EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com", EmailSettingSendResponses: "true",
		} {
			require.NoError(b, settingsRepo.Set(ctx, key, value))
		}
		svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
		var settingsSelects atomic.Int64
		settingsRepo.SetQueryObserver(func(query string) {
			if strings.Contains(query, "FROM app_settings") {
				settingsSelects.Add(1)
			}
		})
		var smtpCalls atomic.Int64
		svc.sendMail = func(context.Context, EmailRuntimeConfig, string, string, string, string, string, string) error {
			smtpCalls.Add(1)
			return nil
		}

		waits := make([]time.Duration, 0, b.N*unrelatedQueries)
		totals := make([]time.Duration, 0, b.N)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			acquired := make(chan struct{})
			release := make(chan struct{})
			var blockFirst sync.Once
			settingsRepo.SetQueryAcquiredObserver(func(query string) {
				if strings.Contains(query, "FROM app_settings") {
					blockFirst.Do(func() {
						close(acquired)
						<-release
					})
				}
			})

			iterationStart := time.Now()
			emailDone := make(chan error, 1)
			go func() {
				for n := 0; n < emailOperations; n++ {
					if n%2 == 0 {
						result := svc.SendOutboundMessage(ctx, "person@example.com", "Notice", "body")
						if !result.OK {
							emailDone <- fmt.Errorf("direct outbound failed: %s", result.Error)
							return
						}
					} else {
						svc.SendTaskCompletionToThread(ctx, "person@example.com", "<message@example.com>", "<root@example.com>", "Question", "Task", "done", "")
					}
				}
				emailDone <- nil
			}()
			<-acquired

			waitCount := db.Stats().WaitCount
			waitResults := make(chan time.Duration, unrelatedQueries)
			var unrelated sync.WaitGroup
			unrelated.Add(unrelatedQueries)
			for n := 0; n < unrelatedQueries; n++ {
				go func() {
					defer unrelated.Done()
					waitStart := time.Now()
					var value string
					err := db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", EmailSettingAddress).Scan(&value)
					if err != nil {
						b.Errorf("unrelated query failed: %v", err)
					}
					waitResults <- time.Since(waitStart)
				}()
			}
			require.Eventually(b, func() bool {
				return db.Stats().WaitCount >= waitCount+unrelatedQueries
			}, time.Second, time.Millisecond, "all unrelated queries must queue behind acquired settings query")
			close(release)
			unrelated.Wait()
			require.NoError(b, <-emailDone)
			totals = append(totals, time.Since(iterationStart))
			close(waitResults)
			for wait := range waitResults {
				waits = append(waits, wait)
			}
		}
		b.StopTimer()
		settingsRepo.SetQueryAcquiredObserver(nil)
		require.Equal(b, int64(b.N*emailOperations), smtpCalls.Load(), "every measured operation must reach the stubbed SMTP handoff")
		expectedSettingsSelects := int64(b.N * emailOperations)
		require.Equal(b, expectedSettingsSelects, settingsSelects.Load(), "measured service path must match the settings-read ledger")
		b.ReportMetric(float64(settingsSelects.Load())/float64(b.N), "settings_selects/op")
		sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
		sort.Slice(totals, func(i, j int) bool { return totals[i] < totals[j] })
		if len(waits) > 0 {
			b.ReportMetric(float64(waits[len(waits)/2].Nanoseconds()), "median_wait_ns")
			b.ReportMetric(float64(waits[(len(waits)-1)*95/100].Nanoseconds()), "p95_wait_ns")
		}
		if len(totals) > 0 {
			b.ReportMetric(float64(totals[len(totals)/2].Nanoseconds()), "median_total_ns")
			b.ReportMetric(float64(totals[(len(totals)-1)*95/100].Nanoseconds()), "p95_total_ns")
		}
	})
}

func TestEmailProviderSettingsAndHelpers(t *testing.T) {
	presets := EmailProviderPresets()
	require.Len(t, presets, 6)
	require.Equal(t, EmailProviderGmail, presets[0].Key)
	require.Equal(t, EmailProviderCustom, NormalizeEmailProvider("unknown"))
	require.Equal(t, "abcd", NormalizeEmailPasswordForProvider(EmailProviderGmail, " ab cd "))
	require.Equal(t, "ab cd", NormalizeEmailPasswordForProvider(EmailProviderCustom, " ab cd "))

	provider, imapHost, imapPort, smtpHost, smtpPort, err := ResolveEmailProviderSettings(" gmail ", "ignored", "1", "ignored", "2")
	require.NoError(t, err)
	require.Equal(t, EmailProviderGmail, provider)
	require.Equal(t, "imap.gmail.com", imapHost)
	require.Equal(t, 993, imapPort)
	require.Equal(t, "smtp.gmail.com", smtpHost)
	require.Equal(t, 587, smtpPort)

	provider, imapHost, imapPort, smtpHost, smtpPort, err = ResolveEmailProviderSettings(EmailProviderCustom, " imap.example.com ", "1143", " smtp.example.com ", "2525")
	require.NoError(t, err)
	require.Equal(t, EmailProviderCustom, provider)
	require.Equal(t, "imap.example.com", imapHost)
	require.Equal(t, 1143, imapPort)
	require.Equal(t, "smtp.example.com", smtpHost)
	require.Equal(t, 2525, smtpPort)

	_, _, _, _, _, err = ResolveEmailProviderSettings(EmailProviderCustom, "", "993", "smtp.example.com", "587")
	require.ErrorContains(t, err, "requires IMAP and SMTP hosts")
	_, _, _, _, _, err = ResolveEmailProviderSettings(EmailProviderCustom, "imap.example.com", "0", "smtp.example.com", "587")
	require.ErrorContains(t, err, "invalid IMAP port")
	_, _, _, _, _, err = ResolveEmailProviderSettings(EmailProviderCustom, "imap.example.com", "993", "smtp.example.com", "70000")
	require.ErrorContains(t, err, "invalid SMTP port")

	require.Equal(t, "Hello  world", stripHTML(" <p>Hello <b>world</b></p> "))
	require.Equal(t, "plain", firstNonEmpty(" plain ", "fallback"))
	require.Equal(t, "fallback", firstNonEmpty(" ", " fallback "))
	require.True(t, emailIncomingAttachmentsRequireVision([]EmailInboundAttachment{{ContentType: "image/png; name=a.png"}}))
	require.True(t, emailIncomingAttachmentsRequireVision([]EmailInboundAttachment{{ContentType: "application/octet-stream"}}))
	require.True(t, emailIncomingAttachmentsRequireVision([]EmailInboundAttachment{{ContentType: ""}}))
	require.False(t, emailIncomingAttachmentsRequireVision([]EmailInboundAttachment{{ContentType: "text/plain"}}))
}

func TestEmailServiceLifecycleStatusWithConfigLoader(t *testing.T) {
	ctx := context.Background()
	svc := &EmailService{}
	svc.configLoader = func(context.Context) (EmailRuntimeConfig, error) {
		return EmailRuntimeConfig{}, nil
	}
	require.NoError(t, svc.Start())
	require.False(t, svc.IsRunning())
	require.Equal(t, EmailConnectionStatus{}, svc.GetConnectionStatus(ctx))
	require.ErrorContains(t, svc.TestConnection(ctx), "email channel is not fully configured")
	require.NoError(t, svc.ReloadFromSettings(ctx))
	svc.Stop()

	svc.configLoader = func(context.Context) (EmailRuntimeConfig, error) {
		return EmailRuntimeConfig{Provider: EmailProviderCustom, Address: "bot@example.com", Password: "secret", IMAPHost: "imap.example.com", IMAPPort: 993, SMTPHost: "smtp.example.com", SMTPPort: 587}, nil
	}
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) {
		return &fakeEmailIMAPClient{}, nil
	}
	require.NoError(t, svc.TestConnection(ctx))
	status := svc.GetConnectionStatus(ctx)
	require.True(t, status.Configured)
	require.Equal(t, "bot@example.com", status.Address)
	require.Equal(t, "imap.example.com", status.IMAPHost)
	require.Equal(t, 993, status.IMAPPort)
}

func TestEmailServiceReloadFromSettingsUsesNewPollSnapshot(t *testing.T) {
	oldConfig := EmailRuntimeConfig{
		Provider:                EmailProviderCustom,
		Address:                 "old@example.com",
		Password:                "old-secret",
		IMAPHost:                "imap.example.com",
		SMTPHost:                "smtp.example.com",
		PollInterval:            time.Hour,
		MarkExistingSeenOnStart: false,
	}
	newConfig := oldConfig
	newConfig.Address = "new@example.com"
	newConfig.Password = "new-secret"
	removedConfig := EmailRuntimeConfig{}

	var loadCalls atomic.Int32
	observedAddresses := make(chan string, 4)
	oldClient := newFakeEmailIMAPClient(testIMAPMessageWithBody(1, "old self", "old@example.com", "old body"))
	newClient := newFakeEmailIMAPClient(testIMAPMessageWithBody(1, "new self", "new@example.com", "new body"))
	svc := &EmailService{}
	svc.configLoader = func(context.Context) (EmailRuntimeConfig, error) {
		switch loadCalls.Add(1) {
		case 1:
			return oldConfig, nil
		case 2:
			return newConfig, nil
		default:
			return removedConfig, nil
		}
	}
	svc.connectIMAP = func(_ context.Context, cfg EmailRuntimeConfig) (emailIMAPClient, error) {
		observedAddresses <- cfg.Address
		switch cfg.Address {
		case oldConfig.Address:
			return oldClient, nil
		case newConfig.Address:
			return newClient, nil
		default:
			return newFakeEmailIMAPClient(), nil
		}
	}

	require.NoError(t, svc.Start())
	defer svc.Stop()
	select {
	case address := <-observedAddresses:
		require.Equal(t, "old@example.com", address)
	case <-time.After(time.Second):
		t.Fatal("initial poll did not observe the loaded address")
	}
	require.Eventually(t, func() bool {
		seen := oldClient.seenIDs()
		return len(seen) == 1 && seen[0] == 1
	}, time.Second, time.Millisecond, "initial poll must filter using its loaded address snapshot")

	require.NoError(t, svc.ReloadFromSettings(context.Background()))
	select {
	case address := <-observedAddresses:
		require.Equal(t, "new@example.com", address)
	case <-time.After(time.Second):
		t.Fatal("reloaded poll did not observe the new address")
	}
	require.Eventually(t, func() bool {
		seen := newClient.seenIDs()
		return len(seen) == 1 && seen[0] == 1
	}, time.Second, time.Millisecond, "reloaded poll must filter using the new address snapshot")

	require.NoError(t, svc.ReloadFromSettings(context.Background()))
	assert.False(t, svc.IsRunning(), "removing the address must stop the poller instead of retaining the prior snapshot")
}

func TestEmailService_CompleteExecutionUsesSharedChatPromotion(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	project := &models.Project{Name: "Email Promotion Project"}
	require.NoError(t, repository.NewProjectRepo(db).Create(ctx, project))
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "Email Promotion Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	agentID := agent.ID

	chatTask := &models.Task{ProjectID: project.ID, Title: "Email Chat", Prompt: "chat", Category: models.CategoryChat, Status: models.StatusRunning, CreatedVia: models.TaskOriginEmail, AgentID: &agentID}
	require.NoError(t, taskRepo.Create(ctx, chatTask))
	chatExec := &models.Execution{TaskID: chatTask.ID, AgentConfigID: agentID, Status: models.ExecRunning, PromptSent: "chat"}
	require.NoError(t, execRepo.Create(ctx, chatExec))

	nonChatTask := &models.Task{ProjectID: project.ID, Title: "Email Task", Prompt: "task", Category: models.CategoryActive, Status: models.StatusRunning, CreatedVia: models.TaskOriginEmail, AgentID: &agentID}
	require.NoError(t, taskRepo.Create(ctx, nonChatTask))
	nonChatExec := &models.Execution{TaskID: nonChatTask.ID, AgentConfigID: agentID, Status: models.ExecRunning, PromptSent: "task"}
	require.NoError(t, execRepo.Create(ctx, nonChatExec))

	var promoted []string
	completeExecution := channelCompletionFunc("email", execRepo, taskRepo, nil, func(projectID string) { promoted = append(promoted, projectID) })

	completeExecution(ctx, nonChatExec.ID, nonChatTask.ID, "done", "", 1, 10)
	require.Empty(t, promoted)

	completeExecution(ctx, chatExec.ID, chatTask.ID, "done", "", 1, 10)
	require.Equal(t, []string{project.ID}, promoted)
}

// TestEmailServiceProcessIncomingPassesChannelRuntimeTools verifies that
// processIncomingMessage populates FirstTurn.RuntimeTools so the channel-specific
// switch_project executor (with persistence) is wired into the ChannelChatRunRequest
// handed to the handler runner.
func TestEmailServiceProcessIncomingPassesChannelRuntimeTools(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)

	project := &models.Project{Name: "Email RT Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "rt@example.com", AddedBy: "test"}))

	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, nil)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, nil, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })

	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "rt@example.com",
		Subject:     "Hello",
		Body:        "hi there",
		MessageID:   "<rt-1@example.com>",
	})

	require.NotNil(t, got.RuntimeTools, "RuntimeTools must be non-nil for email channel turns")
	require.NotEmpty(t, got.RuntimeTools.Definitions, "RuntimeTools must carry definitions")
	require.NotNil(t, got.RuntimeTools.Executor, "RuntimeTools must have an executor")
}

// TestEmailServiceSwitchProjectViaRuntimeToolsPersists verifies that calling
// switch_project through the channel RuntimeTools executor (built by buildEmailActionToolRuntime)
// actually persists the sender's active project in the email_sender_projects table.
func TestEmailServiceSwitchProjectViaRuntimeToolsPersists(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)

	project1 := &models.Project{Name: "Default Email Project"}
	project2 := &models.Project{Name: "Target Email Project"}
	require.NoError(t, projectRepo.Create(ctx, project1))
	require.NoError(t, projectRepo.Create(ctx, project2))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project1.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))

	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, nil)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, nil, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })

	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "alice@example.com",
		Subject:     "Switch please",
		Body:        "switch project",
		MessageID:   "<alice-1@example.com>",
	})

	require.NotNil(t, got.RuntimeTools, "RuntimeTools must be non-nil for email channel turns")

	// Call switch_project through the channel runtime executor.
	result, handled, _, err := got.RuntimeTools.Executor(ctx, "switch_project", []byte(`{"project":"Target Email Project"}`))
	require.NoError(t, err)
	require.True(t, handled, "switch_project must be handled by email channel executor")
	require.Contains(t, result, "Target Email Project", "result must mention the project")
	require.Contains(t, result, "Future messages", "result must include same-turn semantics message")

	// Verify the selection was persisted.
	saved, err := emailSenderProjectRepo.GetSenderProject(ctx, "alice@example.com")
	require.NoError(t, err)
	require.Equal(t, project2.ID, saved, "switch_project must have persisted selection to email_sender_projects")
}

func TestEmailServiceSwitchProjectViaRuntimeToolsNormalizesDisplayNameSender(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)

	project1 := &models.Project{Name: "Default Display Email Project"}
	project2 := &models.Project{Name: "Target Display Email Project"}
	require.NoError(t, projectRepo.Create(ctx, project1))
	require.NoError(t, projectRepo.Create(ctx, project2))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project1.ID, EmailAddress: "sender@example.test", AddedBy: "test"}))

	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, nil)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, nil, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })

	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "Test Sender <sender@example.test>",
		Subject:     "Switch please",
		Body:        "switch project",
		MessageID:   "<james-display-1@example.com>",
	})

	require.NotNil(t, got.RuntimeTools, "RuntimeTools must be non-nil for email channel turns")

	result, handled, _, err := got.RuntimeTools.Executor(ctx, "switch_project", []byte(`{"project":"Target Display Email Project"}`))
	require.NoError(t, err)
	require.True(t, handled, "switch_project must be handled by email channel executor")
	require.Contains(t, result, "Target Display Email Project")
	require.NotContains(t, result, "not authorized")

	saved, err := emailSenderProjectRepo.GetSenderProject(ctx, "sender@example.test")
	require.NoError(t, err)
	require.Equal(t, project2.ID, saved, "selection must persist under normalized sender address")

	resolved := svc.resolveAuthorizedProject(ctx, "Test Sender <sender@example.test>")
	require.Equal(t, project2.ID, resolved, "future display-name messages must use the saved normalized sender project")
}

func TestEmailServiceSwitchProjectViaRuntimeToolsAllowsSystemAuthorizedSenderAcrossProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)

	project1 := &models.Project{Name: "openvibely"}
	project2 := &models.Project{Name: "Default Project"}
	require.NoError(t, projectRepo.Create(ctx, project1))
	require.NoError(t, projectRepo.Create(ctx, project2))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project1.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))

	agent := &models.LLMConfig{Name: "Email Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, nil)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, nil, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	svc.SetThreadInputRepo(repository.NewThreadInputRepo(db))

	var got ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { got = req })

	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "Alice <alice@example.com>",
		Subject:     "Switch please",
		Body:        "switch project",
		MessageID:   "<alice-unauth-1@example.com>",
	})

	require.NotNil(t, got.RuntimeTools, "RuntimeTools must be non-nil for email channel turns")

	result, handled, _, err := got.RuntimeTools.Executor(ctx, "switch_project", []byte(`{"project":"Default Project"}`))
	require.NoError(t, err)
	require.True(t, handled, "switch_project must be handled by email channel executor")
	require.Contains(t, result, "Default")
	require.NotContains(t, result, "not authorized")

	saved, err := emailSenderProjectRepo.GetSenderProject(ctx, "alice@example.com")
	require.NoError(t, err)
	require.Equal(t, project2.ID, saved, "system-authorized sender must be able to switch to another project")
}

// TestEmailServiceResolveAuthorizedProjectUsesSavedSelection verifies that
// resolveAuthorizedProject returns the sender's saved active project (from
// email_sender_projects) instead of the first authorized project in the list.
func TestEmailServiceResolveAuthorizedProjectUsesSavedSelection(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)

	project1 := &models.Project{Name: "First Project"}
	project2 := &models.Project{Name: "Second Project"}
	require.NoError(t, projectRepo.Create(ctx, project1))
	require.NoError(t, projectRepo.Create(ctx, project2))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project1.ID, EmailAddress: "bob@example.com", AddedBy: "test"}))

	// Save project2 as the active project for bob.
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "bob@example.com", project2.ID))

	svc := NewEmailService(settingsRepo, projectRepo, nil, nil, nil, nil, nil, nil, nil, emailAuthRepo, nil)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)

	resolved := svc.resolveAuthorizedProject(ctx, "bob@example.com")
	require.Equal(t, project2.ID, resolved, "resolveAuthorizedProject must return the saved active project, not the first one")
}
func buildEmailMultipartFixture(t *testing.T, png []byte, withAttachment bool) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("From: Alice <alice@example.com>\r\n")
	b.WriteString("Subject: Photo\r\n")
	b.WriteString("Message-ID: <photo@example.com>\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n\r\n")
	b.WriteString("--BOUND\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString("Here is a photo\r\n")
	if withAttachment {
		encoded := base64.StdEncoding.EncodeToString(png)
		b.WriteString("--BOUND\r\n")
		b.WriteString("Content-Type: image/png\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"shot.png\"\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		// wrap at 76 chars per MIME convention
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			b.WriteString(encoded[i:end] + "\r\n")
		}
	}
	b.WriteString("--BOUND--\r\n")
	return []byte(b.String())
}

func TestReadEmailParts_ExtractsDecodedAttachmentAndBody(t *testing.T) {
	raw := buildEmailMultipartFixture(t, slackTestPNGBytes, true)
	mr, err := messagemail.CreateReader(bytes.NewReader(raw))
	require.NoError(t, err)
	body, attachments := readEmailParts(mr, false)
	require.Equal(t, "Here is a photo", body)
	require.Len(t, attachments, 1)
	require.Equal(t, "shot.png", attachments[0].FileName)
	require.Equal(t, "image/png", attachments[0].ContentType)
	require.Equal(t, slackTestPNGBytes, attachments[0].Data, "attachment bytes must be base64-decoded back to original PNG")
}

func TestReadEmailParts_SkipAttachmentsDropsAttachmentParts(t *testing.T) {
	raw := buildEmailMultipartFixture(t, slackTestPNGBytes, true)
	mr, err := messagemail.CreateReader(bytes.NewReader(raw))
	require.NoError(t, err)
	body, attachments := readEmailParts(mr, true)
	require.Equal(t, "Here is a photo", body)
	require.Empty(t, attachments, "attachments must be dropped when skipAttachments is true")
}

func newEmailAttachmentTestService(t *testing.T) (*EmailService, *repository.ChatAttachmentRepo, *repository.ThreadInputRepo, *models.Project, *models.LLMConfig, *models.LLMConfig) {
	t.Helper()
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	emailAuthRepo := repository.NewEmailAuthRepo(db)
	emailTaskContextRepo := repository.NewEmailTaskContextRepo(db)
	project := &models.Project{Name: "Email Attachment Project", IsDefault: true}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, emailAuthRepo.Create(ctx, &models.EmailAuthorizedSender{ProjectID: project.ID, EmailAddress: "alice@example.com", AddedBy: "test"}))
	emailSenderProjectRepo := repository.NewEmailSenderProjectRepo(db)
	require.NoError(t, emailSenderProjectRepo.SetSenderProject(ctx, "alice@example.com", project.ID))
	defaultAgent := &models.LLMConfig{Name: "text-cli", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet-4-5", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, defaultAgent))
	visionAgent := &models.LLMConfig{Name: "vision", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-3-5-sonnet-20241022", APIKey: "key"}
	require.NoError(t, llmConfigRepo.Create(ctx, visionAgent))
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc := NewEmailService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, repository.NewScheduleRepo(db), taskSvc, llmSvc, workerSvc, emailAuthRepo, emailTaskContextRepo)
	svc.SetEmailSenderProjectRepo(emailSenderProjectRepo)
	threadInputRepo := repository.NewThreadInputRepo(db)
	svc.SetThreadInputRepo(threadInputRepo)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	svc.SetChatAttachmentRepo(chatAttachmentRepo)
	svc.SetUploadsDir(t.TempDir())
	return svc, chatAttachmentRepo, threadInputRepo, project, defaultAgent, visionAgent
}

func TestEmailService_FirstTurnLinksImageAttachmentAndSelectsVisionModel(t *testing.T) {
	svc, chatAttachmentRepo, _, _, _, visionAgent := newEmailAttachmentTestService(t)
	ctx := context.Background()
	var runReq ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runReq = req })
	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "alice@example.com",
		Subject:     "Photo",
		Body:        "what is in this screenshot?",
		MessageID:   "<photo@example.com>",
		Attachments: []EmailInboundAttachment{{FileName: "shot.png", ContentType: "image/png", Data: slackTestPNGBytes}},
	})
	require.NotEmpty(t, runReq.ExecID, "expected a first-turn execution")
	require.Equal(t, visionAgent.ID, runReq.Agent.ID, "image attachment must drive vision model selection")
	require.Len(t, runReq.ImageAttachments, 1)
	require.Equal(t, "shot.png", runReq.ImageAttachments[0].FileName)
	linked, err := chatAttachmentRepo.ListByExecution(ctx, runReq.ExecID)
	require.NoError(t, err)
	require.Len(t, linked, 1, "image attachment must be linked to the execution")
	require.Equal(t, "image/png", linked[0].MediaType)
	data, err := os.ReadFile(linked[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, slackTestPNGBytes, data, "persisted attachment bytes must match the source PNG")
}

func TestEmailService_EmptyBodyWithAttachmentCreatesAndLinksFirstTurn(t *testing.T) {
	svc, chatAttachmentRepo, _, _, _, visionAgent := newEmailAttachmentTestService(t)
	ctx := context.Background()
	var runReq ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runReq = req })
	// Attachment-only email: empty body must still reach the ingress pipeline.
	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "alice@example.com",
		Subject:     "Photo",
		Body:        "",
		MessageID:   "<nobody@example.com>",
		Attachments: []EmailInboundAttachment{{FileName: "shot.png", ContentType: "image/png", Data: slackTestPNGBytes}},
	})
	require.NotEmpty(t, runReq.ExecID, "empty-body email with an attachment must still create a first-turn execution")
	require.Equal(t, visionAgent.ID, runReq.Agent.ID, "image attachment must drive vision model selection even with empty body")
	require.Len(t, runReq.ImageAttachments, 1)
	linked, err := chatAttachmentRepo.ListByExecution(ctx, runReq.ExecID)
	require.NoError(t, err)
	require.Len(t, linked, 1, "attachment must be linked to the execution")
	require.Equal(t, "image/png", linked[0].MediaType)
}

func TestEmailService_EmptyBodyNoAttachmentIsIgnored(t *testing.T) {
	svc, _, _, _, _, _ := newEmailAttachmentTestService(t)
	ctx := context.Background()
	called := false
	svc.SetChannelChatRunner(func(context.Context, ChannelChatRunRequest) { called = true })
	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "alice@example.com",
		Subject:     "Empty",
		Body:        "",
		MessageID:   "<empty@example.com>",
	})
	require.False(t, called, "empty-body email with no attachments must still be ignored")
}

func TestEmailService_OctetStreamImageBytesClassifiedAsImage(t *testing.T) {
	svc, chatAttachmentRepo, _, _, _, visionAgent := newEmailAttachmentTestService(t)
	ctx := context.Background()
	var runReq ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, req ChannelChatRunRequest) { runReq = req })
	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "alice@example.com",
		Subject:     "Mislabeled",
		Body:        "look at this",
		MessageID:   "<mislabel@example.com>",
		Attachments: []EmailInboundAttachment{{FileName: "data.bin", ContentType: "application/octet-stream", Data: slackTestPNGBytes}},
	})
	require.NotEmpty(t, runReq.ExecID)
	require.Equal(t, visionAgent.ID, runReq.Agent.ID, "octet-stream bytes that sniff as PNG must select vision model")
	linked, err := chatAttachmentRepo.ListByExecution(ctx, runReq.ExecID)
	require.NoError(t, err)
	require.Len(t, linked, 1)
	require.Equal(t, "image/png", linked[0].MediaType, "media type must be corrected from sniffed bytes")
}

func TestEmailService_QueuedChatAttachmentStoresPendingSession(t *testing.T) {
	svc, _, threadInputRepo, project, defaultAgent, visionAgent := newEmailAttachmentTestService(t)
	ctx := context.Background()
	// Seed an active chat execution for the same email session so the next message queues.
	taskRepo := svc.taskRepo
	execRepo := svc.execRepo
	sessionKey := EmailSessionKey("alice@example.com", "<root@example.com>", "", "", "Photo")
	activeTask := &models.Task{ProjectID: project.ID, Title: "Root", Prompt: "root", Category: models.CategoryChat, Status: models.StatusRunning, CreatedVia: models.TaskOriginEmail, AgentID: &defaultAgent.ID}
	require.NoError(t, taskRepo.Create(ctx, activeTask))
	require.NoError(t, svc.emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: activeTask.ID, EmailFrom: "alice@example.com", EmailMessageID: "<root@example.com>", EmailSubject: "Photo", EmailSessionKey: sessionKey}))
	activeExec := &models.Execution{TaskID: activeTask.ID, AgentConfigID: defaultAgent.ID, Status: models.ExecRunning, PromptSent: "root"}
	require.NoError(t, execRepo.Create(ctx, activeExec))

	svc.SetChannelChatRunner(func(context.Context, ChannelChatRunRequest) {})
	svc.ProcessIncoming(ctx, EmailInboundMessage{
		FromAddress: "alice@example.com",
		Subject:     "Photo",
		Body:        "another screenshot",
		MessageID:   "<photo2@example.com>",
		References:  "<root@example.com>",
		Attachments: []EmailInboundAttachment{{FileName: "shot.png", ContentType: "image/png", Data: slackTestPNGBytes}},
	})

	pending, err := threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1, "expected the attachment-bearing email to queue")
	require.Equal(t, visionAgent.ID, pending[0].AgentConfigID, "queued input must persist the vision model from sniffed bytes")
	require.NotEmpty(t, pending[0].AttachmentSessionID, "queued input must reference a pending attachment session")
	pendingDir := filepath.Join(svc.emailUploadsDir(), "chat", "pending", pending[0].AttachmentSessionID)
	entries, err := os.ReadDir(pendingDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one staged attachment expected")
}
