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
)

func TestEmailAuthorizedSendersHandlers(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetEmailAuthRepo(repository.NewEmailAuthRepo(db))
	project := createProject(t, h, "Email Auth UI")
	otherProject := createProject(t, h, "Other Email Auth UI")

	rec := htmxGet(e, "/channels/email/authorized-senders?project_id="+project.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No authorized senders configured. Access is denied until senders are added.") {
		t.Fatal("expected deny empty state")
	}

	form := url.Values{"project_id": {project.ID}, "email_address": {"bot@example.com"}, "authorized_email_address": {"Alice@Example.COM"}, "display_name": {"Alice"}}
	rec = postForm(e, "/channels/email/authorized-senders", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected add 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alice@example.com") {
		t.Fatal("expected normalized sender in response")
	}
	otherSender := &models.EmailAuthorizedSender{ProjectID: otherProject.ID, EmailAddress: "bob@example.com", DisplayName: "Other Email Sender", AddedBy: "test"}
	if err := h.emailAuthRepo.Create(context.Background(), otherSender); err != nil {
		t.Fatalf("seed other email sender: %v", err)
	}

	senders, err := h.emailAuthRepo.ListByProject(httptest.NewRequest(http.MethodGet, "/", nil).Context(), project.ID)
	if err != nil {
		t.Fatalf("list senders: %v", err)
	}
	var sender *models.EmailAuthorizedSender
	for i := range senders {
		if senders[i].EmailAddress == "alice@example.com" {
			sender = &senders[i]
			break
		}
	}
	if sender == nil {
		t.Fatalf("expected normalized authorized sender in %#v", senders)
	}
	req := httptest.NewRequest(http.MethodDelete, "/channels/email/authorized-senders/"+sender.ID, nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected remove 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "alice@example.com") {
		t.Fatalf("expected removed sender to disappear from response, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Other Email Sender") {
		t.Fatalf("expected other system-level sender to remain visible, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="project_id" value="`+project.ID+`"`) {
		t.Fatalf("expected omitted project_id delete to reload with record project %q, got %q", project.ID, rec.Body.String())
	}
	deleted, err := h.emailAuthRepo.GetByID(context.Background(), sender.ID)
	if err != nil {
		t.Fatalf("get deleted email sender: %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected deleted email sender removed, got %#v", deleted)
	}
	remaining, err := h.emailAuthRepo.GetByID(context.Background(), otherSender.ID)
	if err != nil {
		t.Fatalf("get other email sender: %v", err)
	}
	if remaining == nil {
		t.Fatal("expected other project sender to remain")
	}
}

func TestEmailConfigureDoesNotSaveTypedAuthorizedSenderWithoutAdd(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	project := createProject(t, h, "Email Configure Sender")

	form := url.Values{
		"project_id":                        {project.ID},
		"email_provider":                    {"gmail"},
		"email_address":                     {"bot@example.com"},
		"email_password":                    {"abcd efgh ijkl mnop"},
		"authorized_email_address":          {"Alice@Example.COM"},
		"display_name":                      {"Alice"},
		"email_send_responses":              {"true"},
		"email_mark_existing_seen_on_start": {"true"},
	}
	rec := postForm(e, "/channels/email/configure", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected configure redirect, got %d %s", rec.Code, rec.Body.String())
	}

	senders, err := h.emailAuthRepo.ListByProject(httptest.NewRequest(http.MethodGet, "/", nil).Context(), project.ID)
	if err != nil {
		t.Fatalf("list authorized senders: %v", err)
	}
	if len(senders) != 0 {
		t.Fatalf("expected Save Email Settings not to add typed authorized sender, got %d", len(senders))
	}

	addForm := url.Values{"project_id": {project.ID}, "authorized_email_address": {"Alice@Example.COM"}, "display_name": {"Alice"}}
	rec = postForm(e, "/channels/email/authorized-senders", addForm)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected explicit add 200, got %d %s", rec.Code, rec.Body.String())
	}

	rec = htmxGet(e, "/channels?project_id="+project.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected channels render 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice@example.com") || !strings.Contains(body, "1 sender(s)") {
		t.Fatalf("expected explicitly added sender to appear on reopen, body=%s", body)
	}
}

func TestEmailAuthorizedSendersValidation(t *testing.T) {
	_, e, _, _ := setupTestHandlerWithDB(t)
	rec := postForm(e, "/channels/email/authorized-senders", url.Values{"email_address": {"a@example.com"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing project 400, got %d", rec.Code)
	}
	rec = postForm(e, "/channels/email/authorized-senders", url.Values{"project_id": {"default"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing address 400, got %d", rec.Code)
	}
	rec = postForm(e, "/channels/email/authorized-senders", url.Values{"project_id": {"default"}, "email_address": {"a@example.com"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected ambiguous email_address-only sender request 400, got %d", rec.Code)
	}
}

func TestEmailConfigurePresetsRemove(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	form := url.Values{"email_provider": {"gmail"}, "email_address": {"bot@example.com"}, "email_password": {"abcd efgh ijkl mnop"}, "email_send_responses": {"true"}, "email_mark_existing_seen_on_start": {"true"}}
	req := httptest.NewRequest(http.MethodPost, "/channels/email/configure", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected configure 200, got %d %s", rec.Code, rec.Body.String())
	}
	imapHost, _ := h.settingsRepo.Get(req.Context(), "email_imap_host")
	smtpHost, _ := h.settingsRepo.Get(req.Context(), "email_smtp_host")
	if imapHost != "imap.gmail.com" || smtpHost != "smtp.gmail.com" {
		t.Fatalf("expected gmail hosts, got %q %q", imapHost, smtpHost)
	}
	password, _ := h.settingsRepo.Get(req.Context(), "email_password")
	if password != "abcdefghijklmnop" {
		t.Fatalf("expected provider app password whitespace to be removed, got %q", password)
	}

	custom := url.Values{"email_provider": {"custom"}, "email_address": {"bot@example.com"}}
	req = httptest.NewRequest(http.MethodPost, "/channels/email/configure", strings.NewReader(custom.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected custom missing host 400, got %d", rec.Code)
	}

	removeReq := httptest.NewRequest(http.MethodPost, "/channels/email/remove", nil)
	removeReq.Header.Set("HX-Request", "true")
	removeRec := httptest.NewRecorder()
	e.ServeHTTP(removeRec, removeReq)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("expected remove 200, got %d", removeRec.Code)
	}
	password, _ = h.settingsRepo.Get(removeReq.Context(), "email_password")
	if password != "" {
		t.Fatal("expected email password cleared")
	}
}

func TestChannelsPageEmailUI(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	_ = h.settingsRepo.Set(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "email_address", "bot@example.com")
	_ = h.settingsRepo.Set(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "email_password", "super-secret-app-password")
	_ = h.emailAuthRepo.Create(httptest.NewRequest(http.MethodGet, "/", nil).Context(), &models.EmailAuthorizedSender{ProjectID: "default", EmailAddress: "alice@example.com", AddedBy: "test"})

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected channels 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"data-channel-type=\"email\"", "Configure Email", "Gmail", "Outlook / Microsoft 365", "Use a Google app password", "Authorized Senders", "1 sender(s)", "person@example.com", "name=\"authorized_email_address\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected email UI to contain %q", want)
		}
	}
	for _, name := range []string{"email_send_responses", "email_skip_attachments", "email_mark_existing_seen_on_start"} {
		if !strings.Contains(body, `class="toggle toggle-primary" name="`+name+`"`) {
			t.Fatalf("expected %s to use toggle styling", name)
		}
		if strings.Contains(body, `class="checkbox checkbox-primary" name="`+name+`"`) {
			t.Fatalf("expected %s not to use checkbox styling", name)
		}
	}
	if !strings.Contains(body, "super-secret-app-password") {
		t.Fatal("email UI should render the saved app password in the masked secret input")
	}
	if strings.Contains(body, "pairing") || strings.Contains(body, "PIN") {
		t.Fatal("email UI should not include pairing/pin language")
	}
}
