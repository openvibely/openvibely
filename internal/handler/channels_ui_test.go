package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestChannelsPageRendersConfiguredXWithoutOAuthSecrets(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	secrets := map[string]string{
		service.XSettingConsumerKey:       "browser-x-consumer-key",
		service.XSettingConsumerSecret:    "browser-x-consumer-secret",
		service.XSettingAccessToken:       "browser-x-access-token",
		service.XSettingAccessTokenSecret: "browser-x-access-token-secret",
	}
	for key, value := range secrets {
		if err := h.settingsRepo.Set(ctx, key, value); err != nil {
			t.Fatalf("save X setting %s: %v", key, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		`data-channel-type="x"`,
		`id="x_config_modal"`,
		`hx-post="/channels/x/configure"`,
		`name="x_consumer_key"`,
		`name="x_consumer_secret"`,
		`name="x_access_token"`,
		`name="x_access_token_secret"`,
		`type="password"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected configured X page to contain %q", expected)
		}
	}
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("configured X page exposed OAuth secret %q", secret)
		}
	}
}

func cardSectionByType(body, channelType string) string {
	start := strings.Index(body, `data-channel-type="`+channelType+`"`)
	if start == -1 {
		return ""
	}
	next := strings.Index(body[start+1:], `data-channel-type="`)
	if next == -1 {
		return body[start:]
	}
	return body[start : start+1+next]
}

func titleSection(cardBody string) string {
	titleStart := strings.Index(cardBody, `<h3 class="font-bold flex items-center gap-2">`)
	if titleStart == -1 {
		return ""
	}
	titleEnd := strings.Index(cardBody[titleStart:], `</h3>`)
	if titleEnd == -1 {
		return ""
	}
	return cardBody[titleStart : titleStart+titleEnd+len(`</h3>`)]
}

func inputTagByID(body, id string) string {
	return inputTagByAttribute(body, "id", id)
}

func inputTagByName(body, name string) string {
	return inputTagByAttribute(body, "name", name)
}

func inputTagByAttribute(body, attr, value string) string {
	marker := attr + `="` + value + `"`
	attrIdx := strings.Index(body, marker)
	if attrIdx == -1 {
		return ""
	}
	return inputTagEndingAt(body, attrIdx)
}

func inputTagBeforeText(body, text string) string {
	textIdx := strings.Index(body, text)
	if textIdx == -1 {
		return ""
	}
	return inputTagEndingAt(body, textIdx)
}

func inputTagEndingAt(body string, endBefore int) string {
	start := strings.LastIndex(body[:endBefore], "<input")
	if start == -1 {
		return ""
	}
	endRel := strings.Index(body[start:], ">")
	if endRel == -1 {
		return ""
	}
	return body[start : start+endRel+1]
}

func optionTagByValue(body, value string) string {
	marker := `value="` + value + `"`
	valueIdx := strings.Index(body, marker)
	if valueIdx == -1 {
		return ""
	}
	start := strings.LastIndex(body[:valueIdx], "<option")
	if start == -1 {
		return ""
	}
	endRel := strings.Index(body[start:], "</option>")
	if endRel == -1 {
		return ""
	}
	return body[start : start+endRel+len("</option>")]
}

func assertIndexOrder(t *testing.T, body, first, second, message string) {
	t.Helper()
	firstIdx := strings.Index(body, first)
	if firstIdx == -1 {
		t.Fatalf("missing marker %q", first)
	}
	secondIdx := strings.Index(body, second)
	if secondIdx == -1 {
		t.Fatalf("missing marker %q", second)
	}
	if firstIdx > secondIdx {
		t.Fatal(message)
	}
}

func TestChannelsPageRendersCardLayout(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify empty-state card-based layout is present
	if !strings.Contains(body, "No channels added yet") {
		t.Error("expected empty state when no channels are configured")
	}

	// Verify no active telegram card by default
	if strings.Contains(body, `data-channel-type="telegram"`) {
		t.Error("did not expect telegram card before channel is added")
	}

	// Verify dropdown handler still present for channel cards
	if !strings.Contains(body, "handleDropdownToggle") {
		t.Error("expected dropdown menu handler")
	}

	// Verify modal for telegram editing
	if !strings.Contains(body, "channel_modal") {
		t.Error("expected channel configuration modal")
	}
	if !strings.Contains(body, `id="channel_form"`) {
		t.Error("expected telegram channel form")
	}
	if !strings.Contains(body, `hx-post="/channels/telegram"`) {
		t.Error("expected telegram channel form to submit via HTMX")
	}
	if !strings.Contains(body, `hx-swap="none"`) {
		t.Error("expected telegram channel form to submit in-place without swapping")
	}

	// Verify add-channel dropdown menu exists
	if !strings.Contains(body, "All channels added") && !strings.Contains(body, "GitHub") {
		t.Error("expected add-channel dropdown options")
	}

	// Verify "Add Channel" button
	if !strings.Contains(body, "+ Add Channel") {
		t.Error("expected Add Channel button")
	}

	// Verify all first-class channel integrations are available instead of placeholder cards.
	if strings.Contains(body, "Coming Soon") {
		t.Error("did not expect Coming Soon section for first-class channels")
	}

	// Verify add modal includes available channel options
	if !strings.Contains(body, "GitHub") || !strings.Contains(body, "Telegram Bot") || !strings.Contains(body, "Slack") || !strings.Contains(body, "Discord") {
		t.Error("expected add-channel options for GitHub, Slack, Discord, and Telegram")
	}
	if strings.Contains(body, "Discord Bot") || strings.Contains(body, "Discord Bot Coming Soon") {
		t.Error("did not expect Discord to render as coming soon")
	}
	if !strings.Contains(body, `id="discord_config_modal"`) || !strings.Contains(body, `hx-post="/channels/discord/configure"`) {
		t.Error("expected Discord configuration modal")
	}
	if !strings.Contains(body, `id="discord_config_modal" class="modal" onclose="resetDiscordConfigForm()"`) ||
		!strings.Contains(body, `function resetDiscordConfigForm()`) ||
		!strings.Contains(body, `form.reset();`) ||
		!strings.Contains(body, `resetSecretInputVisibility('discord_bot_token')`) {
		t.Error("expected Discord configuration modal to reset unsaved edits and masked token state on close/reopen")
	}
	discordFormIdx := strings.Index(body, `id="discord_config_form"`)
	if discordFormIdx == -1 {
		t.Fatal("expected Discord config form")
	}
	discordFormTagEnd := strings.Index(body[discordFormIdx:], `>`)
	if discordFormTagEnd == -1 {
		t.Fatal("expected Discord config form opening tag to close")
	}
	discordFormTag := body[discordFormIdx : discordFormIdx+discordFormTagEnd]
	if strings.Contains(discordFormTag, `hx-on::after-request`) {
		t.Error("did not expect Discord config form to have a parent after-request close hook that can catch authorized-user HTMX requests")
	}
	if !strings.Contains(body, `var requestConfig = detail.requestConfig || {}`) ||
		!strings.Contains(body, `var rawPath = requestConfig.path || (detail.pathInfo && detail.pathInfo.requestPath) || detail.path || ''`) ||
		!strings.Contains(body, `var rp = String(rawPath).split('?')[0]`) ||
		!strings.Contains(body, `rp === '/channels/discord/configure' && detail.successful`) ||
		!strings.Contains(body, `document.getElementById('discord_config_modal')`) {
		t.Error("expected Discord configuration modal to close after successful configure save using robust HTMX request path detection")
	}
	if !strings.Contains(body, `hx-post="/channels/discord/authorized-users"`) ||
		!strings.Contains(body, `hx-target="#discord-authorized-users"`) ||
		!strings.Contains(body, `hx-include="#discord-authorized-users-add-controls"`) {
		t.Error("expected Discord authorized-user Add to update the allowlist fragment without closing the config modal")
	}
	for _, removed := range []string{`name="discord_default_channel_id"`, `name="discord_free_response_channels"`, `name="discord_require_mention"`, "Default Channel ID", "Free-Response Channel IDs", "Require bot mention"} {
		if strings.Contains(body, removed) {
			t.Fatalf("did not expect removed Discord free-response/default-channel setting %q in Channels page", removed)
		}
	}
	if !strings.Contains(body, "DMs are supported. In guild channels, users must mention the bot.") {
		t.Fatal("expected Discord modal copy to state guild messages require bot mention")
	}
}

func TestChannelsPageTelegramRichMessagesToggleDefaultsCheckedAndHonorsSavedFalse(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="channel_telegram_rich_messages_v2"`) {
		t.Fatal("expected Telegram rich messages toggle in channel modal")
	}
	if !strings.Contains(body, "Telegram Rich Messages V2") {
		t.Fatal("expected rich messages toggle label")
	}
	if !strings.Contains(body, "Use Telegram Bot API 10.1 rich formatting for assistant responses when supported.") {
		t.Fatal("expected rich messages help copy")
	}
	if !strings.Contains(body, `name="telegram_rich_messages_v2"`) || !strings.Contains(body, `value="true"`) {
		t.Fatal("expected rich messages setting to be posted with the Telegram form")
	}
	section := inputTagByID(body, "channel_telegram_rich_messages_v2")
	if !strings.Contains(section, "checked") {
		t.Fatal("expected rich messages toggle checked by default when setting is missing")
	}

	if err := h.settingsRepo.Set(context.Background(), service.TelegramSettingRichMessagesV2, "false"); err != nil {
		t.Fatalf("failed to seed rich messages setting: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body = rec.Body.String()
	section = inputTagByID(body, "channel_telegram_rich_messages_v2")
	if strings.Contains(section, "checked") {
		t.Fatal("expected rich messages toggle unchecked when setting is explicitly false")
	}
	if !strings.Contains(body, "if (richToggle) richToggle.checked = true;") {
		t.Fatal("expected add Telegram flow to default rich messages toggle on")
	}
	if !strings.Contains(body, "if (richToggle) richToggle.checked = richToggle.defaultChecked;") {
		t.Fatal("expected edit Telegram flow to restore saved rich messages preference")
	}
}

func TestChannelsPageOutboundTargetsRenderAsPermanentTopEditCard(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	targetRepo := repository.NewChannelTargetRepo(db)
	h.SetChannelTargetRepo(targetRepo)
	seedTarget := models.ChannelTarget{
		ID:             repository.NewID(),
		ProjectID:      "default",
		Platform:       "email",
		Name:           "client",
		TargetID:       "client@example.com",
		Home:           true,
		DefaultSubject: "Original subject",
	}
	if err := targetRepo.Upsert(context.Background(), seedTarget); err != nil {
		t.Fatalf("failed to seed outbound target: %v", err)
	}
	h.SetSlackService(&fakeSlackService{
		statusFn: func(ctx context.Context) (service.SlackConnectionStatus, error) {
			return service.SlackConnectionStatus{Configured: true, Connected: true}, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`id="channels-container"`,
		`hx-get="/channels?project_id=default"`,
		`hx-trigger="channels-refresh from:body"`,
		`hx-target="this"`,
		`hx-swap="outerHTML"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Channels container refresh contract to contain %q", want)
		}
	}
	outboundCard := cardSectionByType(body, "outbound-targets")
	if outboundCard == "" {
		t.Fatal("expected permanent outbound message targets card")
	}
	if !strings.Contains(outboundCard, "Outbound Message Targets") || !strings.Contains(outboundCard, "Safety controls") {
		t.Fatalf("expected outbound card summary, got %q", outboundCard)
	}
	if !strings.Contains(outboundCard, "email: 1") || !strings.Contains(outboundCard, "Saved targets only") {
		t.Fatalf("expected outbound card count and policy badges, got %q", outboundCard)
	}
	if !strings.Contains(outboundCard, "openOutboundTargetsModal()") {
		t.Fatal("expected outbound card to open edit modal")
	}
	if !strings.Contains(outboundCard, `hx-get="/channels/outbound-targets/card?project_id=default"`) || !strings.Contains(outboundCard, `hx-trigger="outbound-targets-card-refresh from:body"`) {
		t.Fatalf("expected outbound card to self-refresh on target mutations, got %q", outboundCard)
	}
	if strings.Contains(outboundCard, "Delete") || strings.Contains(outboundCard, "openDeleteChannelConfirm") {
		t.Fatal("outbound safety card must not expose a delete action")
	}
	assertIndexOrder(t, body, `data-channel-type="outbound-targets"`, `data-channel-type="slack"`, "expected outbound targets card before channel cards")
	if !strings.Contains(body, `id="outbound_targets_modal"`) || !strings.Contains(body, `onclose="handleOutboundTargetsModalClose()"`) {
		t.Fatal("expected outbound targets edit modal with draft-discard close hook")
	}
	if !strings.Contains(body, `onsubmit="return addOutboundTargetDraft(event)"`) || !strings.Contains(body, `client@example.com`) {
		t.Fatal("expected staged outbound target controls inside modal")
	}
	savedRowStart := strings.Index(body, `<tr data-outbound-target-draft-key="`)
	if savedRowStart == -1 {
		t.Fatalf("expected saved outbound target row markup, body=%q", body)
	}
	savedRowEnd := strings.Index(body[savedRowStart:], `</tr>`)
	if savedRowEnd == -1 {
		t.Fatalf("expected saved outbound target row to close, body=%q", body)
	}
	savedRow := body[savedRowStart : savedRowStart+savedRowEnd+len(`</tr>`)]
	if strings.Count(savedRow, `onclick="editOutboundTargetDraft(this)"`) != 1 {
		t.Fatalf("expected one discoverable Edit action for the saved outbound target, row=%q", savedRow)
	}
	for _, want := range []string{
		"function editOutboundTargetDraft",
		"outboundTargetsFindDraftGroup",
		"form.dataset.editingRowKey",
		"Update Target",
		"const fieldsID = 'outbound-target-draft-fields-'",
		"const id = outboundTargetsDraftValue(editingGroup, 'target_row_id');", `name="target_default_subject" value="Original subject"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected saved target edit flow to contain %q, body=%q", want, body)
		}
	}
	if strings.Count(body, `data-channel-type="outbound-targets"`) != 1 {
		t.Fatalf("expected exactly one outbound targets card on page, got %d", strings.Count(body, `data-channel-type="outbound-targets"`))
	}
	if !strings.Contains(body, `closeOutboundTargetsModal()">Cancel`) || !strings.Contains(body, `Save Settings`) {
		t.Fatal("expected outbound targets modal to expose Cancel and Save Settings actions")
	}
	if !strings.Contains(body, `class="alert alert-info mt-3 mb-4 text-sm"`) || !strings.Contains(body, `Saved destinations for send_message`) || !strings.Contains(body, `Telegram numeric user IDs`) {
		t.Fatal("expected outbound targets guidance to render as an info alert with authorized-recipient copy")
	}
	assertIndexOrder(t, body, `id="outbound-target-add-form"`, `Allow explicit unsaved targets`, "expected explicit-target toggle below target list controls, not in the modal header")
	assertIndexOrder(t, body, `Allow explicit unsaved targets`, `closeOutboundTargetsModal()">Cancel`, "expected explicit-target toggle in the left side of the footer before action buttons")
	if !strings.Contains(body, `modal-action flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between`) || !strings.Contains(body, `toggle toggle-primary toggle-sm`) {
		t.Fatal("expected outbound targets footer to split toggle on the left and save actions on the right")
	}
	if !strings.Contains(body, `id="outbound-targets-draft-fields" class="hidden"`) {
		t.Fatal("expected hidden outbound target draft fields to stay out of the footer flex layout")
	}
	if strings.Contains(body, `onchange="this.form.requestSubmit()"`) {
		t.Fatal("explicit-target toggle should not autosubmit and append refreshed cards into the modal")
	}

	fragmentReq := httptest.NewRequest(http.MethodGet, "/channels/outbound-targets?project_id=default", nil)
	fragmentRec := httptest.NewRecorder()
	e.ServeHTTP(fragmentRec, fragmentReq)
	if fragmentRec.Code != http.StatusOK {
		t.Fatalf("expected fragment status 200, got %d", fragmentRec.Code)
	}
	fragmentBody := fragmentRec.Body.String()
	if strings.Contains(fragmentBody, `data-channel-type="outbound-targets"`) || strings.Contains(fragmentBody, `hx-swap-oob`) {
		t.Fatal("outbound targets fragment must not include the top-level card or OOB card markup")
	}
	if !strings.Contains(body, `id="outbound-target-add-form"`) || !strings.Contains(body, `>Add Target</button>`) {
		t.Fatal("expected add-target form and Add Target button label")
	}
	if !strings.Contains(body, `<option value="discord">Discord</option>`) {
		t.Fatal("expected outbound targets modal to support Discord targets")
	}
	if !strings.Contains(body, `/channels/outbound-targets/test-draft`) || !strings.Contains(body, `window.htmx.process(row)`) {
		t.Fatal("expected draft-added target rows to include an immediately usable Test button")
	}
	if !strings.Contains(body, `data-outbound-target-test-label`) || !strings.Contains(body, `outboundTargetTestBefore(this)`) || !strings.Contains(body, `outboundTargetTestAfter(this, event)`) || !strings.Contains(body, `loading loading-spinner loading-xs`) || !strings.Contains(body, `<span>Testing...</span>`) {
		t.Fatal("expected draft target Test buttons to show spinner progress and completion inside the button")
	}
	if strings.Contains(body, `id="outbound-target-test-draft-`) || strings.Contains(body, `hx-indicator="#outbound-target-test-indicator-`) {
		t.Fatal("expected draft target Test status to avoid separate banner/indicator elements")
	}
	if !strings.Contains(body, "resetOutboundTargetAddForm") || !strings.Contains(body, "reloadOutboundTargetsModalDraft") || !strings.Contains(body, "rp === '/channels/send-message-explicit-targets'") {
		t.Fatal("expected outbound modal close/reset/reload hooks")
	}
	if !strings.Contains(body, "outbound-targets-card-refresh") || !strings.Contains(body, "getResponseHeader('HX-Trigger')") {
		t.Fatal("expected outbound modal to close only after successful saved-target refresh trigger")
	}
	if strings.Contains(body, "// Close webhook modal after successful save\t") || strings.Contains(body, "// Close webhook modal after successful save var rp") {
		t.Fatal("expected rp declaration to be executable, not commented out")
	}
	assertIndexOrder(t, body, "var rp = String(rawPath).split('?')[0];", "// Close webhook modal after successful save", "expected normalized request path declaration before webhook modal branch")

	cardReq := httptest.NewRequest(http.MethodGet, "/channels/outbound-targets/card?project_id=default", nil)
	cardRec := httptest.NewRecorder()
	e.ServeHTTP(cardRec, cardReq)
	if cardRec.Code != http.StatusOK {
		t.Fatalf("expected card fragment status 200, got %d", cardRec.Code)
	}
	cardBody := cardRec.Body.String()
	if strings.Count(cardBody, `data-channel-type="outbound-targets"`) != 1 || !strings.Contains(cardBody, "email: 1") {
		t.Fatalf("expected card fragment to show persisted target count before save, got %q", cardBody)
	}

	policyForm := url.Values{}
	policyForm.Set("project_id", "default")
	policyForm.Set("enabled", "true")
	policyForm.Add("target_row_id", seedTarget.ID)
	policyForm.Add("target_platform", "email")
	policyForm.Add("target_name", "client")
	policyForm.Add("target_target_id", "client@example.com")
	policyForm.Add("target_thread_id", "")
	policyForm.Add("target_is_home", "true")
	policyForm.Add("target_default_subject", "")
	policyForm.Add("target_row_id", "")
	policyForm.Add("target_platform", "email")
	policyForm.Add("target_name", "billing")
	policyForm.Add("target_target_id", "billing@example.com")
	policyForm.Add("target_thread_id", "")
	policyForm.Add("target_is_home", "false")
	policyForm.Add("target_default_subject", "")
	policyReq := httptest.NewRequest(http.MethodPost, "/channels/send-message-explicit-targets", strings.NewReader(policyForm.Encode()))
	policyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	policyRec := httptest.NewRecorder()
	e.ServeHTTP(policyRec, policyReq)
	if policyRec.Code != http.StatusOK {
		t.Fatalf("expected policy status 200, got %d", policyRec.Code)
	}
	if policyRec.Header().Get("HX-Trigger") != "outbound-targets-card-refresh" {
		t.Fatalf("expected save response to refresh summary card, got HX-Trigger %q", policyRec.Header().Get("HX-Trigger"))
	}
	policyBody := policyRec.Body.String()
	if !strings.Contains(policyBody, "Saved outbound message targets.") || !strings.Contains(policyBody, "billing@example.com") {
		t.Fatalf("expected saved draft target to appear in refreshed modal fragment, got %q", policyBody)
	}
	if !strings.Contains(policyBody, `data-outbound-target-test-label`) || !strings.Contains(policyBody, `outboundTargetTestBefore(this)`) || !strings.Contains(policyBody, `outboundTargetTestAfter(this, event)`) {
		t.Fatalf("expected saved target Test status to stay inside the button, got %q", policyBody)
	}
	if strings.Contains(policyBody, `hx-indicator="#outbound-target-test-indicator-`) || strings.Contains(policyBody, `<div id="outbound-target-test-`) {
		t.Fatalf("expected saved target Test status to avoid separate banner/indicator elements, got %q", policyBody)
	}
	if strings.Contains(policyBody, `data-channel-type="outbound-targets"`) || strings.Contains(policyBody, `hx-swap-oob`) {
		t.Fatal("save response must not include card markup or OOB swaps")
	}

	cardReq = httptest.NewRequest(http.MethodGet, "/channels/outbound-targets/card?project_id=default", nil)
	cardRec = httptest.NewRecorder()
	e.ServeHTTP(cardRec, cardReq)
	if cardRec.Code != http.StatusOK {
		t.Fatalf("expected card fragment status 200 after save, got %d", cardRec.Code)
	}
	cardBody = cardRec.Body.String()
	if !strings.Contains(cardBody, "email: 2") || !strings.Contains(cardBody, "Explicit targets allowed") {
		t.Fatalf("expected refreshed card fragment to show saved draft and policy badge, got %q", cardBody)
	}
}

func TestChannelsPageUsesCompactAgentPickerProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	alertRepo := repository.NewAlertRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, nil, nil)
	h.SetLocalRepoPathEnabled(true)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	e := echo.New()
	h.RegisterRoutes(e)

	alpha := &models.Agent{Name: "Alpha Picker", SystemPrompt: strings.Repeat("large hidden prompt ", 1024), ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read"}}}}}
	if err := agentRepo.Create(context.Background(), alpha); err != nil {
		t.Fatalf("create alpha agent: %v", err)
	}
	zulu := &models.Agent{Name: "Zulu Picker", SystemPrompt: strings.Repeat("large hidden prompt ", 1024)}
	if err := agentRepo.Create(context.Background(), zulu); err != nil {
		t.Fatalf("create zulu agent: %v", err)
	}
	archived := &models.Agent{Name: "Archived Picker", GeneratedStatus: models.AgentStatusArchived}
	if err := agentRepo.Create(context.Background(), archived); err != nil {
		t.Fatalf("create archived agent: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	e.ServeHTTP(rec, req)
	counter.SetEnabled(false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	alphaIdx := strings.Index(body, alpha.ID)
	zuluIdx := strings.Index(body, zulu.ID)
	if alphaIdx == -1 || zuluIdx == -1 {
		t.Fatalf("expected compact picker IDs in Channels response; alpha=%d zulu=%d body=%s", alphaIdx, zuluIdx, body)
	}
	if alphaIdx > zuluIdx {
		t.Fatalf("expected picker JSON to keep name ASC order, alpha index %d after zulu index %d", alphaIdx, zuluIdx)
	}
	forbiddenBody := []string{archived.ID, "large hidden prompt", "scoped_files", "tool_config"}
	for _, forbidden := range forbiddenBody {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Channels response contained hidden picker payload %q", forbidden)
		}
	}

	var pickerQueries []string
	for _, statement := range counter.Statements() {
		stmt := strings.ToLower(statement)
		if strings.Contains(stmt, "from agents") && strings.Contains(stmt, "order by name asc") {
			pickerQueries = append(pickerQueries, statement)
		}
	}
	if len(pickerQueries) != 1 {
		t.Fatalf("expected one Channels agent picker query, got %#v from statements %#v", pickerQueries, counter.Statements())
	}
	projection := strings.Split(strings.ToLower(pickerQueries[0]), "from agents")[0]
	if !strings.Contains(projection, "select id, name") {
		t.Fatalf("Channels picker query used unexpected projection: %s", pickerQueries[0])
	}
	for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("Channels picker query selected full agent column %q: %s", forbidden, pickerQueries[0])
		}
	}
}

func TestChannelsPageBatchesSettingsReadsAndPreservesRenderedValues(t *testing.T) {
	tc := NewTestContext(t)
	selectedProject := tc.CreateProject().WithName("Channels Batch Settings Project").Build()
	otherProject := tc.CreateProject().WithName("Other Channels Project").Build()
	seedRepresentativeChannelsSettings(t, tc.settingsRepo, selectedProject.ID, otherProject.ID)

	var settingsQueries []string
	tc.settingsRepo.SetQueryObserver(func(query string) {
		if strings.Contains(strings.ToLower(query), "from app_settings") {
			settingsQueries = append(settingsQueries, query)
		}
	})
	rec := tc.HTMX().Get("/channels?project_id=" + url.QueryEscape(selectedProject.ID)).Execute()
	tc.settingsRepo.SetQueryObserver(nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected Channels status 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	var batchQueries, singleQueries int
	for _, query := range settingsQueries {
		lower := strings.ToLower(query)
		if strings.Contains(lower, "select key, value") && strings.Contains(lower, "where key in") {
			batchQueries++
		}
		if strings.Contains(lower, "select value from app_settings where key = ?") {
			singleQueries++
		}
	}
	if batchQueries != 1 || singleQueries != 0 {
		t.Fatalf("expected one batched app_settings query and no individual settings queries, got batch=%d single=%d all=%#v", batchQueries, singleQueries, settingsQueries)
	}

	body := rec.Body.String()
	for _, snippet := range []string{
		`name="github_app_id" class="input input-bordered" value="98765"`,
		`name="github_app_slug" class="input input-bordered" value="batch-app"`,
		`batch-private-key`,
		`value="batch-pat"`,
		`name="github_api_endpoint"`,
		`value="https://ghe.example/api/v3"`,
		`name="slack_client_id" class="input input-bordered" value="slack-client-id"`,
		`value="slack-client-secret"`,
		`value="slack-app-token"`,
		`value="slack-bot-override"`,
		`value="discord-bot-token"`,
		`name="email_address" class="input input-bordered" value="bot@example.com"`,
		`value="email-secret"`,
		`name="email_imap_host" class="input input-bordered" value="imap.example.com"`,
		`name="email_smtp_host" class="input input-bordered" value="smtp.example.com"`,
		`name="email_poll_interval_seconds" class="input input-bordered" value="45"`,
		"Explicit targets allowed",
	} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("Channels response missing seeded setting snippet %q", snippet)
		}
	}
	if tag := inputTagByID(body, "channel_telegram_rich_messages_v2"); !strings.Contains(tag, "checked") {
		t.Fatalf("expected channel_telegram_rich_messages_v2 to render checked, got %s", tag)
	}
	for _, label := range []string{
		"Send task responses to Telegram",
		"Send task completion/failure notifications for Slack-created tasks",
		"Send task completion/failure notifications for Discord-created tasks",
		"Send task completion/failure replies by email",
		"Mark existing unread messages seen on start",
	} {
		if tag := inputTagBeforeText(body, label); strings.Contains(tag, "checked") {
			t.Fatalf("expected checkbox before %q to render unchecked from saved false setting, got %s", label, tag)
		}
	}
	if tag := inputTagByName(body, "email_skip_attachments"); !strings.Contains(tag, "checked") {
		t.Fatalf("expected email_skip_attachments to render checked, got %s", tag)
	}
	for _, option := range []struct {
		value string
		label string
	}{
		{service.GitHubAuthModeApp, "GitHub App"},
		{service.SlackBotTokenSourceManual, "Manual Override Token"},
		{service.EmailProviderFastmail, "Fastmail"},
	} {
		tag := optionTagByValue(body, option.value)
		if !strings.Contains(tag, "selected") || !strings.Contains(tag, option.label) {
			t.Fatalf("expected option %q to render selected with label %q, got %s", option.value, option.label, tag)
		}
	}
	for _, snippet := range []string{
		`name="email_imap_port" class="input input-bordered" value="1993"`,
		`name="email_smtp_port" class="input input-bordered" value="2587"`,
	} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("Channels response missing normalized setting snippet %q", snippet)
		}
	}
}

func TestChannelsPageDefaultsStillRenderWhenSettingsAreMissing(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTMX().Get("/channels?project_id=default").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected Channels status 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if tag := inputTagByID(body, "channel_telegram_rich_messages_v2"); !strings.Contains(tag, "checked") {
		t.Fatalf("expected Telegram rich messages default checked, got %s", tag)
	}
	if tag := inputTagBeforeText(body, "Mark existing unread messages seen on start"); !strings.Contains(tag, "checked") {
		t.Fatalf("expected mark-existing-seen default checked, got %s", tag)
	}
	for _, option := range []struct {
		value string
		label string
	}{
		{service.SlackBotTokenSourceOAuth, "OAuth Callback Token"},
		{service.EmailProviderCustom, "Custom"},
	} {
		tag := optionTagByValue(body, option.value)
		if !strings.Contains(tag, "selected") || !strings.Contains(tag, option.label) {
			t.Fatalf("expected default option %q to render selected with label %q, got %s", option.value, option.label, tag)
		}
	}
	for _, snippet := range []string{
		`name="email_imap_port" class="input input-bordered" value="993"`,
		`name="email_smtp_port" class="input input-bordered" value="587"`,
		`name="email_poll_interval_seconds" class="input input-bordered" value="15"`,
	} {
		if !strings.Contains(body, snippet) {
			t.Fatalf("Channels response missing default setting snippet %q", snippet)
		}
	}
	if strings.Contains(body, "Explicit targets allowed") {
		t.Fatal("explicit send targets should default disabled when project setting is missing")
	}
}

func BenchmarkHandlerChannelsSettingsBatchedContention(b *testing.B) {
	db := testutil.NewTestDB(b)
	h, e, _ := setupTestHandlerForDB(b, db)
	project := createProjectTB(b, h, "Channels Settings Benchmark Project")
	settingsRepo := repository.NewSettingsRepo(db)
	seedRepresentativeChannelsSettings(b, settingsRepo, project.ID, "other-project")
	h.settingsRepo = settingsRepo
	h.SetChannelTargetRepo(repository.NewChannelTargetRepo(db))

	var totalLightweightLatency int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		queryAcquired := make(chan struct{})
		var once sync.Once
		settingsRepo.SetQueryAcquiredObserver(func(query string) {
			if strings.Contains(strings.ToLower(query), "from app_settings") {
				once.Do(func() { close(queryAcquired) })
			}
		})
		errCh := make(chan error, 1)
		go func() {
			rec := htmxGet(e, "/channels?project_id="+url.QueryEscape(project.ID))
			if rec.Code != http.StatusOK {
				errCh <- fmt.Errorf("Channels request status=%d", rec.Code)
				return
			}
			errCh <- nil
		}()
		select {
		case <-queryAcquired:
		case err := <-errCh:
			b.Fatalf("Channels request ended before app_settings query started: %v", err)
		case <-time.After(2 * time.Second):
			b.Fatal("Channels app_settings query did not start")
		}
		lightweightStart := time.Now()
		if _, err := h.projectSvc.List(context.Background()); err != nil {
			b.Fatalf("lightweight project list: %v", err)
		}
		totalLightweightLatency += time.Since(lightweightStart).Nanoseconds()
		if err := <-errCh; err != nil {
			b.Fatal(err)
		}
		settingsRepo.SetQueryAcquiredObserver(nil)
	}
	b.ReportMetric(float64(totalLightweightLatency)/float64(b.N), "lightweight_db_block_ns/op")
}

func seedRepresentativeChannelsSettings(t testing.TB, settingsRepo *repository.SettingsRepo, projectID, otherProjectID string) {
	t.Helper()
	ctx := context.Background()
	settings := map[string]string{
		service.TelegramSettingBotToken:                                       "telegram-batch-token",
		service.TelegramSettingSendResponses:                                  "false",
		service.TelegramSettingRichMessagesV2:                                 "true",
		service.GitHubSettingAuthMode:                                         service.GitHubAuthModeApp,
		service.GitHubSettingAppID:                                            "98765",
		service.GitHubSettingAppSlug:                                          "batch-app",
		service.GitHubSettingAppPrivateKey:                                    "batch-private-key",
		service.GitHubSettingPAT:                                              "batch-pat",
		service.GitHubSettingAPIEndpoint:                                      "https://ghe.example/api/v3",
		service.SlackSettingClientID:                                          "slack-client-id",
		service.SlackSettingClientSecret:                                      "slack-client-secret",
		service.SlackSettingAppToken:                                          "slack-app-token",
		service.SlackSettingBotTokenOverride:                                  "slack-bot-override",
		service.SlackSettingBotTokenSource:                                    service.SlackBotTokenSourceManual,
		service.SlackSettingBotToken:                                          "slack-oauth-bot-token",
		service.SlackSettingSendResponses:                                     "false",
		service.DiscordSettingBotToken:                                        "discord-bot-token",
		service.DiscordSettingSendResponses:                                   "false",
		service.EmailSettingProvider:                                          service.EmailProviderFastmail,
		service.EmailSettingAddress:                                           "bot@example.com",
		service.EmailSettingPassword:                                          "email-secret",
		service.EmailSettingIMAPHost:                                          "imap.example.com",
		service.EmailSettingIMAPPort:                                          "1993",
		service.EmailSettingSMTPHost:                                          "smtp.example.com",
		service.EmailSettingSMTPPort:                                          "2587",
		service.EmailSettingPollIntervalSeconds:                               "45",
		service.EmailSettingSendResponses:                                     "false",
		service.EmailSettingSkipAttachments:                                   "true",
		service.EmailSettingMarkExistingSeenOnStart:                           "false",
		service.SendMessageAllowExplicitTargetsSetting + ":" + projectID:      "true",
		service.SendMessageAllowExplicitTargetsSetting + ":" + otherProjectID: "false",
	}
	for key, value := range settings {
		if err := settingsRepo.Set(ctx, key, value); err != nil {
			t.Fatalf("seed setting %s: %v", key, err)
		}
	}
}

func TestChannelsPageExplicitTargetsSettingMatchesFullPageAndHTMX(t *testing.T) {
	tc := NewTestContext(t)
	if err := tc.settingsRepo.Set(context.Background(), service.SendMessageAllowExplicitTargetsSetting+":default", "true"); err != nil {
		t.Fatalf("failed to seed explicit target policy: %v", err)
	}

	full := tc.HTTP().Get("/channels?project_id=default").Execute()
	if full.Code != http.StatusOK {
		t.Fatalf("expected full-page status 200, got %d", full.Code)
	}
	htmx := tc.HTMX().Get("/channels?project_id=default").Execute()
	if htmx.Code != http.StatusOK {
		t.Fatalf("expected HTMX status 200, got %d", htmx.Code)
	}

	fullBody := full.Body.String()
	htmxBody := htmx.Body.String()
	if !strings.Contains(fullBody, "Explicit targets allowed") || !strings.Contains(htmxBody, "Explicit targets allowed") {
		t.Fatalf("expected full-page and HTMX responses to render explicit-targets badge; full=%t htmx=%t", strings.Contains(fullBody, "Explicit targets allowed"), strings.Contains(htmxBody, "Explicit targets allowed"))
	}

	fullPolicy := outboundTargetsPolicyControlMarkup(t, fullBody)
	htmxPolicy := outboundTargetsPolicyControlMarkup(t, htmxBody)
	if fullPolicy != htmxPolicy {
		t.Fatalf("explicit-targets policy control differed between full-page and HTMX responses\nfull: %s\nhtmx: %s", fullPolicy, htmxPolicy)
	}
	if !strings.Contains(fullPolicy, "checked") {
		t.Fatalf("expected explicit-targets policy control to render checked, got %s", fullPolicy)
	}
}

func outboundTargetsPolicyControlMarkup(t *testing.T, body string) string {
	t.Helper()
	marker := "Allow explicit unsaved targets"
	markerIdx := strings.Index(body, marker)
	if markerIdx == -1 {
		t.Fatalf("missing explicit-targets policy marker %q", marker)
	}
	labelStart := strings.LastIndex(body[:markerIdx], "<label")
	if labelStart == -1 {
		t.Fatalf("missing explicit-targets policy label before %q", marker)
	}
	labelEnd := strings.Index(body[markerIdx:], "</label>")
	if labelEnd == -1 {
		t.Fatalf("missing explicit-targets policy closing label after %q", marker)
	}
	return body[labelStart : markerIdx+labelEnd+len("</label>")]
}

func TestChannelsPageOutboundTargetTestButtonIncludesSelectedProjectID(t *testing.T) {
	tc := NewTestContext(t)
	firstProject := tc.CreateProject().WithName("First Project").Build()
	selectedProject := tc.CreateProject().WithName("Selected Project").Build()
	if firstProject.ID == selectedProject.ID {
		t.Fatal("expected distinct projects")
	}

	targetRepo := repository.NewChannelTargetRepo(tc.db)
	slack := &outboundTargetTestSlack{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetSlackService(slack)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	target := models.ChannelTarget{
		ID:        repository.NewID(),
		ProjectID: selectedProject.ID,
		Platform:  "slack",
		Name:      "selected-alerts",
		TargetID:  "CSELECTED",
	}
	if err := targetRepo.Upsert(context.Background(), target); err != nil {
		t.Fatalf("failed to seed selected project target: %v", err)
	}

	pagePath := "/channels?project_id=" + url.QueryEscape(selectedProject.ID)
	rec := tc.HTTP().Get(pagePath).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	expectedPost := `/channels/outbound-targets/` + target.ID + `/test?project_id=` + selectedProject.ID
	if !strings.Contains(body, `hx-post="`+expectedPost+`"`) {
		t.Fatalf("expected rendered Test button to include selected project id %q, body=%s", expectedPost, body)
	}
	if strings.Contains(body, `/channels/outbound-targets/`+target.ID+`/test"`) {
		t.Fatal("rendered Test button must not use a bare target test URL")
	}

	testRec := tc.HTMX().Post(expectedPost).Execute()
	if testRec.Code != http.StatusOK {
		t.Fatalf("expected rendered Test URL to succeed for selected project, got %d body=%s", testRec.Code, testRec.Body.String())
	}
	if slack.channelID != "CSELECTED" {
		t.Fatalf("expected test send through selected project target, got channel %q", slack.channelID)
	}
}

func TestChannelsPageConnectedCardsHideTokenSpecificTextAndActions(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.SetGitHubService(&fakeGitHubService{
		statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{
				Configured: true,
				Connected:  true,
				AuthMode:   service.GitHubAuthModePAT,
			}, nil
		},
	})

	if err := h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"); err != nil {
		t.Fatalf("failed to seed telegram token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `data-channel-type="github"`) {
		t.Fatal("expected connected GitHub card")
	}
	if !strings.Contains(body, `data-channel-type="telegram"`) {
		t.Fatal("expected connected Telegram card")
	}
	if strings.Contains(body, `data-channel-type="slack"`) {
		t.Fatal("did not expect Slack card when not configured")
	}
	if strings.Contains(body, `data-channel-type="discord"`) {
		t.Fatal("did not expect Discord card when not configured")
	}
	if strings.Contains(body, "Clear Token") {
		t.Fatal("did not expect Clear Token action on connected GitHub card")
	}
	if strings.Contains(body, "Token configured") {
		t.Fatal("did not expect token configured text on connected Telegram card")
	}
	if !strings.Contains(body, `data-icon="telegram-brand"`) {
		t.Fatal("expected Telegram brand icon marker on connected Telegram card")
	}
	// Check icon uses currentColor (theme-adaptive) not hardcoded colors
	if !strings.Contains(body, `fill="currentColor"`) {
		t.Fatal("expected Telegram icon to use fill=\"currentColor\" for theme adaptation")
	}
	// Verify it's the official Simple Icons Telegram path (not custom/broken)
	if !strings.Contains(body, `M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12`) {
		t.Fatal("expected official Telegram icon path from Simple Icons")
	}
	if strings.Contains(body, `fill="#229ED9"`) || strings.Contains(body, `fill="#fff"`) {
		t.Fatal("Telegram icon should not use hardcoded brand colors")
	}
	if strings.Contains(body, `M8 10h.01M12 10h.01M16 10h.01`) {
		t.Fatal("did not expect legacy chat bubble icon path for Telegram card")
	}
}

func TestChannelsPageStatusBadgesRenderAtBottomOfDetailsSection(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)

	h.SetGitHubService(&fakeGitHubService{
		statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{
				Configured:   true,
				Connected:    true,
				AuthMode:     service.GitHubAuthModePAT,
				AccountLogin: "ov-user",
			}, nil
		},
	})
	h.SetSlackService(&fakeSlackService{
		statusFn: func(ctx context.Context) (service.SlackConnectionStatus, error) {
			return service.SlackConnectionStatus{Configured: true, Connected: true, TeamName: "OpenVibely"}, nil
		},
	})
	h.SetDiscordService(&fakeDiscordService{
		statusFn: func(ctx context.Context) (service.DiscordConnectionStatus, error) {
			return service.DiscordConnectionStatus{Configured: true, Connected: true, Running: true, BotUserID: "bot-1"}, nil
		},
	})

	if err := h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"); err != nil {
		t.Fatalf("failed to seed telegram token: %v", err)
	}

	if h.slackAuthRepo != nil {
		if err := h.slackAuthRepo.Create(context.Background(), &models.SlackAuthorizedUser{
			ProjectID:   "default",
			SlackUserID: "U123",
			DisplayName: "Slack User",
			AddedBy:     "test",
		}); err != nil {
			t.Fatalf("failed to seed slack authorized user: %v", err)
		}
	}
	if h.telegramAuthRepo == nil {
		h.SetTelegramAuthRepo(repository.NewTelegramAuthRepo(db))
	}
	if err := h.telegramAuthRepo.Create(context.Background(), &models.TelegramAuthorizedUser{
		ProjectID:        "default",
		TelegramUserID:   1001,
		TelegramUsername: "tguser",
		DisplayName:      "Telegram User",
		AddedBy:          "test",
	}); err != nil {
		t.Fatalf("failed to seed telegram authorized user: %v", err)
	}
	if h.discordAuthRepo == nil {
		h.SetDiscordAuthRepo(repository.NewDiscordAuthRepo(db))
	}
	if err := h.discordAuthRepo.Create(context.Background(), &models.DiscordAuthorizedUser{
		ProjectID:     "default",
		DiscordUserID: "1002",
		DisplayName:   "Discord User",
		AddedBy:       "test",
	}); err != nil {
		t.Fatalf("failed to seed discord authorized user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, channelType := range []string{"github", "slack", "discord", "telegram"} {
		card := cardSectionByType(body, channelType)
		if card == "" {
			t.Fatalf("expected %s card to render", channelType)
		}
		title := titleSection(card)
		if strings.Contains(title, "badge") {
			t.Fatalf("expected %s title row to not include status badge", channelType)
		}
	}

	githubCard := cardSectionByType(body, "github")
	if !strings.Contains(githubCard, `<span class="badge badge-sm badge-success">Connected</span>`) {
		t.Fatal("expected github connected badge in details section")
	}
	assertIndexOrder(
		t,
		githubCard,
		`Account: ov-user`,
		`<span class="badge badge-sm badge-success">Connected</span>`,
		"expected github status badge below account metadata",
	)

	slackCard := cardSectionByType(body, "slack")
	if !strings.Contains(slackCard, `<span class="badge badge-sm badge-success">Connected</span>`) {
		t.Fatal("expected slack connected badge in details section")
	}
	assertIndexOrder(
		t,
		slackCard,
		`Authorized users:</span>`,
		`<span class="badge badge-sm badge-success">Connected</span>`,
		"expected slack status badge below authorized users",
	)

	discordCard := cardSectionByType(body, "discord")
	if !strings.Contains(discordCard, `<span class="badge badge-sm badge-success">Connected</span>`) {
		t.Fatal("expected discord connected badge in details section")
	}
	assertIndexOrder(
		t,
		discordCard,
		`Authorized users:</span>`,
		`<span class="badge badge-sm badge-success">Connected</span>`,
		"expected discord status badge below authorized users",
	)

	telegramCard := cardSectionByType(body, "telegram")
	if !strings.Contains(telegramCard, `<span class="badge badge-sm badge-warning">Not Connected</span>`) && !strings.Contains(telegramCard, `<span class="badge badge-sm badge-success">Connected</span>`) {
		t.Fatal("expected telegram status badge in details section")
	}
	if strings.Contains(telegramCard, `<span class="badge badge-sm badge-warning">Not Connected</span>`) {
		assertIndexOrder(
			t,
			telegramCard,
			`Authorized users:</span>`,
			`<span class="badge badge-sm badge-warning">Not Connected</span>`,
			"expected telegram status badge below authorized users",
		)
	}
	if strings.Contains(telegramCard, `<span class="badge badge-sm badge-success">Connected</span>`) {
		assertIndexOrder(
			t,
			telegramCard,
			`Authorized users:</span>`,
			`<span class="badge badge-sm badge-success">Connected</span>`,
			"expected telegram status badge below authorized users",
		)
	}
}

func TestChannelsPageTelegramMenuShowsDeleteAndNoChannelMenuUsesRemove(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.SetGitHubService(&fakeGitHubService{
		statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{Configured: true, Connected: true, AuthMode: service.GitHubAuthModePAT}, nil
		},
	})
	h.SetSlackService(&fakeSlackService{
		statusFn: func(ctx context.Context) (service.SlackConnectionStatus, error) {
			return service.SlackConnectionStatus{Configured: true, Connected: true}, nil
		},
	})
	h.SetDiscordService(&fakeDiscordService{
		statusFn: func(ctx context.Context) (service.DiscordConnectionStatus, error) {
			return service.DiscordConnectionStatus{Configured: true, Connected: true, Running: true}, nil
		},
	})
	if err := h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"); err != nil {
		t.Fatalf("failed to seed telegram token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, channelType := range []string{"github", "slack", "discord", "telegram"} {
		card := cardSectionByType(body, channelType)
		if card == "" {
			t.Fatalf("expected %s card to render", channelType)
		}
		if !strings.Contains(card, "Delete") {
			t.Fatalf("expected %s card menu to contain Delete action", channelType)
		}
		if strings.Contains(card, "Remove") {
			t.Fatalf("did not expect %s card menu to contain Remove action", channelType)
		}
	}
	if strings.Contains(body, ">Remove<") {
		t.Fatal("did not expect any channels card action to render Remove")
	}
}

func TestChannelsPageDeleteActionsUseConfirmationDialog(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	webhookRepo := repository.NewWebhookRepo(db)
	h.SetWebhookRepo(webhookRepo)

	h.SetGitHubService(&fakeGitHubService{
		statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{Configured: true, Connected: true, AuthMode: service.GitHubAuthModePAT}, nil
		},
	})
	h.SetSlackService(&fakeSlackService{
		statusFn: func(ctx context.Context) (service.SlackConnectionStatus, error) {
			return service.SlackConnectionStatus{Configured: true, Connected: true}, nil
		},
	})
	h.SetDiscordService(&fakeDiscordService{
		statusFn: func(ctx context.Context) (service.DiscordConnectionStatus, error) {
			return service.DiscordConnectionStatus{Configured: true, Connected: true, Running: true}, nil
		},
	})
	if err := h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"); err != nil {
		t.Fatalf("failed to seed telegram token: %v", err)
	}
	webhook := &models.WebhookEndpoint{ProjectID: "default", Name: "Deploy Alerts", Enabled: true, DefaultPriority: 2}
	if err := webhookRepo.Create(context.Background(), webhook); err != nil {
		t.Fatalf("failed to seed webhook: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, marker := range []string{
		`id="delete_channel_confirm_modal"`,
		`id="delete_channel_name"`,
		`onclick="closeDeleteChannelConfirm()"`,
		`class="btn btn-error" onclick="confirmDeleteChannel()"`,
		`Are you sure you want to delete`,
		`This action cannot be undone.`,
		`htmx.ajax(pendingDeleteChannelMethod, pendingDeleteChannelURL, { swap: 'none' })`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("expected channels delete confirmation markup to contain %q", marker)
		}
	}

	for _, tc := range []struct {
		channelType string
		name        string
		url         string
		method      string
	}{
		{channelType: "github", name: "GitHub", url: "/channels/github/remove", method: "POST"},
		{channelType: "slack", name: "Slack", url: "/channels/slack/remove", method: "POST"},
		{channelType: "discord", name: "Discord", url: "/channels/discord/remove", method: "POST"},
		{channelType: "telegram", name: "Telegram Bot", url: "/channels/telegram/remove", method: "POST"},
		{channelType: "webhook", name: "Deploy Alerts", url: "/channels/webhooks/" + webhook.ID, method: "DELETE"},
	} {
		card := cardSectionByType(body, tc.channelType)
		if !strings.Contains(card, `class="text-error"`) {
			t.Fatalf("expected %s delete action to use text-error class", tc.channelType)
		}
		if !strings.Contains(card, "openDeleteChannelConfirm") {
			t.Fatalf("expected %s delete action to open confirmation dialog", tc.channelType)
		}
		if !strings.Contains(card, tc.name) {
			t.Fatalf("expected %s delete action to identify %q", tc.channelType, tc.name)
		}
		if !strings.Contains(card, tc.url) {
			t.Fatalf("expected %s delete action to preserve route %q", tc.channelType, tc.url)
		}
		if !strings.Contains(card, tc.method) {
			t.Fatalf("expected %s delete action to preserve method %q", tc.channelType, tc.method)
		}
		if !strings.Contains(card, `data-card-select-eligible="true"`) {
			t.Fatalf("expected %s channel card to be selectable", tc.channelType)
		}
		if tc.channelType != "webhook" {
			for _, marker := range []string{`data-card-select-id="channel:` + tc.channelType + `"`, `data-card-delete-url="` + tc.url, `data-card-delete-method="` + tc.method + `"`} {
				if !strings.Contains(card, marker) {
					t.Fatalf("expected %s selectable channel card to contain %q", tc.channelType, marker)
				}
			}
		}
		if strings.Contains(card, `hx-confirm="Delete this `) {
			t.Fatalf("did not expect %s delete action to use immediate delete hx-confirm", tc.channelType)
		}
	}

	if strings.Contains(body, `hx-confirm="Delete this GitHub channel configuration?"`) ||
		strings.Contains(body, `hx-confirm="Delete this Slack channel configuration?"`) ||
		strings.Contains(body, `hx-confirm="Delete this Discord channel configuration?"`) ||
		strings.Contains(body, `hx-confirm="Delete this Telegram channel configuration?"`) ||
		strings.Contains(body, `hx-confirm="Delete this webhook configuration?"`) {
		t.Fatal("did not expect channel delete actions to use immediate hx-confirm")
	}
}

func TestChannelsPagePasswordToggleButtonStaysFixedOnActive(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	if strings.Contains(body, "translate(0.25rem, -50%)") {
		t.Fatal("password toggle active state still shifts horizontally")
	}

	if !strings.Contains(body, "translate(0, -50%) !important") {
		t.Fatal("expected password toggle active/focus transform to keep fixed position")
	}
	if !strings.Contains(body, `.password-toggle-btn.top-2:focus-visible`) || !strings.Contains(body, "translate(0, 0) !important") {
		t.Fatal("expected textarea secret toggle focus transform to keep top-aligned position")
	}

	if !strings.Contains(body, `onclick="togglePasswordVisibility('channel_telegram_token', this)"`) {
		t.Fatal("expected password visibility toggle onclick handler")
	}

	for _, want := range []string{
		`id="webhook_secret_display"`,
		`type="password"`,
		`readonly`,
		`autocomplete="off"`,
		`onclick="togglePasswordVisibility('webhook_secret_display', this)"`,
		`aria-label="Toggle secret visibility"`,
		`aria-pressed="false"`,
		`button.setAttribute('aria-pressed', willReveal ? 'true' : 'false')`,
		`function resetSecretInputVisibility(inputId)`,
		`resetSecretInputVisibility('channel_telegram_token')`,
		`resetSecretInputVisibility('github_pat')`,
		`resetSecretTextareaVisibility('github_app_private_key')`,
		`resetSecretInputVisibility('slack_client_secret')`,
		`resetSecretInputVisibility('slack_app_token')`,
		`resetSecretInputVisibility('slack_bot_token')`,
		`resetSecretInputVisibility('webhook_secret_display')`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected webhook secret input pattern to contain %q", want)
		}
	}

	if strings.Contains(body, `onclick="togglePasswordVisibility('webhook_secret_display', this)" tabindex="-1"`) {
		t.Fatal("expected webhook secret reveal toggle to remain keyboard reachable")
	}

	if !strings.Contains(body, `class="eye-open`) || !strings.Contains(body, `class="eye-closed`) {
		t.Fatal("expected both eye icons for show/hide token toggle")
	}
}
