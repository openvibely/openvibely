package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/service"
)

func TestUsageResetRoutesRejectMissingSelection(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(nil, repo)
	for _, body := range []string{`{}`, `{"account_key":"account","idempotency_key":"attempt"}`, `{"account_key":"account","credit_id":"credit"}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/analytics/usage/resets/consume", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}
