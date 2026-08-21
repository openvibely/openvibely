package anthropicclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRefreshTokenFailureRedactsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","refresh_token":"secret-refresh","access_token":"secret-access"}`))
	}))
	defer srv.Close()

	oldURL := OAuthTokenURL
	OAuthTokenURL = srv.URL
	defer func() { OAuthTokenURL = oldURL }()

	_, err := RefreshToken("secret-refresh")
	if err == nil {
		t.Fatal("expected refresh failure")
	}
	message := err.Error()
	for _, forbidden := range []string{"secret-refresh", "secret-access", "invalid_grant", "refresh_token", "access_token"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("refresh failure leaked %q in error: %s", forbidden, message)
		}
	}
	if !strings.Contains(message, "HTTP 401") {
		t.Fatalf("refresh failure should include status code, got %s", message)
	}
}

func TestTokenStorageAndRefreshSuccessPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := TokenFilePath()
	if err != nil {
		t.Fatalf("TokenFilePath: %v", err)
	}
	if !strings.HasSuffix(path, tokenFileName) {
		t.Fatalf("TokenFilePath = %q, want suffix %s", path, tokenFileName)
	}
	auth := &StoredAuth{Token: "access", RefreshToken: "refresh", ExpiresAt: 12345}
	if err := SaveAuth(auth); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	loaded, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if loaded.Token != auth.Token || loaded.RefreshToken != auth.RefreshToken || loaded.ExpiresAt != auth.ExpiresAt {
		t.Fatalf("loaded auth = %#v", loaded)
	}

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("anthropic-version") != AnthropicAPIVersion {
			t.Fatalf("headers content-type=%q anthropic-version=%q", r.Header.Get("Content-Type"), r.Header.Get("anthropic-version"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":60}`))
	}))
	defer srv.Close()
	oldURL := OAuthTokenURL
	OAuthTokenURL = srv.URL
	defer func() { OAuthTokenURL = oldURL }()
	refreshed, err := RefreshToken("refresh-secret")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if refreshed.Token != "new-access" || refreshed.RefreshToken != "new-refresh" || refreshed.ExpiresAt <= 0 {
		t.Fatalf("refreshed auth = %#v", refreshed)
	}
	if !strings.Contains(gotBody, "refresh-secret") || !strings.Contains(gotBody, oauthClientID) {
		t.Fatalf("refresh request body = %s", gotBody)
	}
}

func TestLoadAuthAndRefreshTokenErrorPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadAuth(); err == nil || !strings.Contains(err.Error(), "run --login first") {
		t.Fatalf("missing token error = %v", err)
	}
	path, err := TokenFilePath()
	if err != nil {
		t.Fatalf("TokenFilePath: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write invalid auth: %v", err)
	}
	if _, err := LoadAuth(); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("invalid token error = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()
	oldURL := OAuthTokenURL
	OAuthTokenURL = srv.URL
	defer func() { OAuthTokenURL = oldURL }()
	if _, err := RefreshToken("refresh"); err == nil || !strings.Contains(err.Error(), "decode refresh response") {
		t.Fatalf("invalid refresh response error = %v", err)
	}
}
