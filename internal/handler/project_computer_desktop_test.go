package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

func TestComputerDesktopTicketsAreScopedOneUseAndExpire(t *testing.T) {
	h := &Handler{desktopMode: true}
	h.SetComputerDesktopBackendURL("http://127.0.0.1:1234")
	e := echo.New()
	e.Use(h.AuthMiddleware())
	e.GET("/computer-stream", func(c echo.Context) error {
		ticket, ok := c.Get(computerTicketContextKey).(computerStreamTicket)
		if !ok || ticket.ProjectID != "project" || ticket.Mode != "view" || ticket.TaskID != "task" || ticket.Principal.UserID != "bob" {
			t.Error("ticket scope lost")
		}
		return c.NoContent(204)
	})
	token, err := h.issueComputerTicket(models.WithPrincipal(t.Context(), models.Principal{UserID: "bob"}), "project", "view", "task")
	if err != nil {
		t.Fatal(err)
	}
	request := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/computer-stream", nil)
		req.Header.Set("Origin", "wails://wails.localhost")
		req.Header.Set("Sec-WebSocket-Protocol", "binary, "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := request(""); code != 403 {
		t.Fatal(code)
	}
	if code := request(token); code != 204 {
		t.Fatal(code)
	}
	if code := request(token); code != 403 {
		t.Fatalf("ticket replay: %d", code)
	}
	h.computerTickets["expired"] = computerStreamTicket{ProjectID: "project", Mode: "view", Expires: time.Now().Add(-time.Second)}
	if code := request("computer-ticket.expired"); code != 403 {
		t.Fatal(code)
	}
	token, err = h.issueComputerTicket(models.WithPrincipal(t.Context(), models.Principal{UserID: "bob"}), "project", "view", "task")
	if err != nil {
		t.Fatal(err)
	}
	h.SetDesktopMode(false)
	if code := request(token); code != 403 {
		t.Fatalf("hosted/web accepted native ticket: %d", code)
	}
}
