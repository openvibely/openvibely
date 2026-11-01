package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// TestParseUsageFilter_MonthRangeUsesLocalTime verifies that when range=month is requested,
// parseUsageFilter sets DateFrom to the start of the current LOCAL month (midnight local),
// not the start of the UTC month. This mirrors the Schedules page which uses time.Local
// and time.Now() (not time.Now().UTC()) for all calendar-day arithmetic.
func TestParseUsageFilter_MonthRangeUsesLocalTime(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/usage?range=month&group_by=day", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	before := time.Now()
	filter := parseUsageFilter(c)
	after := time.Now()

	if filter.DateFrom.IsZero() {
		t.Fatal("expected non-zero DateFrom for range=month")
	}
	if filter.DateTo.IsZero() {
		t.Fatal("expected non-zero DateTo for range=month")
	}

	// DateTo should be ≈ now (within the before/after window).
	if filter.DateTo.Before(before) || filter.DateTo.After(after) {
		t.Errorf("DateTo=%v should be between %v and %v", filter.DateTo, before, after)
	}

	// DateFrom must be the first of the current LOCAL month at local midnight.
	// This differs from time.UTC for any non-UTC timezone.
	localNow := time.Now().In(time.Local)
	expectedLocalStart := time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, time.Local)

	// Compare as UTC instants so the assertion is timezone-agnostic.
	if !filter.DateFrom.UTC().Equal(expectedLocalStart.UTC()) {
		t.Errorf("DateFrom=%v (UTC: %v) should be the local month start %v (UTC: %v)",
			filter.DateFrom, filter.DateFrom.UTC(), expectedLocalStart, expectedLocalStart.UTC())
	}
}

// TestParseUsageFilter_SevenDayRange verifies that range=7d sets DateFrom to ~7 days ago
// and DateTo to now.
func TestParseUsageFilter_SevenDayRange(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/usage?range=7d&group_by=day", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	before := time.Now()
	filter := parseUsageFilter(c)
	after := time.Now()

	if filter.DateFrom.IsZero() || filter.DateTo.IsZero() {
		t.Fatal("expected non-zero DateFrom and DateTo for range=7d")
	}

	expectedFrom := before.AddDate(0, 0, -7)
	expectedTo := after

	// DateFrom should be ≈ 7 days ago (within a 1-second window).
	if filter.DateFrom.Before(expectedFrom.Add(-time.Second)) || filter.DateFrom.After(expectedTo.AddDate(0, 0, -7).Add(time.Second)) {
		t.Errorf("DateFrom=%v not within expected 7-day-ago window [%v, %v]",
			filter.DateFrom, expectedFrom.Add(-time.Second), expectedTo.AddDate(0, 0, -7).Add(time.Second))
	}

	// DateTo should be within the before/after window.
	if filter.DateTo.Before(before) || filter.DateTo.After(after) {
		t.Errorf("DateTo=%v should be between %v and %v", filter.DateTo, before, after)
	}
}

// TestParseUsageFilter_ThirtyDayDefault verifies that the default range (or "30d") sets
// a 30-calendar-day window in local time.
func TestParseUsageFilter_ThirtyDayDefault(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/usage?group_by=day", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	before := time.Now()
	filter := parseUsageFilter(c)
	after := time.Now()

	if filter.DateFrom.IsZero() || filter.DateTo.IsZero() {
		t.Fatal("expected non-zero date bounds for default range")
	}

	// Calendar days can span 23 or 25 hours across daylight-saving changes.
	expectedFrom := filter.DateTo.In(time.Local).AddDate(0, 0, -30)
	if !filter.DateFrom.Equal(expectedFrom) {
		t.Errorf("DateFrom=%v, want 30 local calendar days before DateTo: %v", filter.DateFrom, expectedFrom)
	}

	// DateTo should be within the before/after window.
	if filter.DateTo.Before(before) || filter.DateTo.After(after) {
		t.Errorf("DateTo=%v should be between %v and %v", filter.DateTo, before, after)
	}
}

// TestParseUsageFilter_AllRange verifies that range=all sets no date bounds.
func TestParseUsageFilter_AllRange(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/usage?range=all", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	filter := parseUsageFilter(c)

	if !filter.DateFrom.IsZero() {
		t.Errorf("expected zero DateFrom for range=all, got %v", filter.DateFrom)
	}
	if !filter.DateTo.IsZero() {
		t.Errorf("expected zero DateTo for range=all, got %v", filter.DateTo)
	}
}

// TestParseUsageFilter_GroupByDefault verifies that GroupBy defaults to "day" when not specified.
func TestParseUsageFilter_GroupByDefault(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/usage?range=all", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	filter := parseUsageFilter(c)
	if filter.GroupBy != "day" {
		t.Errorf("expected default GroupBy=day, got %q", filter.GroupBy)
	}
}
