package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestRenderNativeWindowMarker(t *testing.T) {
	for _, tc := range []struct {
		name         string
		desktop      bool
		header, want string
	}{
		{"browser on web server", false, "", "false"},
		{"browser on desktop server", true, "", "false"},
		{"native window", true, "1", "true"},
		{"header on web server", false, "1", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("X-OpenVibely-Native-Window", tc.header)
			// Avoid unrelated preference lookups while exercising the shared renderer.
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)
			c.Set("handler", &Handler{desktopMode: tc.desktop, buildIdentity: buildinfo.Build{Version: "0.8.0"}})
			if err := render(c, http.StatusOK, layout.Base("Test", nil, "")); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rec.Body.String(), `data-build-version="0.8.0"`) {
				t.Fatal("missing server build version")
			}
			if !strings.Contains(rec.Body.String(), `data-openvibely-native-window="`+tc.want+`"`) {
				t.Fatalf("missing native window marker %s", tc.want)
			}
		})
	}
}
