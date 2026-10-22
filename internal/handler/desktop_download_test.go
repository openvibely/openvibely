package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestDesktopDownloadAuthorizationAndOriginal(t *testing.T) {
	fixture := setupChatAttachmentOwnershipFixture(t)
	for _, tc := range []struct {
		name, project, origin        string
		configured, canceled, failed bool
		status                       int
	}{
		{"original", fixture.owningProject.ID, "http://example.com", true, false, false, 200},
		{"canceled", fixture.owningProject.ID, "http://example.com", true, true, false, 200},
		{"save failure", fixture.owningProject.ID, "http://example.com", true, false, true, 500},
		{"foreign project", fixture.foreignProject.ID, "http://example.com", true, false, false, 404},
		{"foreign origin", fixture.owningProject.ID, "http://evil.example", true, false, false, 403},
		{"missing origin", fixture.owningProject.ID, "", true, false, false, 403},
		{"web server", fixture.owningProject.ID, "http://example.com", false, false, false, 501},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			fixture.h.SetDesktopFileSaver(nil)
			if tc.configured {
				fixture.h.SetDesktopFileSaver(func(ctx context.Context, name string, r io.Reader) (bool, error) {
					called = true
					require.Equal(t, fixture.attachment.FileName, name)
					data, err := io.ReadAll(r)
					require.NoError(t, err)
					require.Equal(t, fixture.fileContent, data)
					if tc.failed {
						return false, errors.New("disk full")
					}
					return tc.canceled, nil
				})
			}
			req := httptest.NewRequest(http.MethodPost, "http://example.com/chat/attachments/"+fixture.attachment.ID+"/download?metadata=1&view=1&project_id="+tc.project, nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-OpenVibely-Save", "1")
			rec := httptest.NewRecorder()
			c := fixture.e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues(fixture.attachment.ID)
			err := fixture.h.saveDesktopFile(fixture.h.DownloadChatAttachment)(c)
			if err != nil {
				var he *echo.HTTPError
				require.ErrorAs(t, err, &he)
				require.Equal(t, tc.status, he.Code)
			} else {
				require.Equal(t, tc.status, rec.Code)
				require.Contains(t, rec.Body.String(), "\"canceled\"")
			}
			require.Equal(t, tc.status == 200 || tc.failed, called)
		})
	}
}

func TestDesktopSaveNativeOrigins(t *testing.T) {
	for _, tc := range []struct {
		host, origin, native string
		allowed              bool
	}{
		{"localhost", "null", "1", true},
		{"localhost", "", "1", true},
		{"localhost", "wails://localhost", "1", true},
		{"wails.localhost", "http://wails.localhost", "1", true},
		{"localhost", "https://evil.example", "1", false},
		{"localhost", "null", "", false},
	} {
		t.Run(tc.host+tc.origin+tc.native, func(t *testing.T) {
			h := &Handler{desktopFileSaver: func(context.Context, string, io.Reader) (bool, error) { return false, nil }}
			req := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/files/id", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-OpenVibely-Native-Window", tc.native)
			req.Header.Set("X-OpenVibely-Save", "1")
			c := echo.New().NewContext(req, httptest.NewRecorder())
			called := false
			err := h.saveDesktopFile(func(echo.Context) error { called = true; return nil })(c)
			require.Equal(t, tc.allowed, called)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
