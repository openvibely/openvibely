package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
	messagemail "github.com/emersion/go-message/mail"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailPollOnceDoesNotRepeatDurableHandoffAfterStoreFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	client := newFakeEmailIMAPClient(testIMAPMessage(1, "durable", "alice@example.com"))
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
	require.Equal(t, 1, processed)

	newService().pollOnce(context.Background(), cfg)
	assert.Equal(t, []uint32{1}, client.seenIDs())
	assert.Equal(t, 1, processed)
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
	svc.emailInboundReceiptStore = &fakeEmailInboundReceiptStore{keys: make(map[string]bool)}
	client := newFakeEmailIMAPClient(
		testIMAPMessageWithBody(1, "successful", "alice@example.com", "normal request"),
		testIMAPMessageWithBody(2, "retry", "alice@example.com", "transient task failure"),
	)
	svc.connectIMAP = func(context.Context, EmailRuntimeConfig) (emailIMAPClient, error) { return client, nil }
	cfg := EmailRuntimeConfig{Address: "bot@example.com"}

	svc.pollOnce(ctx, cfg)
	assert.Equal(t, []uint32{1}, client.seenIDs())
	tasks, err := taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Contains(t, tasks[0].Prompt, "normal request")

	_, err = db.Exec(`DROP TRIGGER fail_email_task_insert`)
	require.NoError(t, err)
	client.messages[2] = testIMAPMessageWithBody(2, "retry", "alice@example.com", "transient task failure")
	svc.pollOnce(ctx, cfg)
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	tasks, err = taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	assert.Len(t, tasks, 2)
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
	assert.Equal(t, map[string]int{"success": 1, "retry": 1}, attempts)

	svc.pollOnce(context.Background(), EmailRuntimeConfig{})
	assert.Equal(t, []uint32{1, 2}, client.seenIDs())
	assert.Equal(t, map[string]int{"success": 1, "retry": 2}, attempts)
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
	assert.Zero(t, processed)
	assert.Equal(t, 2, client.fetchCount)
}

type fakeEmailInboundReceiptStore struct {
	keys map[string]bool
}

func (s *fakeEmailInboundReceiptStore) Exists(_ context.Context, mailbox, messageKey string) (bool, error) {
	return s.keys[mailbox+"\x00"+messageKey], nil
}

func (s *fakeEmailInboundReceiptStore) Record(_ context.Context, mailbox, messageKey string) error {
	s.keys[mailbox+"\x00"+messageKey] = true
	return nil
}

type fakeEmailIMAPClient struct {
	messages      map[uint32]*imap.Message
	seen          map[uint32]bool
	fetchCount    int
	storeFailures int
}

func newFakeEmailIMAPClient(messages ...*imap.Message) *fakeEmailIMAPClient {
	client := &fakeEmailIMAPClient{messages: make(map[uint32]*imap.Message), seen: make(map[uint32]bool)}
	for _, msg := range messages {
		client.messages[msg.SeqNum] = msg
	}
	return client
}

func testIMAPMessage(id uint32, subject, from string) *imap.Message {
	msg := &imap.Message{SeqNum: id, Envelope: &imap.Envelope{Subject: subject, MessageId: fmt.Sprintf("<message-%d@example.com>", id)}}
	if from != "" {
		parts := strings.SplitN(from, "@", 2)
		msg.Envelope.From = []*imap.Address{{MailboxName: parts[0], HostName: parts[1]}}
	}
	return msg
}

func testIMAPMessageWithBody(id uint32, subject, from, body string) *imap.Message {
	msg := testIMAPMessage(id, subject, from)
	raw := fmt.Sprintf("From: %s\r\nSubject: %s\r\nMessage-ID: <message-%d@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", from, subject, id, body)
	msg.Body = map[*imap.BodySectionName]imap.Literal{&imap.BodySectionName{}: bytes.NewBufferString(raw)}
	return msg
}

func (c *fakeEmailIMAPClient) Login(string, string) error { return nil }
func (c *fakeEmailIMAPClient) Select(string, bool) (*imap.MailboxStatus, error) {
	return &imap.MailboxStatus{}, nil
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
func (c *fakeEmailIMAPClient) Fetch(seqset *imap.SeqSet, _ []imap.FetchItem, ch chan *imap.Message) error {
	c.fetchCount++
	defer close(ch)
	for id := uint32(1); id <= uint32(len(c.messages)); id++ {
		if seqset.Contains(id) {
			ch <- c.messages[id]
		}
	}
	return nil
}
func (c *fakeEmailIMAPClient) Store(seqset *imap.SeqSet, _ imap.StoreItem, _ interface{}, _ chan *imap.Message) error {
	if c.storeFailures > 0 {
		c.storeFailures--
		return fmt.Errorf("transient store failure")
	}
	for id := range c.messages {
		if seqset.Contains(id) {
			c.seen[id] = true
		}
	}
	return nil
}
func (c *fakeEmailIMAPClient) Logout() error { return nil }
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

func TestEmailThreadingHelpers(t *testing.T) {
	msg := EmailInboundMessage{FromName: "Alice", FromAddress: "alice@example.com", Subject: "Deploy question", Body: "What now?", MessageID: "<m2@example.com>", References: "<root@example.com> <m1@example.com>"}
	assert.Equal(t, "[Email from: Alice <alice@example.com>]\n[Subject: Deploy question]\n\nWhat now?", BuildEmailPrompt(msg))
	assert.Equal(t, "email:alice@example.com:<root@example.com>", EmailSessionKey("Alice@Example.com", msg.MessageID, msg.References, msg.Subject))
	assert.Equal(t, "Re: Deploy question", replySubject("Deploy question"))
	assert.Equal(t, "Re: Deploy question", replySubject("Re: Deploy question"))
	assert.Equal(t, "<root@example.com> <m1@example.com> <m2@example.com>", appendEmailReference(msg.References, msg.MessageID))
	assert.NotEqual(t, EmailSessionKey("alice@example.com", "", "", "Subject A"), EmailSessionKey("alice@example.com", "", "", "Subject B"))
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
	svc.sendMail = func(context.Context, EmailRuntimeConfig, string, string, string, string, string) error {
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
	svc.sendMail = func(_ context.Context, _ EmailRuntimeConfig, to, subject, body, inReplyTo, references string) error {
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

func TestEmailService_SendOutboundMessage_NewEmailUsesSMTPWithoutReplyHeaders(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := repository.NewSettingsRepo(db)
	for k, v := range map[string]string{EmailSettingAddress: "bot@example.com", EmailSettingPassword: "secret", EmailSettingIMAPHost: "imap.example.com", EmailSettingSMTPHost: "smtp.example.com"} {
		require.NoError(t, settingsRepo.Set(ctx, k, v))
	}
	svc := NewEmailService(settingsRepo, repository.NewProjectRepo(db), repository.NewLLMConfigRepo(db), repository.NewTaskRepo(db, nil), repository.NewExecutionRepo(db), repository.NewScheduleRepo(db), nil, nil, nil, repository.NewEmailAuthRepo(db), repository.NewEmailTaskContextRepo(db))
	var gotTo, gotSubject, gotBody, gotInReplyTo, gotRefs string
	svc.sendMail = func(_ context.Context, _ EmailRuntimeConfig, to, subject, body, inReplyTo, references string) error {
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
	sessionKey := EmailSessionKey("alice@example.com", "<root@example.com>", "", "Photo")
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
