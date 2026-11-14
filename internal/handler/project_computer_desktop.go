package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// Native WebViews use Wails' asset origin; WebSockets must connect directly to
// the loopback backend. A one-use ticket carries the already authenticated
// project/mode scope across origins without sharing a session cookie.
type computerStreamTicket struct {
	ProjectID, Mode, TaskID string
	ViewerID                string
	Principal               models.Principal
	Expires                 time.Time
}

const computerTicketContextKey = "project_computer_stream_ticket"

func (h *Handler) SetComputerDesktopBackendURL(base string) {
	if h.desktopMode {
		h.computerDesktopURL = base
	}
}
func (h *Handler) issueComputerTicket(ctx context.Context, projectID, mode string, taskIDs ...string) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	now := time.Now()
	h.computerTicketsMu.Lock()
	defer h.computerTicketsMu.Unlock()
	if h.computerTickets == nil {
		h.computerTickets = make(map[string]computerStreamTicket)
	}
	for key, ticket := range h.computerTickets {
		if !ticket.Expires.After(now) {
			delete(h.computerTickets, key)
		}
	}
	if len(h.computerTickets) >= 1024 {
		return "", errors.New("too many computer viewer connections")
	}
	token := hex.EncodeToString(secret)
	taskID := ""
	if len(taskIDs) > 0 {
		taskID = taskIDs[0]
	}
	viewerID := ""
	if len(taskIDs) > 1 {
		viewerID = taskIDs[1]
	}
	principal, _ := models.PrincipalFromContext(ctx)
	h.computerTickets[token] = computerStreamTicket{ViewerID: viewerID, Principal: principal, TaskID: taskID, ProjectID: projectID, Mode: mode, Expires: now.Add(30 * time.Second)}
	return "computer-ticket." + token, nil
}
func (h *Handler) consumeComputerTicket(c echo.Context) bool {
	if !h.desktopMode || h.computerDesktopURL == "" || c.Request().Method != http.MethodGet {
		return false
	}
	var token string
	for _, protocol := range strings.Split(c.Request().Header.Get("Sec-WebSocket-Protocol"), ",") {
		protocol = strings.TrimSpace(protocol)
		if strings.HasPrefix(protocol, "computer-ticket.") {
			token = strings.TrimPrefix(protocol, "computer-ticket.")
			break
		}
	}
	h.computerTicketsMu.Lock()
	ticket, ok := h.computerTickets[token]
	delete(h.computerTickets, token)
	h.computerTicketsMu.Unlock()
	if !ok || !ticket.Expires.After(time.Now()) {
		return false
	}
	c.Set(computerTicketContextKey, ticket)
	return true
}
func (h *Handler) ProjectComputerNativeStream(c echo.Context) error {
	ticket, ok := c.Get(computerTicketContextKey).(computerStreamTicket)
	if !ok {
		return echo.NewHTTPError(403, "Computer viewer ticket required")
	}
	c.SetRequest(c.Request().WithContext(models.WithPrincipal(c.Request().Context(), ticket.Principal)))
	// Echo caches parsed query parameters. Update that same map so middleware
	// that already parsed the query cannot hide the ticket's task/viewer scope.
	// Ticket values override any caller-supplied scope.
	q := c.QueryParams()
	q.Set("task_id", ticket.TaskID)
	q.Set("viewer_id", ticket.ViewerID)
	c.Request().URL.RawQuery = q.Encode()
	c.SetParamNames("id", "mode")
	c.SetParamValues(ticket.ProjectID, ticket.Mode)
	return h.ProjectComputerStream(c)
}
