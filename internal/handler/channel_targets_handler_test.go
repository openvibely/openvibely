package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

type outboundTargetTestDiscord struct {
	channelID string
	threadID  string
	userID    string
	text      string
}

func (d *outboundTargetTestDiscord) SendOutboundMessage(_ context.Context, channelID, threadID, text string) service.SendMessageResult {
	d.channelID = channelID
	d.threadID = threadID
	d.text = text
	return service.SendMessageResult{OK: true, Platform: "discord", Target: "discord:" + channelID + ":" + threadID, MessageID: "discord-ch-1"}
}

func (d *outboundTargetTestDiscord) SendOutboundDirectMessage(_ context.Context, userID, text string) service.SendMessageResult {
	d.userID = userID
	d.text = text
	return service.SendMessageResult{OK: true, Platform: "discord", Target: "discord:user:" + userID, MessageID: "discord-dm-1"}
}

type outboundTargetTestSlack struct {
	channelID string
	threadTS  string
	userID    string
	text      string
}

func (s *outboundTargetTestSlack) SendOutboundMessage(ctx context.Context, channelID, threadTS, text string) service.SendMessageResult {
	_ = ctx
	s.channelID = channelID
	s.threadTS = threadTS
	s.text = text
	return service.SendMessageResult{OK: true, Platform: "slack", Target: "slack:" + channelID + ":" + threadTS, MessageID: "123.456"}
}

func (s *outboundTargetTestSlack) SendOutboundDirectMessage(_ context.Context, userID, text string) service.SendMessageResult {
	s.userID = userID
	s.text = text
	return service.SendMessageResult{OK: true, Platform: "slack", Target: "slack:" + userID, MessageID: "dm.1"}
}

func TestOutboundTargetHandlersDenyCrossProjectTargetIDs(t *testing.T) {
	tc := NewTestContext(t)
	ownerProject := tc.CreateProject().WithName("Owner Project").Build()
	otherProject := tc.CreateProject().WithName("Other Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	slack := &outboundTargetTestSlack{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetSlackService(slack)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	target := models.ChannelTarget{
		ID:        repository.NewID(),
		ProjectID: ownerProject.ID,
		Platform:  "slack",
		Name:      "alerts",
		TargetID:  "COWNER",
	}
	if err := targetRepo.Upsert(context.Background(), target); err != nil {
		t.Fatalf("upsert target: %v", err)
	}

	editForm := url.Values{}
	editForm.Set("project_id", otherProject.ID)
	editForm.Add("target_row_id", target.ID)
	editForm.Add("target_platform", "slack")
	editForm.Add("target_name", "stolen")
	editForm.Add("target_target_id", "COTHER")
	editForm.Add("target_thread_id", "")
	editForm.Add("target_is_home", "false")
	editForm.Add("target_default_subject", "")
	editReq := httptest.NewRequest(http.MethodPost, "/channels/send-message-explicit-targets", strings.NewReader(editForm.Encode()))
	editReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	editRec := httptest.NewRecorder()
	tc.echo.ServeHTTP(editRec, editReq)
	if editRec.Code != http.StatusNotFound {
		t.Fatalf("expected cross-project edit to be denied with 404, got %d body=%s", editRec.Code, editRec.Body.String())
	}
	stored, err := targetRepo.GetByID(context.Background(), target.ID)
	if err != nil || stored == nil || stored.ProjectID != ownerProject.ID || stored.TargetID != "COWNER" || stored.Name != "alerts" {
		t.Fatalf("target should remain unchanged after cross-project edit, target=%+v err=%v", stored, err)
	}

	deletePath := "/channels/outbound-targets/" + target.ID + "?project_id=" + url.QueryEscape(ownerProject.ID)
	deleteRec := tc.HTMX().Delete(deletePath).Execute()
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("expected direct delete route to be unavailable so deletes require Save Settings, got %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	stored, err = targetRepo.GetByID(context.Background(), target.ID)
	if err != nil || stored == nil {
		t.Fatalf("target should remain after unavailable direct delete, target=%v err=%v", stored, err)
	}

	testPath := "/channels/outbound-targets/" + target.ID + "/test?project_id=" + url.QueryEscape(otherProject.ID)
	testRec := tc.HTMX().Post(testPath).Execute()
	if testRec.Code != http.StatusNotFound {
		t.Fatalf("expected cross-project test send to be denied with 404, got %d body=%s", testRec.Code, testRec.Body.String())
	}
	if slack.channelID != "" || slack.text != "" {
		t.Fatalf("cross-project test send should not dispatch, channel=%q text=%q", slack.channelID, slack.text)
	}

	otherTarget := models.ChannelTarget{ID: repository.NewID(), ProjectID: otherProject.ID, Platform: "slack", Name: "other", TargetID: "COTHER"}
	if err := targetRepo.Upsert(context.Background(), otherTarget); err != nil {
		t.Fatalf("upsert other target: %v", err)
	}
	validThenInvalid := url.Values{}
	validThenInvalid.Set("project_id", ownerProject.ID)
	validThenInvalid.Add("target_row_id", target.ID)
	validThenInvalid.Add("target_platform", "slack")
	validThenInvalid.Add("target_name", "updated")
	validThenInvalid.Add("target_target_id", "CUPDATED")
	validThenInvalid.Add("target_thread_id", "")
	validThenInvalid.Add("target_is_home", "false")
	validThenInvalid.Add("target_default_subject", "")
	validThenInvalid.Add("target_row_id", otherTarget.ID)
	validThenInvalid.Add("target_platform", "slack")
	validThenInvalid.Add("target_name", "stolen")
	validThenInvalid.Add("target_target_id", "COTHER")
	validThenInvalid.Add("target_thread_id", "")
	validThenInvalid.Add("target_is_home", "false")
	validThenInvalid.Add("target_default_subject", "")
	invalidReq := httptest.NewRequest(http.MethodPost, "/channels/send-message-explicit-targets", strings.NewReader(validThenInvalid.Encode()))
	invalidReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalidRec := httptest.NewRecorder()
	tc.echo.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusNotFound {
		t.Fatalf("expected draft save with cross-project row to be denied with 404, got %d body=%s", invalidRec.Code, invalidRec.Body.String())
	}
	stored, err = targetRepo.GetByID(context.Background(), target.ID)
	if err != nil || stored == nil || stored.ProjectID != ownerProject.ID || stored.TargetID != "COWNER" || stored.Name != "alerts" {
		t.Fatalf("valid rows before an invalid draft row must not be persisted, target=%+v err=%v", stored, err)
	}
}

func TestOutboundTargetsPersistOnlyOnSaveSettings(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Staged Outbound Targets").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	tc.handler.SetChannelTargetRepo(targetRepo)

	keep := models.ChannelTarget{ID: repository.NewID(), ProjectID: project.ID, Platform: "email", Name: "keep", TargetID: "keep@example.com"}
	remove := models.ChannelTarget{ID: repository.NewID(), ProjectID: project.ID, Platform: "slack", Name: "remove", TargetID: "CREMOVE"}
	if err := targetRepo.Upsert(context.Background(), keep); err != nil {
		t.Fatalf("upsert keep target: %v", err)
	}
	if err := targetRepo.Upsert(context.Background(), remove); err != nil {
		t.Fatalf("upsert remove target: %v", err)
	}

	cardBefore := tc.HTTP().Get("/channels/outbound-targets/card?project_id=" + url.QueryEscape(project.ID)).Execute()
	if cardBefore.Code != http.StatusOK || !strings.Contains(cardBefore.Body.String(), "email: 1") || !strings.Contains(cardBefore.Body.String(), "slack: 1") || !strings.Contains(cardBefore.Body.String(), "Saved targets only") {
		t.Fatalf("expected persisted card before save, status=%d body=%s", cardBefore.Code, cardBefore.Body.String())
	}
	// Simulating client-side draft add/delete/toggle without submitting Save Settings: persisted state is unchanged.
	cardStill := tc.HTTP().Get("/channels/outbound-targets/card?project_id=" + url.QueryEscape(project.ID)).Execute()
	if cardStill.Code != http.StatusOK || !strings.Contains(cardStill.Body.String(), "email: 1") || !strings.Contains(cardStill.Body.String(), "slack: 1") || !strings.Contains(cardStill.Body.String(), "Saved targets only") {
		t.Fatalf("expected unsaved draft changes to be discarded, status=%d body=%s", cardStill.Code, cardStill.Body.String())
	}

	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("enabled", "true")
	form.Add("target_row_id", keep.ID)
	form.Add("target_platform", "email")
	form.Add("target_name", "keep")
	form.Add("target_target_id", "keep@example.com")
	form.Add("target_thread_id", "")
	form.Add("target_is_home", "false")
	form.Add("target_default_subject", "")
	form.Add("target_row_id", "")
	form.Add("target_platform", "email")
	form.Add("target_name", "")
	form.Add("target_target_id", "billing@example.com")
	form.Add("target_thread_id", "")
	form.Add("target_is_home", "false")
	form.Add("target_default_subject", "")
	form.Add("target_row_id", "")
	form.Add("target_platform", "discord")
	form.Add("target_name", "ops")
	form.Add("target_target_id", "123456789")
	form.Add("target_thread_id", "987654321")
	form.Add("target_is_home", "false")
	form.Add("target_default_subject", "")
	req := httptest.NewRequest(http.MethodPost, "/channels/send-message-explicit-targets", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected save status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "outbound-targets-card-refresh" {
		t.Fatalf("expected card refresh trigger, got %q", rec.Header().Get("HX-Trigger"))
	}

	targets, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("list targets after save: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("expected save to reconcile target list to three rows, got %+v", targets)
	}
	var sawKeep, sawNew, sawDiscord, sawRemoved bool
	for _, target := range targets {
		sawKeep = sawKeep || target.ID == keep.ID
		sawNew = sawNew || target.Platform == "email" && target.Name == "" && target.TargetID == "billing@example.com"
		sawDiscord = sawDiscord || target.Platform == "discord" && target.Name == "ops" && target.TargetID == "123456789" && target.ThreadID == "987654321"
		sawRemoved = sawRemoved || target.ID == remove.ID
	}
	if !sawKeep || !sawNew || !sawDiscord || sawRemoved {
		t.Fatalf("unexpected reconciled targets: keep=%v new=%v discord=%v removed=%v targets=%+v", sawKeep, sawNew, sawDiscord, sawRemoved, targets)
	}

	cardAfter := tc.HTTP().Get("/channels/outbound-targets/card?project_id=" + url.QueryEscape(project.ID)).Execute()
	if cardAfter.Code != http.StatusOK || !strings.Contains(cardAfter.Body.String(), "email: 2") || !strings.Contains(cardAfter.Body.String(), "discord: 1") || !strings.Contains(cardAfter.Body.String(), "Explicit targets allowed") || strings.Contains(cardAfter.Body.String(), "slack: 1") {
		t.Fatalf("expected card after save to reflect reconciled targets and policy, status=%d body=%s", cardAfter.Code, cardAfter.Body.String())
	}

	duplicateForm := url.Values{}
	duplicateForm.Set("project_id", project.ID)
	duplicateForm.Add("target_row_id", keep.ID)
	duplicateForm.Add("target_platform", "email")
	duplicateForm.Add("target_name", "keep")
	duplicateForm.Add("target_target_id", "keep@example.com")
	duplicateForm.Add("target_thread_id", "")
	duplicateForm.Add("target_is_home", "false")
	duplicateForm.Add("target_default_subject", "")
	duplicateForm.Add("target_row_id", "")
	duplicateForm.Add("target_platform", "email")
	duplicateForm.Add("target_name", "")
	duplicateForm.Add("target_target_id", "keep@example.com")
	duplicateForm.Add("target_thread_id", "")
	duplicateForm.Add("target_is_home", "false")
	duplicateForm.Add("target_default_subject", "")
	duplicateReq := httptest.NewRequest(http.MethodPost, "/channels/send-message-explicit-targets", strings.NewReader(duplicateForm.Encode()))
	duplicateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	duplicateRec := httptest.NewRecorder()
	tc.echo.ServeHTTP(duplicateRec, duplicateReq)
	if duplicateRec.Code != http.StatusOK || duplicateRec.Header().Get("HX-Trigger") != "outbound-targets-save-error" || !strings.Contains(duplicateRec.Body.String(), "Duplicate outbound target destination") {
		t.Fatalf("expected inline duplicate destination validation, got %d trigger=%q body=%s", duplicateRec.Code, duplicateRec.Header().Get("HX-Trigger"), duplicateRec.Body.String())
	}
	targetsAfterDuplicate, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(targetsAfterDuplicate) != 3 {
		t.Fatalf("duplicate validation should not mutate saved targets, targets=%+v err=%v", targetsAfterDuplicate, err)
	}
}

func TestOutboundTargetDraftTestSendsWithoutPersisting(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Outbound Draft Test Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	slack := &outboundTargetTestSlack{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetSlackService(slack)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("target_platform", "slack")
	form.Set("target_target_id", "CDRAFT")
	form.Set("target_thread_id", "1690000000.000000")
	form.Set("target_default_subject", "")
	req := httptest.NewRequest(http.MethodPost, "/channels/outbound-targets/test-draft", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if slack.channelID != "CDRAFT" || slack.threadTS != "1690000000.000000" || slack.text != "Test message from OpenVibely" {
		t.Fatalf("unexpected draft test send channel=%q thread=%q text=%q", slack.channelID, slack.threadTS, slack.text)
	}
	targets, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(targets) != 0 {
		t.Fatalf("draft test must not persist targets, targets=%+v err=%v", targets, err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `>✓</span><span>Sent</span>`) || !strings.Contains(body, `text-success`) || strings.Contains(body, "&#34;ok&#34;") || strings.Contains(body, `{"ok"`) || strings.Contains(body, `alert alert-success`) {
		t.Fatalf("expected compact button-local success result with green check and without raw JSON or banner, got %q", body)
	}
}

func TestOutboundTargetSaveAndDraftTestShareCanonicalValidation(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Outbound Shared Validation Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	slack := &outboundTargetTestSlack{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetSlackService(slack)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	saveForm := url.Values{}
	saveForm.Set("project_id", project.ID)
	saveForm.Add("target_row_id", "")
	saveForm.Add("target_platform", "telegram")
	saveForm.Add("target_name", "topic")
	saveForm.Add("target_target_id", "-100123")
	saveForm.Add("target_thread_id", "not-a-topic-id")
	saveForm.Add("target_is_home", "false")
	saveForm.Add("target_default_subject", "")
	saveRec := tc.HTMX().Post("/channels/send-message-explicit-targets").WithForm(saveForm).Execute()
	if saveRec.Code != http.StatusOK || saveRec.Header().Get("HX-Trigger") != "outbound-targets-save-error" || !strings.Contains(saveRec.Body.String(), "telegram thread id must be an integer") {
		t.Fatalf("expected saved-target form to reject invalid Telegram thread through shared validation, status=%d trigger=%q body=%s", saveRec.Code, saveRec.Header().Get("HX-Trigger"), saveRec.Body.String())
	}
	targets, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(targets) != 0 {
		t.Fatalf("invalid save must not persist targets, targets=%+v err=%v", targets, err)
	}

	draftForm := url.Values{}
	draftForm.Set("project_id", project.ID)
	draftForm.Set("target_platform", "slack")
	draftForm.Set("target_kind", "user")
	draftForm.Set("target_target_id", "CNOTAUSER")
	draftRec := tc.HTMX().Post("/channels/outbound-targets/test-draft").WithForm(draftForm).Execute()
	if draftRec.Code != http.StatusOK || !strings.Contains(draftRec.Body.String(), "Failed") || !strings.Contains(draftRec.Body.String(), "Invalid Slack user ID") {
		t.Fatalf("expected draft test to reject invalid Slack user through shared validation, status=%d body=%s", draftRec.Code, draftRec.Body.String())
	}
	if slack.userID != "" || slack.channelID != "" || slack.text != "" {
		t.Fatalf("invalid draft target must not dispatch, userID=%q channelID=%q text=%q", slack.userID, slack.channelID, slack.text)
	}
}

func TestOutboundTargetDraftTestRendersCleanFailure(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Outbound Draft Failure Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("target_platform", "email")
	form.Set("target_target_id", "draft@example.com")
	req := httptest.NewRequest(http.MethodPost, "/channels/outbound-targets/test-draft", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `>✕</span><span>Failed</span>`) || !strings.Contains(body, `text-error`) || !strings.Contains(body, `title="Test failed:`) || strings.Contains(body, `{"ok"`) || strings.Contains(body, "&#34;ok&#34;") || strings.Contains(body, `alert alert-error`) {
		t.Fatalf("expected compact button-local failure result with red x and without raw JSON or banner, got %q", body)
	}
}

func TestOutboundTargetTestPreservesThreadIDAndEscapesResult(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Outbound Target Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	slack := &outboundTargetTestSlack{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetSlackService(slack)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	target := models.ChannelTarget{
		ID:        repository.NewID(),
		ProjectID: project.ID,
		Platform:  "slack",
		Name:      "alerts",
		TargetID:  "C123",
		ThreadID:  "1690000000.000000",
		Home:      true,
	}
	if err := targetRepo.Upsert(context.Background(), target); err != nil {
		t.Fatalf("upsert target: %v", err)
	}

	path := "/channels/outbound-targets/" + target.ID + "/test?project_id=" + url.QueryEscape(project.ID)
	rec := tc.HTMX().Post(path).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if slack.channelID != "C123" || slack.threadTS != "1690000000.000000" {
		t.Fatalf("sent to channel=%q thread=%q", slack.channelID, slack.threadTS)
	}
	if slack.text != "Test message from OpenVibely" {
		t.Fatalf("unexpected message %q", slack.text)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `>✓</span><span>Sent</span>`) || !strings.Contains(body, `text-success`) {
		t.Fatalf("unexpected body %q", body)
	}
	if strings.Contains(body, "slack:C123:1690000000.000000") || strings.Contains(body, `{"ok":true`) || strings.Contains(body, "&#34;ok&#34;") || strings.Contains(body, `alert alert-success`) {
		t.Fatalf("expected compact button-local success result with green check and without transport JSON or banner, got %q", body)
	}
}

// TestSavingUserDMOutboundTargetPersistsTargetKind verifies that posting target_kind=user
// saves the target with target_kind 'user' and the test button dispatches as a direct message.
func TestSavingUserDMOutboundTargetPersistsTargetKind(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("User DM Target Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	slack := &outboundTargetTestSlack{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetSlackService(slack)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	// POST a single Slack user DM target with target_kind=user.
	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Add("target_row_id", "")
	form.Add("target_platform", "slack")
	form.Add("target_kind", "user")
	form.Add("target_name", "")
	form.Add("target_target_id", "U0AQYLJR14Y")
	form.Add("target_thread_id", "")
	form.Add("target_is_home", "false")
	form.Add("target_default_subject", "")
	form.Set("enabled", "false")

	rec := tc.HTMX().Post("/channels/send-message-explicit-targets").WithForm(form).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify the target was saved with target_kind='user'.
	targets, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].TargetKind != "user" {
		t.Fatalf("expected target_kind=user, got %q", targets[0].TargetKind)
	}
	if targets[0].TargetID != "U0AQYLJR14Y" {
		t.Fatalf("expected target_id=U0AQYLJR14Y, got %q", targets[0].TargetID)
	}

	// Test button for the saved user-kind target must dispatch as a direct message, not a channel.
	path := "/channels/outbound-targets/" + targets[0].ID + "/test?project_id=" + url.QueryEscape(project.ID)
	rec2 := tc.HTMX().Post(path).Execute()
	if rec2.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if slack.userID != "U0AQYLJR14Y" {
		t.Fatalf("expected DM to U0AQYLJR14Y, got channelID=%q userID=%q", slack.channelID, slack.userID)
	}
	if slack.channelID != "" {
		t.Fatalf("user DM target must not dispatch to a Slack channel, got channelID=%q", slack.channelID)
	}
}

// TestOutboundTargetDraftTestUserDMDispatchesAsDM verifies that a draft test for a Slack
// user DM target (target_kind=user) dispatches via SendOutboundDirectMessage.
func TestOutboundTargetDraftTestUserDMDispatchesAsDM(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Draft DM Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	slack := &outboundTargetTestSlack{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetSlackService(slack)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("target_platform", "slack")
	form.Set("target_kind", "user")
	form.Set("target_target_id", "U0AQYLJR14Y")
	form.Set("target_thread_id", "")
	form.Set("target_default_subject", "")

	rec := tc.HTMX().Post("/channels/outbound-targets/test-draft").WithForm(form).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if slack.userID != "U0AQYLJR14Y" {
		t.Fatalf("expected DM to U0AQYLJR14Y, got channelID=%q userID=%q", slack.channelID, slack.userID)
	}
	if slack.channelID != "" {
		t.Fatalf("draft user DM test must not dispatch to a channel, got channelID=%q", slack.channelID)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sent") || !strings.Contains(body, "text-success") {
		t.Fatalf("expected success response, got %q", body)
	}
}

// TestSavingDiscordUserDMOutboundTargetPersistsTargetKind verifies that a Discord user DM
// target posted with target_kind=user is saved with target_kind 'user' and that the test
// button dispatches via SendOutboundDirectMessage, not SendOutboundMessage.
func TestSavingDiscordUserDMOutboundTargetPersistsTargetKind(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Discord User DM Target Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	discord := &outboundTargetTestDiscord{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetDiscordService(discord)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	// POST a single Discord user DM target with target_kind=user.
	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Add("target_row_id", "")
	form.Add("target_platform", "discord")
	form.Add("target_kind", "user")
	form.Add("target_name", "")
	form.Add("target_target_id", "1518288288572641398")
	form.Add("target_thread_id", "")
	form.Add("target_is_home", "false")
	form.Add("target_default_subject", "")
	form.Set("enabled", "false")

	rec := tc.HTMX().Post("/channels/send-message-explicit-targets").WithForm(form).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify the target was saved with target_kind='user'.
	targets, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].TargetKind != "user" {
		t.Fatalf("expected target_kind=user, got %q", targets[0].TargetKind)
	}
	if targets[0].TargetID != "1518288288572641398" {
		t.Fatalf("expected target_id=1518288288572641398, got %q", targets[0].TargetID)
	}

	// Test button for the saved Discord user-kind target must dispatch as a direct message.
	path := "/channels/outbound-targets/" + targets[0].ID + "/test?project_id=" + url.QueryEscape(project.ID)
	rec2 := tc.HTMX().Post(path).Execute()
	if rec2.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if discord.userID != "1518288288572641398" {
		t.Fatalf("expected DM to 1518288288572641398, got channelID=%q userID=%q", discord.channelID, discord.userID)
	}
	if discord.channelID != "" {
		t.Fatalf("Discord user DM target must not dispatch to a channel, got channelID=%q", discord.channelID)
	}
}

// TestDiscordOutboundTargetDraftTestUserDMDispatchesAsDM verifies that a draft test for a
// Discord user DM target (target_kind=user) dispatches via SendOutboundDirectMessage.
func TestDiscordOutboundTargetDraftTestUserDMDispatchesAsDM(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Discord Draft DM Project").Build()
	targetRepo := repository.NewChannelTargetRepo(tc.db)
	discord := &outboundTargetTestDiscord{}
	router := service.NewChannelMessageRouter(targetRepo, tc.settingsRepo)
	router.SetDiscordService(discord)
	tc.handler.SetChannelTargetRepo(targetRepo)
	tc.handler.SetChannelMessageRouter(router)

	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("target_platform", "discord")
	form.Set("target_kind", "user")
	form.Set("target_target_id", "1518288288572641398")
	form.Set("target_thread_id", "")
	form.Set("target_default_subject", "")

	rec := tc.HTMX().Post("/channels/outbound-targets/test-draft").WithForm(form).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if discord.userID != "1518288288572641398" {
		t.Fatalf("expected DM to 1518288288572641398, got channelID=%q userID=%q", discord.channelID, discord.userID)
	}
	if discord.channelID != "" {
		t.Fatalf("Discord draft user DM test must not dispatch to a channel, got channelID=%q", discord.channelID)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sent") || !strings.Contains(body, "text-success") {
		t.Fatalf("expected success response, got %q", body)
	}
}

// TestAuthorizedUsersMutationDoesNotAffectOutboundTargets verifies that adding and removing
// Slack or Discord authorized users does not mutate the outbound channel_targets table.
func TestAuthorizedUsersMutationDoesNotAffectOutboundTargets(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Auth Isolation Project")
	targetRepo := repository.NewChannelTargetRepo(db)
	h.SetChannelTargetRepo(targetRepo)

	// Seed two outbound targets: a Slack channel and a Discord user DM.
	slackChannelTarget := models.ChannelTarget{
		ID: repository.NewID(), ProjectID: project.ID,
		Platform: "slack", TargetKind: "channel", Name: "ops", TargetID: "COPS123",
	}
	discordUserTarget := models.ChannelTarget{
		ID: repository.NewID(), ProjectID: project.ID,
		Platform: "discord", TargetKind: "user", Name: "", TargetID: "1518288288572641398",
	}
	if err := targetRepo.Upsert(context.Background(), slackChannelTarget); err != nil {
		t.Fatalf("upsert slack channel target: %v", err)
	}
	if err := targetRepo.Upsert(context.Background(), discordUserTarget); err != nil {
		t.Fatalf("upsert discord user target: %v", err)
	}

	assertTargetCount := func(label string, want int) {
		t.Helper()
		targets, err := targetRepo.ListByProject(context.Background(), project.ID)
		if err != nil || len(targets) != want {
			t.Fatalf("%s: expected %d outbound targets, got %d err=%v", label, want, len(targets), err)
		}
	}
	assertTargetCount("before any auth changes", 2)

	// Add a Slack authorized user via the handler.
	addSlackForm := url.Values{}
	addSlackForm.Set("project_id", project.ID)
	addSlackForm.Set("slack_user_id", "U0AQYLJR14Y")
	addSlackForm.Set("display_name", "Alice")
	addSlackReq := httptest.NewRequest(http.MethodPost, "/channels/slack/authorized-users", strings.NewReader(addSlackForm.Encode()))
	addSlackReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addSlackRec := httptest.NewRecorder()
	e.ServeHTTP(addSlackRec, addSlackReq)
	if addSlackRec.Code != http.StatusOK {
		t.Fatalf("add slack auth user: status=%d body=%s", addSlackRec.Code, addSlackRec.Body.String())
	}
	assertTargetCount("after adding Slack authorized user", 2)

	// Remove the Slack authorized user.
	slackUsers, err := h.slackAuthRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(slackUsers) != 1 {
		t.Fatalf("expected 1 slack auth user, got %d err=%v", len(slackUsers), err)
	}
	delSlackReq := httptest.NewRequest(http.MethodDelete, "/channels/slack/authorized-users/"+slackUsers[0].ID+"?project_id="+project.ID, nil)
	delSlackRec := httptest.NewRecorder()
	e.ServeHTTP(delSlackRec, delSlackReq)
	if delSlackRec.Code != http.StatusOK {
		t.Fatalf("remove slack auth user: status=%d body=%s", delSlackRec.Code, delSlackRec.Body.String())
	}
	assertTargetCount("after removing Slack authorized user", 2)

	// Add a Discord authorized user via the handler.
	addDiscordForm := url.Values{}
	addDiscordForm.Set("project_id", project.ID)
	addDiscordForm.Set("discord_user_id", "1518288288572641398")
	addDiscordForm.Set("display_name", "Bob")
	addDiscordReq := httptest.NewRequest(http.MethodPost, "/channels/discord/authorized-users", strings.NewReader(addDiscordForm.Encode()))
	addDiscordReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addDiscordRec := httptest.NewRecorder()
	e.ServeHTTP(addDiscordRec, addDiscordReq)
	if addDiscordRec.Code != http.StatusOK {
		t.Fatalf("add discord auth user: status=%d body=%s", addDiscordRec.Code, addDiscordRec.Body.String())
	}
	assertTargetCount("after adding Discord authorized user", 2)

	// Remove the Discord authorized user.
	discordUsers, err := h.discordAuthRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(discordUsers) != 1 {
		t.Fatalf("expected 1 discord auth user, got %d err=%v", len(discordUsers), err)
	}
	delDiscordReq := httptest.NewRequest(http.MethodDelete, "/channels/discord/authorized-users/"+discordUsers[0].ID+"?project_id="+project.ID, nil)
	delDiscordRec := httptest.NewRecorder()
	e.ServeHTTP(delDiscordRec, delDiscordReq)
	if delDiscordRec.Code != http.StatusOK {
		t.Fatalf("remove discord auth user: status=%d body=%s", delDiscordRec.Code, delDiscordRec.Body.String())
	}
	assertTargetCount("after removing Discord authorized user", 2)
}

// TestOutboundTargetMutationDoesNotAffectAuthorizedUsers verifies that saving or removing
// outbound targets does not modify the Slack or Discord authorized users tables.
func TestOutboundTargetMutationDoesNotAffectAuthorizedUsers(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Outbound Isolation Project")
	targetRepo := repository.NewChannelTargetRepo(db)
	h.SetChannelTargetRepo(targetRepo)

	// Seed one Slack and one Discord authorized user.
	if err := h.slackAuthRepo.Create(context.Background(), &models.SlackAuthorizedUser{
		ProjectID: project.ID, SlackUserID: "U0AQYLJR14Y", DisplayName: "Alice", AddedBy: "test",
	}); err != nil {
		t.Fatalf("seed slack auth user: %v", err)
	}
	if err := h.discordAuthRepo.Create(context.Background(), &models.DiscordAuthorizedUser{
		ProjectID: project.ID, DiscordUserID: "1518288288572641398", DisplayName: "Bob", AddedBy: "test",
	}); err != nil {
		t.Fatalf("seed discord auth user: %v", err)
	}

	assertAuthCounts := func(label string, wantSlack, wantDiscord int) {
		t.Helper()
		slack, err := h.slackAuthRepo.ListByProject(context.Background(), project.ID)
		if err != nil || len(slack) != wantSlack {
			t.Fatalf("%s: expected %d Slack auth users, got %d err=%v", label, wantSlack, len(slack), err)
		}
		discord, err := h.discordAuthRepo.ListByProject(context.Background(), project.ID)
		if err != nil || len(discord) != wantDiscord {
			t.Fatalf("%s: expected %d Discord auth users, got %d err=%v", label, wantDiscord, len(discord), err)
		}
	}
	assertAuthCounts("before any outbound target changes", 1, 1)

	// Save outbound targets including user DM targets with the same IDs as authorized users.
	saveForm := url.Values{}
	saveForm.Set("project_id", project.ID)
	saveForm.Set("enabled", "false")
	saveForm.Add("target_row_id", "")
	saveForm.Add("target_platform", "slack")
	saveForm.Add("target_kind", "user")
	saveForm.Add("target_name", "")
	saveForm.Add("target_target_id", "U0AQYLJR14Y")
	saveForm.Add("target_thread_id", "")
	saveForm.Add("target_is_home", "false")
	saveForm.Add("target_default_subject", "")
	saveForm.Add("target_row_id", "")
	saveForm.Add("target_platform", "discord")
	saveForm.Add("target_kind", "user")
	saveForm.Add("target_name", "")
	saveForm.Add("target_target_id", "1518288288572641398")
	saveForm.Add("target_thread_id", "")
	saveForm.Add("target_is_home", "false")
	saveForm.Add("target_default_subject", "")
	saveReq := httptest.NewRequest(http.MethodPost, "/channels/send-message-explicit-targets", strings.NewReader(saveForm.Encode()))
	saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveRec := httptest.NewRecorder()
	e.ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save outbound targets: status=%d body=%s", saveRec.Code, saveRec.Body.String())
	}
	assertAuthCounts("after saving user-DM outbound targets with same IDs as auth users", 1, 1)

	// Verify the outbound targets were actually saved.
	targets, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(targets) != 2 {
		t.Fatalf("expected 2 outbound targets after save, got %d err=%v", len(targets), err)
	}

	// Remove all outbound targets (save empty list).
	emptyForm := url.Values{}
	emptyForm.Set("project_id", project.ID)
	emptyForm.Set("enabled", "false")
	emptyReq := httptest.NewRequest(http.MethodPost, "/channels/send-message-explicit-targets", strings.NewReader(emptyForm.Encode()))
	emptyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	emptyRec := httptest.NewRecorder()
	e.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("remove all outbound targets: status=%d body=%s", emptyRec.Code, emptyRec.Body.String())
	}
	assertAuthCounts("after removing all outbound targets", 1, 1)

	// Confirm outbound targets are now empty.
	remaining, err := targetRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("expected 0 outbound targets after removal, got %d err=%v", len(remaining), err)
	}
}
