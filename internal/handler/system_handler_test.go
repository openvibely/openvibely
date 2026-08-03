package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/internal/update"
)

func TestSystemHealthLoopbackSchema(t *testing.T) {
	e := echo.New()
	h := &Handler{}
	h.SetSystemHealth(buildinfo.Build{Version: "0.6.0", Commit: "abc", BuildTime: time.Unix(1, 0).UTC().Format(time.RFC3339), Artifact: buildinfo.ArtifactContainer}, buildinfo.ModeHosted, buildinfo.DistributionHosted, "token", "", 139, update.NewDrainManager(nil, nil, 0, time.Now))
	req := httptest.NewRequest(http.MethodGet, "/api/system/health", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	if err := h.SystemHealth(e.NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var body SystemHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ready || body.Version != "0.6.0" || body.DatabaseSchema != 139 || body.Distribution != buildinfo.DistributionHosted {
		t.Fatalf("body = %#v", body)
	}
}

func TestSystemHealthRejectsNonLoopbackAndAcceptsMatchingAgentToken(t *testing.T) {
	e := echo.New()
	h := &Handler{}
	h.SetSystemHealth(buildinfo.Build{Artifact: buildinfo.ArtifactContainer}, buildinfo.ModeHosted, buildinfo.DistributionHosted, "secret", "", 1, update.NewDrainManager(nil, nil, 0, time.Now))
	for _, tc := range []struct {
		auth string
		want int
	}{{"", http.StatusUnauthorized}, {"Bearer wrong", http.StatusUnauthorized}, {"Bearer secret", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/api/system/health", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set("Authorization", tc.auth)
		rec := httptest.NewRecorder()
		if err := h.SystemHealth(e.NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.want {
			t.Fatalf("auth %q status=%d want=%d", tc.auth, rec.Code, tc.want)
		}
	}
}
