package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestComputerNativeStatusTicketRespectsViewerMode(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	project := createProject(t, h, "Native preview")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer backend.Close()
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{service.ComputerConnection{Endpoint: backend.URL}})
	repo := h.projectRepo.Computers()
	require.NoError(t, repo.Configure(t.Context(), project.ID, "local"))
	if err := repo.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	h.SetDesktopMode(true)
	h.SetComputerDesktopBackendURL("http://127.0.0.1:1234")
	e := echo.New()
	e.GET("/projects/:id/computer/status", h.ProjectComputerStatus)
	for _, tc := range []struct {
		name, requested, want string
	}{
		{"preview", "view", "view"},
		{"legacy unspecified preview", "", "view"},
		{"controller", "control", "control"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/projects/"+project.ID+"/computer/status?viewer_mode="+tc.requested, nil)
			req.Header.Set("X-OpenVibely-Native-Window", "1")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			var response struct {
				Ticket string `json:"ticket"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.NotEmpty(t, response.Ticket)
			ticket := h.computerTickets[strings.TrimPrefix(response.Ticket, "computer-ticket.")]
			require.Equal(t, tc.want, ticket.Mode)
			// The WebSocket uses the same builder and must preserve native mode
			// and viewer identity without fetching the HTTP status endpoint.
			pushed, err := h.computerStatus(t.Context(), h.computerSvc, project.ID, "", "native-viewer", tc.requested, true)
			require.NoError(t, err)
			pushedTicket := h.computerTickets[strings.TrimPrefix(pushed["ticket"].(string), "computer-ticket.")]
			require.Equal(t, tc.want, pushedTicket.Mode)
			require.Equal(t, "native-viewer", pushedTicket.ViewerID)
			_, detach, err := h.computerSvc.RegisterViewer(t.Context(), project.ID, "owner", "native-window", ticket.Mode == "control")
			require.NoError(t, err)
			defer detach()
		})
	}
	// Disabled computers must reject new control connections.
	require.NoError(t, repo.SetEnabled(t.Context(), project.ID, false))
	_, _, err := h.computerSvc.RegisterViewer(t.Context(), project.ID, "owner", "disabled-window", true)
	require.Error(t, err, "disabled computer accepted a control connection")
}
