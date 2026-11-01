//go:build linux

package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLinuxKeyboardTranslation(t *testing.T) {
	for _, tc := range []struct {
		key  string
		mods any
		want string
	}{
		{"l", []any{"cmd"}, "ctrl+l"}, {"up", []any{"shift"}, "shift+Up"}, {"ctrl+return", nil, "ctrl+Return"}, {"pageup", nil, "Prior"},
	} {
		got, err := linuxKey(tc.key, tc.mods)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q %v", tc.key, got, err)
		}
	}
	if _, err := linuxKey("l", []any{"bad"}); err == nil {
		t.Fatal("accepted invalid modifier")
	}
}
func TestBridgeRejectsBrowserOriginsAndInvalidActions(t *testing.T) {
	req := httptest.NewRequest("GET", "/screen", nil)
	req.Header.Set("Origin", "https://example.com")
	res := httptest.NewRecorder()
	server().ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatal(res.Code)
	}
	req = httptest.NewRequest("POST", "http://127.0.0.1/action", strings.NewReader(`{"Action":"bash","Values":{}}`))
	req.RemoteAddr = "127.0.0.1:1234"
	res = httptest.NewRecorder()
	server().ServeHTTP(res, req)
	if res.Code != 400 {
		t.Fatal(res.Code)
	}
	if _, err := action(context.Background(), "click", map[string]any{"x": float64(-1), "y": float64(20)}); err == nil {
		t.Fatal("accepted offscreen input")
	}
}

func TestScrollGesturesAreBounded(t *testing.T) {
	for _, tc := range []struct {
		v    map[string]any
		want string
	}{
		{map[string]any{}, "3"}, {map[string]any{"amount": float64(500)}, "20"}, {map[string]any{"amount": float64(1)}, "1"},
	} {
		got, err := scrollAmount(tc.v)
		if err != nil || got != tc.want {
			t.Fatalf("%s %v", got, err)
		}
	}
	if _, err := scrollAmount(map[string]any{"amount": float64(-1)}); err == nil {
		t.Fatal("negative scroll accepted")
	}
}

func TestBridgeAuthenticatesPrivateNetworkRequests(t *testing.T) {
	t.Setenv("OPENVIBELY_COMPUTER_TOKEN", "test-token")
	for _, tc := range []struct {
		token, origin string
		want          int
	}{{"", "", 403}, {"Bearer wrong", "", 403}, {"Bearer test-token", "https://evil.example", 403}, {"Bearer test-token", "", 400}} {
		req := httptest.NewRequest("POST", "http://computer:8090/action", strings.NewReader(`{"action":"invalid"}`))
		req.RemoteAddr = "172.20.0.4:30000"
		req.Header.Set("Authorization", tc.token)
		req.Header.Set("Origin", tc.origin)
		res := httptest.NewRecorder()
		server().ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Fatalf("status %d want %d", res.Code, tc.want)
		}
	}
}
