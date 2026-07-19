package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	netmail "net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	messagemail "github.com/emersion/go-message/mail"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

const (
	EmailSettingProvider                = "email_provider"
	EmailSettingAddress                 = "email_address"
	EmailSettingPassword                = "email_password"
	EmailSettingIMAPHost                = "email_imap_host"
	EmailSettingIMAPPort                = "email_imap_port"
	EmailSettingSMTPHost                = "email_smtp_host"
	EmailSettingSMTPPort                = "email_smtp_port"
	EmailSettingPollIntervalSeconds     = "email_poll_interval_seconds"
	EmailSettingSendResponses           = "email_send_responses"
	EmailSettingSkipAttachments         = "email_skip_attachments"
	EmailSettingMarkExistingSeenOnStart = "email_mark_existing_seen_on_start"

	EmailProviderCustom   = "custom"
	EmailProviderGmail    = "gmail"
	EmailProviderOutlook  = "outlook"
	EmailProviderYahoo    = "yahoo"
	EmailProviderFastmail = "fastmail"
	EmailProviderICloud   = "icloud"

	emailProcessTimeout   = 5 * time.Minute
	emailChatHistoryLimit = 50

	emailMaxFileSize     = 20 << 20   // 20 MB per attachment
	emailMaxTextFileSize = 100 * 1024 // 100KB for inline text-attachment context
	emailMaxAttachments  = 10
)

type EmailProviderPreset struct {
	Key      string
	Label    string
	IMAPHost string
	IMAPPort int
	SMTPHost string
	SMTPPort int
	HelpText string
}

var emailProviderPresets = map[string]EmailProviderPreset{
	EmailProviderGmail: {
		Key: EmailProviderGmail, Label: "Gmail", IMAPHost: "imap.gmail.com", IMAPPort: 993, SMTPHost: "smtp.gmail.com", SMTPPort: 587,
		HelpText: "Use a Google app password; IMAP must be enabled in Gmail settings.",
	},
	EmailProviderOutlook: {
		Key: EmailProviderOutlook, Label: "Outlook / Microsoft 365", IMAPHost: "outlook.office365.com", IMAPPort: 993, SMTPHost: "smtp.office365.com", SMTPPort: 587,
		HelpText: "Use an app password or tenant-approved SMTP auth.",
	},
	EmailProviderYahoo: {
		Key: EmailProviderYahoo, Label: "Yahoo Mail", IMAPHost: "imap.mail.yahoo.com", IMAPPort: 993, SMTPHost: "smtp.mail.yahoo.com", SMTPPort: 587,
		HelpText: "Use an app password.",
	},
	EmailProviderFastmail: {
		Key: EmailProviderFastmail, Label: "Fastmail", IMAPHost: "imap.fastmail.com", IMAPPort: 993, SMTPHost: "smtp.fastmail.com", SMTPPort: 587,
		HelpText: "Use an app password/API password.",
	},
	EmailProviderICloud: {
		Key: EmailProviderICloud, Label: "iCloud Mail", IMAPHost: "imap.mail.me.com", IMAPPort: 993, SMTPHost: "smtp.mail.me.com", SMTPPort: 587,
		HelpText: "Use an app-specific password.",
	},
	EmailProviderCustom: {
		Key: EmailProviderCustom, Label: "Custom IMAP/SMTP", IMAPPort: 993, SMTPPort: 587,
		HelpText: "Enter your IMAP and SMTP host details.",
	},
}

func EmailProviderPresets() []EmailProviderPreset {
	return []EmailProviderPreset{
		emailProviderPresets[EmailProviderGmail],
		emailProviderPresets[EmailProviderOutlook],
		emailProviderPresets[EmailProviderYahoo],
		emailProviderPresets[EmailProviderFastmail],
		emailProviderPresets[EmailProviderICloud],
		emailProviderPresets[EmailProviderCustom],
	}
}

func NormalizeEmailProvider(provider string) string {
	key := strings.ToLower(strings.TrimSpace(provider))
	if _, ok := emailProviderPresets[key]; ok {
		return key
	}
	return EmailProviderCustom
}

func NormalizeEmailPasswordForProvider(provider, password string) string {
	password = strings.TrimSpace(password)
	if NormalizeEmailProvider(provider) == EmailProviderCustom {
		return password
	}
	return regexp.MustCompile(`\s+`).ReplaceAllString(password, "")
}

func ResolveEmailProviderSettings(provider, imapHost, imapPort, smtpHost, smtpPort string) (string, string, int, string, int, error) {
	key := NormalizeEmailProvider(provider)
	preset := emailProviderPresets[key]
	if key != EmailProviderCustom {
		return key, preset.IMAPHost, preset.IMAPPort, preset.SMTPHost, preset.SMTPPort, nil
	}
	imapHost = strings.TrimSpace(imapHost)
	smtpHost = strings.TrimSpace(smtpHost)
	if imapHost == "" || smtpHost == "" {
		return key, imapHost, 0, smtpHost, 0, fmt.Errorf("custom email provider requires IMAP and SMTP hosts")
	}
	imapPortInt, err := parseEmailPort(imapPort, preset.IMAPPort)
	if err != nil {
		return key, imapHost, 0, smtpHost, 0, fmt.Errorf("invalid IMAP port")
	}
	smtpPortInt, err := parseEmailPort(smtpPort, preset.SMTPPort)
	if err != nil {
		return key, imapHost, 0, smtpHost, 0, fmt.Errorf("invalid SMTP port")
	}
	return key, imapHost, imapPortInt, smtpHost, smtpPortInt, nil
}

func parseEmailPort(value string, fallback int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return port, nil
}

type EmailConnectionStatus struct {
	Configured bool
	Running    bool
	Address    string
	Provider   string
	IMAPHost   string
	IMAPPort   int
	SMTPHost   string
	SMTPPort   int
}

type emailIMAPClient interface {
	Login(username, password string) error
	Select(name string, readOnly bool) (*imap.MailboxStatus, error)
	Search(criteria *imap.SearchCriteria) ([]uint32, error)
	Fetch(seqset *imap.SeqSet, items []imap.FetchItem, ch chan *imap.Message) error
	Store(seqset *imap.SeqSet, item imap.StoreItem, flags interface{}, ch chan *imap.Message) error
	Logout() error
}

type EmailService struct {
	settingsRepo           *repository.SettingsRepo
	projectRepo            *repository.ProjectRepo
	llmConfigRepo          *repository.LLMConfigRepo
	taskRepo               *repository.TaskRepo
	execRepo               *repository.ExecutionRepo
	scheduleRepo           *repository.ScheduleRepo
	taskSvc                *TaskService
	llmSvc                 *LLMService
	workerSvc              *WorkerService
	emailAuthRepo          *repository.EmailAuthRepo
	emailTaskContextRepo   *repository.EmailTaskContextRepo
	emailSenderProjectRepo *repository.EmailSenderProjectRepo
	threadInputRepo        *repository.ThreadInputRepo
	customPersonalityRepo  *repository.CustomPersonalityRepo
	agentRepo              *repository.AgentRepo
	chatBroadcaster        *events.ChatBroadcaster
	executionStreamHub     *events.ExecutionStreamHub
	queuedTurnPromoter     func(projectID string)
	channelChatRunner      ChannelChatRunner
	channelMessageRouter   *ChannelMessageRouter
	chatAttachmentRepo     *repository.ChatAttachmentRepo
	uploadsDir             string

	mu                       sync.RWMutex
	running                  bool
	ctx                      context.Context
	cancel                   context.CancelFunc
	connectIMAP              func(ctx context.Context, cfg EmailRuntimeConfig) (emailIMAPClient, error)
	sendMail                 func(ctx context.Context, cfg EmailRuntimeConfig, to, subject, body, inReplyTo, references string) error
	processIncomingMessageFn func(context.Context, EmailInboundMessage)
}

type EmailRuntimeConfig struct {
	Provider                string
	Address                 string
	Password                string
	IMAPHost                string
	IMAPPort                int
	SMTPHost                string
	SMTPPort                int
	PollInterval            time.Duration
	SendResponses           bool
	SkipAttachments         bool
	MarkExistingSeenOnStart bool
}

type EmailInboundMessage struct {
	FromName      string
	FromAddress   string
	Subject       string
	Body          string
	MessageID     string
	References    string
	AutoSubmitted string
	Precedence    string
	ListUnsub     string
	Attachments   []EmailInboundAttachment
}

// EmailInboundAttachment carries the decoded bytes and metadata of a single
// MIME attachment part extracted from an inbound email. The go-message/mail
// reader already decodes transfer encodings (base64/quoted-printable), so
// Data holds the raw file bytes.
type EmailInboundAttachment struct {
	FileName    string
	ContentType string
	Data        []byte
}

func NewEmailService(settingsRepo *repository.SettingsRepo, projectRepo *repository.ProjectRepo, llmConfigRepo *repository.LLMConfigRepo, taskRepo *repository.TaskRepo, execRepo *repository.ExecutionRepo, scheduleRepo *repository.ScheduleRepo, taskSvc *TaskService, llmSvc *LLMService, workerSvc *WorkerService, emailAuthRepo *repository.EmailAuthRepo, emailTaskContextRepo *repository.EmailTaskContextRepo) *EmailService {
	s := &EmailService{
		settingsRepo:         settingsRepo,
		projectRepo:          projectRepo,
		llmConfigRepo:        llmConfigRepo,
		taskRepo:             taskRepo,
		execRepo:             execRepo,
		scheduleRepo:         scheduleRepo,
		taskSvc:              taskSvc,
		llmSvc:               llmSvc,
		workerSvc:            workerSvc,
		emailAuthRepo:        emailAuthRepo,
		emailTaskContextRepo: emailTaskContextRepo,
	}
	s.connectIMAP = defaultEmailIMAPConnect
	s.sendMail = defaultEmailSendMail
	return s
}

func (s *EmailService) SetChatBroadcaster(cb *events.ChatBroadcaster) { s.chatBroadcaster = cb }
func (s *EmailService) SetExecutionStreamHub(hub *events.ExecutionStreamHub) {
	s.executionStreamHub = hub
}
func (s *EmailService) SetThreadInputRepo(repo *repository.ThreadInputRepo) { s.threadInputRepo = repo }
func (s *EmailService) SetCustomPersonalityRepo(repo *repository.CustomPersonalityRepo) {
	s.customPersonalityRepo = repo
}
func (s *EmailService) SetAgentRepo(repo *repository.AgentRepo) { s.agentRepo = repo }
func (s *EmailService) SetQueuedTurnPromoter(promoter func(projectID string)) {
	s.queuedTurnPromoter = promoter
}
func (s *EmailService) SetChannelChatRunner(runner ChannelChatRunner) { s.channelChatRunner = runner }
func (s *EmailService) SetChannelMessageRouter(router *ChannelMessageRouter) {
	s.channelMessageRouter = router
}
func (s *EmailService) SetEmailSenderProjectRepo(repo *repository.EmailSenderProjectRepo) {
	s.emailSenderProjectRepo = repo
}
func (s *EmailService) SetChatAttachmentRepo(repo *repository.ChatAttachmentRepo) {
	s.chatAttachmentRepo = repo
}
func (s *EmailService) SetUploadsDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	s.uploadsDir = dir
}

func (s *EmailService) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *EmailService) Start() error {
	cfg, err := s.loadConfig(context.Background())
	if err != nil || !cfg.Configured() {
		return err
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()
	go s.pollLoop(ctx, cfg)
	applog.Infof("[email] polling started for %s", cfg.Address)
	return nil
}

func (s *EmailService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
	applog.Infof("[email] polling stopped")
}

func (s *EmailService) ReloadFromSettings(ctx context.Context) error {
	s.Stop()
	return s.Start()
}

func (s *EmailService) TestConnection(ctx context.Context) error {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Configured() {
		return fmt.Errorf("email channel is not fully configured")
	}
	client, err := s.connectIMAP(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Logout()
	return nil
}

func (s *EmailService) GetConnectionStatus(ctx context.Context) EmailConnectionStatus {
	cfg, _ := s.loadConfig(ctx)
	return EmailConnectionStatus{
		Configured: cfg.Configured(), Running: s.IsRunning(), Address: cfg.Address, Provider: cfg.Provider,
		IMAPHost: cfg.IMAPHost, IMAPPort: cfg.IMAPPort, SMTPHost: cfg.SMTPHost, SMTPPort: cfg.SMTPPort,
	}
}

func (cfg EmailRuntimeConfig) Configured() bool {
	return strings.TrimSpace(cfg.Address) != "" && strings.TrimSpace(cfg.Password) != "" && strings.TrimSpace(cfg.IMAPHost) != "" && strings.TrimSpace(cfg.SMTPHost) != ""
}

func (s *EmailService) loadConfig(ctx context.Context) (EmailRuntimeConfig, error) {
	if s.settingsRepo == nil {
		return EmailRuntimeConfig{}, fmt.Errorf("settings repository not configured")
	}
	get := func(key string) string { val, _ := s.settingsRepo.Get(ctx, key); return strings.TrimSpace(val) }
	provider := NormalizeEmailProvider(get(EmailSettingProvider))
	if provider == "" {
		provider = EmailProviderCustom
	}
	imapPort, _ := parseEmailPort(get(EmailSettingIMAPPort), 993)
	smtpPort, _ := parseEmailPort(get(EmailSettingSMTPPort), 587)
	pollSeconds, _ := strconv.Atoi(get(EmailSettingPollIntervalSeconds))
	if pollSeconds <= 0 {
		pollSeconds = 15
	}
	cfg := EmailRuntimeConfig{
		Provider:                provider,
		Address:                 repository.NormalizeEmailAddress(get(EmailSettingAddress)),
		Password:                NormalizeEmailPasswordForProvider(provider, get(EmailSettingPassword)),
		IMAPHost:                get(EmailSettingIMAPHost),
		IMAPPort:                imapPort,
		SMTPHost:                get(EmailSettingSMTPHost),
		SMTPPort:                smtpPort,
		PollInterval:            time.Duration(pollSeconds) * time.Second,
		SendResponses:           strings.ToLower(get(EmailSettingSendResponses)) != "false",
		SkipAttachments:         strings.ToLower(get(EmailSettingSkipAttachments)) == "true",
		MarkExistingSeenOnStart: strings.ToLower(get(EmailSettingMarkExistingSeenOnStart)) != "false",
	}
	if cfg.IMAPHost == "" && provider != EmailProviderCustom {
		preset := emailProviderPresets[provider]
		cfg.IMAPHost, cfg.IMAPPort, cfg.SMTPHost, cfg.SMTPPort = preset.IMAPHost, preset.IMAPPort, preset.SMTPHost, preset.SMTPPort
	}
	return cfg, nil
}

func (s *EmailService) pollLoop(ctx context.Context, cfg EmailRuntimeConfig) {
	if cfg.MarkExistingSeenOnStart {
		if err := s.markUnreadSeen(ctx, cfg); err != nil {
			applog.Infof("[email] mark existing seen failed: %v", err)
		}
	}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			s.pollOnce(ctx, cfg)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *EmailService) markUnreadSeen(ctx context.Context, cfg EmailRuntimeConfig) error {
	client, err := s.connectIMAP(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Logout()
	if _, err := client.Select("INBOX", false); err != nil {
		return err
	}
	ids, err := client.Search(unseenCriteria())
	if err != nil || len(ids) == 0 {
		return err
	}
	return storeSeen(client, ids)
}

func (s *EmailService) pollOnce(ctx context.Context, cfg EmailRuntimeConfig) {
	client, err := s.connectIMAP(ctx, cfg)
	if err != nil {
		applog.Infof("[email] IMAP connection failed: %v", err)
		return
	}
	defer client.Logout()
	if _, err := client.Select("INBOX", false); err != nil {
		applog.Infof("[email] select inbox failed: %v", err)
		return
	}
	ids, err := client.Search(unseenCriteria())
	if err != nil || len(ids) == 0 {
		if err != nil {
			applog.Infof("[email] search unread failed: %v", err)
		}
		return
	}
	messages, err := fetchEmailMessages(client, ids, cfg.SkipAttachments)
	if err != nil {
		applog.Infof("[email] fetch messages failed: %v", err)
		return
	}
	for _, msg := range messages {
		s.ProcessIncoming(ctx, msg)
	}
	if err := storeSeen(client, ids); err != nil {
		applog.Infof("[email] mark seen failed: %v", err)
	}
}

func unseenCriteria() *imap.SearchCriteria {
	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	return criteria
}

func storeSeen(client emailIMAPClient, ids []uint32) error {
	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)
	return client.Store(seqset, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.SeenFlag}, nil)
}

func defaultEmailIMAPConnect(ctx context.Context, cfg EmailRuntimeConfig) (emailIMAPClient, error) {
	addr := fmt.Sprintf("%s:%d", cfg.IMAPHost, cfg.IMAPPort)
	client, err := imapclient.DialTLS(addr, &tls.Config{ServerName: cfg.IMAPHost})
	if err != nil {
		return nil, fmt.Errorf("connect IMAP: %w", err)
	}
	if err := client.Login(cfg.Address, cfg.Password); err != nil {
		_ = client.Logout()
		return nil, fmt.Errorf("login IMAP: %w", err)
	}
	return client, nil
}

func fetchEmailMessages(client emailIMAPClient, ids []uint32, skipAttachments bool) ([]EmailInboundMessage, error) {
	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, section.FetchItem()}
	ch := make(chan *imap.Message, len(ids))
	if err := client.Fetch(seqset, items, ch); err != nil {
		return nil, err
	}
	var out []EmailInboundMessage
	for msg := range ch {
		if msg == nil {
			continue
		}
		inbound, err := parseIMAPMessage(msg, section, skipAttachments)
		if err != nil {
			applog.Infof("[email] parse message failed: %v", err)
			continue
		}
		out = append(out, inbound)
	}
	return out, nil
}

func parseIMAPMessage(msg *imap.Message, section *imap.BodySectionName, skipAttachments bool) (EmailInboundMessage, error) {
	var inbound EmailInboundMessage
	if msg.Envelope != nil {
		inbound.Subject = strings.TrimSpace(msg.Envelope.Subject)
		inbound.MessageID = strings.TrimSpace(msg.Envelope.MessageId)
		if len(msg.Envelope.From) > 0 {
			from := msg.Envelope.From[0]
			inbound.FromName = strings.TrimSpace(from.PersonalName)
			inbound.FromAddress = repository.NormalizeEmailAddress(from.MailboxName + "@" + from.HostName)
		}
	}
	body := msg.GetBody(section)
	if body != nil {
		mr, err := messagemail.CreateReader(body)
		if err == nil {
			h := mr.Header
			if from, err := h.AddressList("From"); err == nil && len(from) > 0 {
				inbound.FromName = from[0].Name
				inbound.FromAddress = repository.NormalizeEmailAddress(from[0].Address)
			}
			if subj, err := h.Subject(); err == nil && subj != "" {
				inbound.Subject = subj
			}
			inbound.MessageID = firstNonEmpty(h.Get("Message-ID"), inbound.MessageID)
			inbound.References = firstNonEmpty(h.Get("References"), inbound.References)
			inbound.AutoSubmitted = h.Get("Auto-Submitted")
			inbound.Precedence = h.Get("Precedence")
			inbound.ListUnsub = h.Get("List-Unsubscribe")
			inbound.Body, inbound.Attachments = readEmailParts(mr, skipAttachments)
		} else {
			raw, _ := io.ReadAll(body)
			inbound.Body = string(raw)
		}
	}
	if inbound.FromAddress == "" {
		return inbound, fmt.Errorf("missing sender")
	}
	return inbound, nil
}

// readEmailParts walks the MIME tree, returning the best plain-text body and any
// attachment parts. The go-message/mail reader decodes transfer encodings
// (base64/quoted-printable) on part.Body, so attachment Data holds raw bytes.
// Inline parts that carry a filename (for example embedded/inline images) are
// also treated as attachments. When skipAttachments is true, attachment parts
// are read past and discarded so behavior matches the legacy text-only path.
func readEmailParts(mr *messagemail.Reader, skipAttachments bool) (string, []EmailInboundAttachment) {
	var plain string
	var fallback string
	var attachments []EmailInboundAttachment
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch h := part.Header.(type) {
		case *messagemail.InlineHeader:
			contentType, _, _ := h.ContentType()
			fileName := emailPartFilename(h)
			b, _ := io.ReadAll(part.Body)
			// Inline parts with a filename (e.g. inline images) are attachments.
			if fileName != "" && !strings.HasPrefix(contentType, "text/") {
				if !skipAttachments && len(attachments) < emailMaxAttachments {
					attachments = appendEmailAttachment(attachments, fileName, contentType, b)
				}
				continue
			}
			if plain == "" && strings.HasPrefix(contentType, "text/plain") {
				plain = strings.TrimSpace(string(b))
				continue
			}
			if fallback == "" && strings.HasPrefix(contentType, "text/html") {
				fallback = stripHTML(string(b))
			}
		case *messagemail.AttachmentHeader:
			if skipAttachments || len(attachments) >= emailMaxAttachments {
				_, _ = io.Copy(io.Discard, part.Body)
				continue
			}
			contentType, _, _ := h.ContentType()
			fileName := emailPartFilename(h)
			b, _ := io.ReadAll(part.Body)
			attachments = appendEmailAttachment(attachments, fileName, contentType, b)
		}
	}
	body := plain
	if body == "" {
		body = strings.TrimSpace(fallback)
	}
	return body, attachments
}

func appendEmailAttachment(attachments []EmailInboundAttachment, fileName, contentType string, data []byte) []EmailInboundAttachment {
	if len(data) == 0 || len(data) > emailMaxFileSize {
		if len(data) > emailMaxFileSize {
			applog.Infof("[email] skipping oversized attachment file=%s size=%d max=%d", fileName, len(data), emailMaxFileSize)
		}
		return attachments
	}
	if strings.TrimSpace(fileName) == "" {
		fileName = "email-attachment"
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return append(attachments, EmailInboundAttachment{FileName: fileName, ContentType: mediaType, Data: data})
}

// emailPartHeader is the subset of go-message header behavior used to resolve a
// part's filename; both *mail.InlineHeader and *mail.AttachmentHeader satisfy it.
type emailPartHeader interface {
	ContentDisposition() (string, map[string]string, error)
	ContentType() (string, map[string]string, error)
}

// emailPartFilename resolves a part's filename, handling RFC 2047/2231 encoded
// names via the header's ContentDisposition/ContentType params.
func emailPartFilename(h emailPartHeader) string {
	if _, params, err := h.ContentDisposition(); err == nil {
		if name := strings.TrimSpace(params["filename"]); name != "" {
			return name
		}
	}
	if _, params, err := h.ContentType(); err == nil {
		if name := strings.TrimSpace(params["name"]); name != "" {
			return name
		}
	}
	return ""
}

var htmlTagRE = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string { return strings.TrimSpace(htmlTagRE.ReplaceAllString(s, " ")) }
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

func (s *EmailService) ProcessIncoming(ctx context.Context, msg EmailInboundMessage) {
	if s.processIncomingMessageFn != nil {
		s.processIncomingMessageFn(ctx, msg)
		return
	}
	s.processIncomingMessage(ctx, msg)
}

func (s *EmailService) processIncomingMessage(ctx context.Context, msg EmailInboundMessage) {
	if isIgnoredEmail(msg, s.getConfiguredAddress(ctx)) ||
		(strings.TrimSpace(msg.Body) == "" && len(msg.Attachments) == 0) {
		return
	}
	if s.taskRepo == nil || s.execRepo == nil || s.llmConfigRepo == nil || s.llmSvc == nil || s.taskSvc == nil || s.projectRepo == nil {
		applog.Infof("[email] incoming message ignored: service dependencies are not fully configured")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), emailProcessTimeout)
	defer cancel()
	projectID := s.resolveAuthorizedProject(ctx, msg.FromAddress)
	if projectID == "" {
		applog.Infof("[email] unauthorized or no project for sender=%s", redactEmail(msg.FromAddress))
		return
	}
	prompt := BuildEmailPrompt(msg)
	sessionKey := EmailSessionKey(msg.FromAddress, msg.MessageID, msg.References, msg.Subject)
	runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:              "email",
		ProjectID:             projectID,
		Message:               prompt,
		Source:                models.TaskOriginEmail,
		Surface:               chatcontrol.SurfaceEmail,
		HasAttachments:        len(msg.Attachments) > 0,
		TaskRepo:              s.taskRepo,
		ExecRepo:              s.execRepo,
		ThreadInputRepo:       s.threadInputRepo,
		LLMConfigRepo:         s.llmConfigRepo,
		ChatBroadcaster:       s.chatBroadcaster,
		TaskSvc:               s.taskSvc,
		ScheduleRepo:          s.scheduleRepo,
		AgentRepo:             s.agentRepo,
		SettingsRepo:          s.settingsRepo,
		CustomPersonalityRepo: s.customPersonalityRepo,
		ProjectRepo:           s.projectRepo,
		UploadsDir:            s.emailUploadsDir(),
		SelectionMessage:      msg.Body,
		DownloadAttachments: func(ctx context.Context) (channelChatIngressDownloadResult, error) {
			if len(msg.Attachments) == 0 {
				return channelChatIngressDownloadResult{}, nil
			}
			attCtx, imgAtts, chatAtts, err := s.stageEmailAttachments(msg.Attachments)
			return channelChatIngressDownloadResult{AttachmentContext: attCtx, ImageAttachments: imgAtts, ChatAttachments: chatAtts}, err
		},
		IncomingAttachmentsNeedVision: func() bool { return emailIncomingAttachmentsRequireVision(msg.Attachments) },
		SavePendingAttachments:        s.saveChatAttachmentsToPendingSession,
		FindActiveExecution: func(ctx context.Context, projectID string) (*models.Execution, error) {
			return s.execRepo.FindLatestActiveEmailChatExecution(ctx, projectID, sessionKey)
		},
		NewQueuedInput: func() *models.ThreadInput {
			return &models.ThreadInput{EmailFrom: msg.FromAddress, EmailMessageID: msg.MessageID, EmailReferences: msg.References, EmailSubject: msg.Subject, EmailSessionKey: sessionKey}
		},
		OnAttachmentDownloadFailed: func(context.Context, string) { applog.Infof("[email] attachment processing failed") },
		OnAttachmentStoreFailed:    func(context.Context, string) { applog.Infof("[email] attachment staging failed") },
		OnModelSelectionFailed:     func(context.Context, error) { applog.Infof("[email] model selection failed") },
		OnActiveLookupFailed:       func(context.Context) { applog.Infof("[email] active chat check failed") },
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task:              &models.Task{Title: fmt.Sprintf("Email %s: %s", time.Now().Format("15:04:05.000"), util.Truncate(msg.Subject, 47)), CreatedVia: models.TaskOriginEmail},
			ReplyContext:      ChannelReplyContext{Source: models.TaskOriginEmail, EmailFrom: msg.FromAddress, EmailMessageID: msg.MessageID, EmailReferences: msg.References, EmailSubject: msg.Subject, EmailSessionKey: sessionKey},
			ChannelChatRunner: s.channelChatRunner,
			RuntimeTools:      s.buildEmailActionToolRuntime(projectID, msg.FromAddress),
			LinkAttachments:   s.linkAttachmentsToExecution,
			AttachmentContextAndImages: func(atts []models.ChatAttachment) (string, []models.Attachment) {
				return channelChatAttachmentContextAndImages(atts, emailMaxTextFileSize)
			},
			CreateTaskContext: func(ctx context.Context, taskID string) error {
				if s.emailTaskContextRepo == nil {
					return nil
				}
				return s.emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{TaskID: taskID, EmailFrom: msg.FromAddress, EmailMessageID: msg.MessageID, EmailReferences: msg.References, EmailSubject: msg.Subject, EmailSessionKey: sessionKey})
			},
			CompleteExecution: channelCompletionFunc("email", s.execRepo, s.taskRepo, s.executionStreamHub, s.queuedTurnPromoter),
			ListChatHistory: func(ctx context.Context, projectID string) ([]models.Execution, error) {
				return s.execRepo.ListEmailChatHistory(ctx, projectID, sessionKey, emailChatHistoryLimit)
			},
			FilterChatHistory: filterEmailChatHistory,
		},
	})
}

// emailUploadsDir returns the configured uploads root, defaulting to "uploads"
// so attachment staging/linking works even before SetUploadsDir wiring.
func (s *EmailService) emailUploadsDir() string {
	if strings.TrimSpace(s.uploadsDir) != "" {
		return s.uploadsDir
	}
	return "uploads"
}

// stageEmailAttachments writes decoded MIME attachment bytes to a temp dir,
// sniffs/validates the bytes (so mislabeled images are classified correctly),
// and returns attachment context, image attachments, and chat attachment
// records for the shared ingress pipeline.
func (s *EmailService) stageEmailAttachments(parts []EmailInboundAttachment) (string, []models.Attachment, []models.ChatAttachment, error) {
	if len(parts) == 0 {
		return "", nil, nil, nil
	}
	tmpDir, err := os.MkdirTemp("", "email-attachment-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	chatAttachments := make([]models.ChatAttachment, 0, len(parts))
	for _, part := range parts {
		if len(part.Data) > emailMaxFileSize {
			return "", nil, nil, fmt.Errorf("attachment %q too large (%d bytes, max %d)", part.FileName, len(part.Data), emailMaxFileSize)
		}
		fileName := safeChannelChatAttachmentFileName(part.FileName, "email-attachment")
		destPath := filepath.Join(tmpDir, uniqueChannelChatTempFilename(tmpDir, fileName))
		if err := os.WriteFile(destPath, part.Data, 0644); err != nil {
			return "", nil, nil, fmt.Errorf("failed to write attachment %q: %w", fileName, err)
		}
		mediaType := part.ContentType
		normalizedMediaType, validateErr := validateChannelChatDownloadedImageFile(destPath, fileName, mediaType, "email")
		if validateErr != nil {
			return "", nil, nil, validateErr
		}
		if normalizedMediaType != "" {
			mediaType = normalizedMediaType
		}
		absPath, err := filepath.Abs(destPath)
		if err != nil {
			absPath = destPath
		}
		chatAttachments = append(chatAttachments, models.ChatAttachment{
			FileName:  fileName,
			FilePath:  absPath,
			MediaType: mediaType,
			FileSize:  int64(len(part.Data)),
		})
		applog.Infof("[email] staged attachment file=%s size=%d mime=%s", fileName, len(part.Data), mediaType)
	}
	cleanup = false
	attachmentContext, imageAttachments := channelChatAttachmentContextAndImages(chatAttachments, emailMaxTextFileSize)
	return attachmentContext, imageAttachments, chatAttachments, nil
}

func (s *EmailService) saveChatAttachmentsToPendingSession(attachments []models.ChatAttachment) (string, error) {
	return saveChannelChatAttachmentsToPendingSession(s.emailUploadsDir(), "email-attachment", attachments)
}

func (s *EmailService) linkAttachmentsToExecution(ctx context.Context, execID string, attachments []models.ChatAttachment) ([]models.ChatAttachment, error) {
	return linkChannelChatAttachmentsToExecution(ctx, execID, attachments, channelChatAttachmentLinkOptions{
		Platform:     "email",
		UploadsDir:   s.emailUploadsDir(),
		Repo:         s.chatAttachmentRepo,
		FallbackName: "email-attachment",
	})
}

// emailIncomingAttachmentsRequireVision reports whether any inbound attachment
// is (or may be) an image, based on declared content types. Final image
// classification is done from sniffed bytes after staging.
func emailIncomingAttachmentsRequireVision(parts []EmailInboundAttachment) bool {
	for _, part := range parts {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(part.ContentType, ";")[0]))
		if isChannelChatImageMediaType(mediaType) {
			return true
		}
		if mediaType == "" || mediaType == "application/octet-stream" {
			return true
		}
	}
	return false
}

func (s *EmailService) resolveAuthorizedProject(ctx context.Context, sender string) string {
	if s.emailAuthRepo == nil || sender == "" || s.projectRepo == nil {
		return ""
	}
	ok, err := s.emailAuthRepo.IsAuthorized(ctx, "", sender)
	if err != nil || !ok {
		return ""
	}
	if s.emailSenderProjectRepo != nil {
		savedProjectID, err := s.emailSenderProjectRepo.GetSenderProject(ctx, sender)
		if err == nil && savedProjectID != "" {
			if project, projectErr := s.projectRepo.GetByID(ctx, savedProjectID); projectErr == nil && project != nil {
				return savedProjectID
			}
			applog.Infof("[email] saved project %s no longer exists for sender=%s; using default", savedProjectID, redactEmail(sender))
		}
	}
	projects, err := s.projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		return ""
	}
	return projects[0].ID
}

// buildEmailActionToolRuntime returns channel-specific RuntimeTools for an
// inbound email chat turn. It covers only the project-sensitive tools
// (switch_project with persistence, get_current_project). The handler's
// generic runtime supplies the remaining tools (task creation, scheduling, etc.)
// via CompositeRuntimeTools in processStreamingResponse.
func (s *EmailService) buildEmailActionToolRuntime(projectID, sender string) *llmcontracts.RuntimeTools {
	handlers := s.emailActionHandlers(projectID, sender)
	defs := make([]llmcontracts.RuntimeToolDefinition, 0, len(handlers))
	// Emit definitions only for the handlers this runtime covers.
	allDefs := actionToolDefinitions(chatcontrol.SurfaceEmail, true)
	for _, def := range allDefs {
		if _, ok := handlers[def.Name]; ok {
			defs = append(defs, def)
		}
	}
	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    chatcontrol.BuildRuntimeToolExecutor(models.ChatModeOrchestrate, chatcontrol.SurfaceEmail, handlers),
	}
}

func (s *EmailService) emailActionHandlers(projectID, sender string) map[string]chatcontrol.RuntimeActionHandler {
	handlers := buildChannelProjectActionHandlers(channelProjectActionHandlerOptions{
		ProjectID:   projectID,
		ProjectRepo: s.projectRepo,
		SwitchProject: func(ctx context.Context, project *models.Project) error {
			if s.emailAuthRepo != nil {
				ok, err := s.emailAuthRepo.IsAuthorized(ctx, project.ID, sender)
				if err != nil || !ok {
					return fmt.Errorf("email sender is not authorized to use project %q", project.Name)
				}
			}
			if s.emailSenderProjectRepo == nil {
				return fmt.Errorf("email sender project repository not configured")
			}
			return s.emailSenderProjectRepo.SetSenderProject(ctx, sender, project.ID)
		},
	})
	mergeChannelRuntimeActionHandlers(handlers, buildChannelContextModeActionHandlers(channelContextModeActionHandlerOptions{
		ChannelDisplayName: "Email",
		ProjectID:          projectID,
		ProjectRepo:        s.projectRepo,
	}))
	return handlers
}

func (s *EmailService) getConfiguredAddress(ctx context.Context) string {
	if s.settingsRepo == nil {
		return ""
	}
	v, _ := s.settingsRepo.Get(ctx, EmailSettingAddress)
	return repository.NormalizeEmailAddress(v)
}

func isIgnoredEmail(msg EmailInboundMessage, selfAddress string) bool {
	from := repository.NormalizeEmailAddress(msg.FromAddress)
	if from == "" || (selfAddress != "" && from == repository.NormalizeEmailAddress(selfAddress)) {
		return true
	}
	if auto := strings.TrimSpace(strings.ToLower(msg.AutoSubmitted)); auto != "" && auto != "no" {
		return true
	}
	precedence := strings.TrimSpace(strings.ToLower(msg.Precedence))
	if precedence == "bulk" || precedence == "list" || precedence == "junk" {
		return true
	}
	if strings.TrimSpace(msg.ListUnsub) != "" {
		return true
	}
	local := from
	if idx := strings.Index(local, "@"); idx >= 0 {
		local = local[:idx]
	}
	for _, token := range []string{"no-reply", "noreply", "do-not-reply", "donotreply", "mailer-daemon"} {
		if strings.Contains(local, token) {
			return true
		}
	}
	return false
}

func BuildEmailPrompt(msg EmailInboundMessage) string {
	name := strings.TrimSpace(msg.FromName)
	from := msg.FromAddress
	if name != "" {
		from = fmt.Sprintf("%s <%s>", name, msg.FromAddress)
	}
	return fmt.Sprintf("[Email from: %s]\n[Subject: %s]\n\n%s", from, strings.TrimSpace(msg.Subject), strings.TrimSpace(msg.Body))
}

func EmailSessionKey(sender, messageID, references, subject string) string {
	sender = repository.NormalizeEmailAddress(sender)
	root := strings.TrimSpace(references)
	if root != "" {
		parts := strings.Fields(root)
		root = parts[0]
	}
	if root == "" {
		root = strings.TrimSpace(messageID)
	}
	if root != "" {
		return "email:" + sender + ":" + root
	}
	h := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(subject))))
	return "email:" + sender + ":" + hex.EncodeToString(h[:8])
}

func filterEmailChatHistory(history []models.Execution, currentExecID string) []models.Execution {
	filtered := make([]models.Execution, 0, len(history))
	for _, exec := range history {
		if exec.ID != currentExecID {
			filtered = append(filtered, exec)
		}
	}
	return filtered
}

func (s *EmailService) IsSendResponsesEnabled(ctx context.Context) bool {
	cfg, _ := s.loadConfig(ctx)
	return cfg.SendResponses
}

func (s *EmailService) SendTaskCompletionToThread(ctx context.Context, to, inboundMessageID, references, subject, taskTitle, output, errMsg string) {
	if !s.IsSendResponsesEnabled(ctx) || strings.TrimSpace(to) == "" {
		return
	}
	body := buildEmailCompletionBody(taskTitle, output, errMsg)
	if err := s.sendReply(ctx, to, subject, body, inboundMessageID, appendEmailReference(references, inboundMessageID)); err != nil {
		applog.Infof("[email] send thread reply failed: %v", err)
	}
}

func (s *EmailService) SendChatResponse(ctx context.Context, task models.Task, output, errMsg string) {
	if s.emailTaskContextRepo == nil || !s.IsSendResponsesEnabled(ctx) {
		return
	}
	etc, err := s.emailTaskContextRepo.GetByTaskID(ctx, task.ID)
	if err != nil || etc == nil {
		return
	}
	body := buildEmailChatBody(output, errMsg)
	if err := s.sendReply(ctx, etc.EmailFrom, etc.EmailSubject, body, etc.EmailMessageID, appendEmailReference(etc.EmailReferences, etc.EmailMessageID)); err != nil {
		applog.Infof("[email] send chat response failed task=%s: %v", task.ID, err)
	}
}

func (s *EmailService) SendTaskCompletionNotification(ctx context.Context, task models.Task, output, errMsg string) {
	if task.CreatedVia != models.TaskOriginEmail && task.ID != "" && s.taskRepo != nil {
		if loaded, err := s.taskRepo.GetByID(ctx, task.ID); err == nil && loaded != nil {
			task = *loaded
		}
	}
	if task.CreatedVia != models.TaskOriginEmail || task.Category == models.CategoryChat || s.emailTaskContextRepo == nil || !s.IsSendResponsesEnabled(ctx) {
		return
	}
	etc, err := s.emailTaskContextRepo.GetByTaskID(ctx, task.ID)
	if err != nil || etc == nil {
		return
	}
	body := buildEmailCompletionBody(task.Title, output, errMsg)
	if err := s.sendReply(ctx, etc.EmailFrom, etc.EmailSubject, body, etc.EmailMessageID, appendEmailReference(etc.EmailReferences, etc.EmailMessageID)); err != nil {
		applog.Infof("[email] send task notification failed task=%s: %v", task.ID, err)
	}
}

func (s *EmailService) sendReply(ctx context.Context, to, subject, body, inReplyTo, references string) error {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Configured() {
		return fmt.Errorf("email channel is not fully configured")
	}
	return s.sendMail(ctx, cfg, to, replySubject(subject), body, inReplyTo, references)
}

func (s *EmailService) sendNewEmail(ctx context.Context, to, subject, body string) error {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Configured() {
		return fmt.Errorf("email channel is not fully configured")
	}
	return s.sendMail(ctx, cfg, to, defaultOutboundEmailSubject(subject), body, "", "")
}

func (s *EmailService) SendOutboundMessage(ctx context.Context, to, subject, body string) SendMessageResult {
	addr, err := netmail.ParseAddress(strings.TrimSpace(to))
	if err != nil || addr == nil || strings.TrimSpace(addr.Address) == "" {
		return SendMessageResult{OK: false, Platform: "email", Error: "invalid email recipient"}
	}
	to = repository.NormalizeEmailAddress(addr.Address)
	if strings.TrimSpace(body) == "" {
		return SendMessageResult{OK: false, Platform: "email", Target: "email:" + to, Error: "message is required"}
	}
	if err := s.sendNewEmail(ctx, to, subject, body); err != nil {
		return SendMessageResult{OK: false, Platform: "email", Target: "email:" + to, Error: err.Error()}
	}
	return SendMessageResult{OK: true, Platform: "email", Target: "email:" + to}
}

func buildEmailCompletionBody(taskTitle, output, errMsg string) string {
	if errMsg != "" {
		return fmt.Sprintf("Task failed: %s\n\n%s", taskTitle, util.Truncate(errMsg, 1000))
	}
	cleaned := llmoutput.CleanChatOutputForDisplay(output)
	if cleaned == "" {
		cleaned = "(No output)"
	}
	return fmt.Sprintf("Task completed: %s\n\n%s", taskTitle, util.Truncate(cleaned, 8000))
}

func buildEmailChatBody(output, errMsg string) string {
	if errMsg != "" {
		return "Error: " + util.Truncate(errMsg, 1000)
	}
	cleaned := llmoutput.CleanChatOutputForDisplay(output)
	if cleaned == "" {
		return "(No output)"
	}
	return util.Truncate(cleaned, 8000)
}

func defaultOutboundEmailSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "OpenVibely"
	}
	return subject
}

func replySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = "OpenVibely response"
	}
	if strings.HasPrefix(strings.ToLower(subject), "re:") {
		return subject
	}
	return "Re: " + subject
}

func appendEmailReference(references, messageID string) string {
	refs := strings.TrimSpace(references)
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return refs
	}
	if refs == "" {
		return messageID
	}
	if strings.Contains(refs, messageID) {
		return refs
	}
	return refs + " " + messageID
}

func defaultEmailSendMail(ctx context.Context, cfg EmailRuntimeConfig, to, subject, body, inReplyTo, references string) error {
	var buf bytes.Buffer
	from := mailAddress(cfg.Address)
	toAddr := mailAddress(to)
	headers := map[string]string{"From": from.String(), "To": toAddr.String(), "Subject": mime.QEncoding.Encode("utf-8", subject), "MIME-Version": "1.0", "Content-Type": `text/plain; charset="utf-8"`, "Content-Transfer-Encoding": "8bit"}
	if inReplyTo != "" {
		headers["In-Reply-To"] = inReplyTo
	}
	if references != "" {
		headers["References"] = references
	}
	for _, key := range []string{"From", "To", "Subject", "MIME-Version", "Content-Type", "Content-Transfer-Encoding", "In-Reply-To", "References"} {
		if v := headers[key]; v != "" {
			fmt.Fprintf(&buf, "%s: %s\r\n", key, v)
		}
	}
	buf.WriteString("\r\n")
	buf.WriteString(body)
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	auth := smtp.PlainAuth("", cfg.Address, cfg.Password, cfg.SMTPHost)
	return smtp.SendMail(addr, auth, cfg.Address, []string{to}, buf.Bytes())
}

func mailAddress(address string) *netmail.Address {
	return &netmail.Address{Address: strings.TrimSpace(address)}
}

func redactEmail(email string) string {
	email = repository.NormalizeEmailAddress(email)
	at := strings.Index(email, "@")
	if at <= 1 {
		return "[redacted]"
	}
	return email[:1] + "***" + email[at:]
}
