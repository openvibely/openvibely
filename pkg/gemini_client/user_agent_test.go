package geminiclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestOAuthRequestUserAgent(t *testing.T) {
	for _, tc := range []struct {
		name, credential, method string
		legacy                   bool
	}{
		{"personal", encodeRefreshCredential(OAuthClientID, "refresh"), "consumer", false},
		{"business", encodeRefreshCredential(BusinessOAuthClientID, "refresh"), "gcp", false},
		{"legacy", "refresh", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got := r.Header.Get("User-Agent")
				if tc.legacy {
					if got != "Go-http-client/1.1" {
						t.Errorf("legacy header changed: %q", got)
					}
				} else {
					want := "\x61\x6e\x74\x69\x67\x72\x61\x76\x69\x74\x79/cli/1.5.14 (aidev_client; os_type=" + runtime.GOOS + "; arch=" + runtime.GOARCH + "; cl=1026970498; auth_method=" + tc.method + ")"
					if got != want {
						t.Errorf("User-Agent = %q, want %q", got, want)
					}
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			c := NewWithOAuth("access", "project")
			c.SetOAuthCredential(tc.credential)
			if c.legacyOAuth != tc.legacy {
				t.Fatal("wrong credential classification")
			}
			resp, err := c.post(context.Background(), server.URL, []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		})
	}
}

func TestOAuthCredentialMetadataCleared(t *testing.T) {
	c := NewWithOAuth("access", "project")
	c.SetOAuthCredential(encodeRefreshCredential(OAuthClientID, "refresh"))
	c.SetOAuthCredential("old-refresh")
	if c.oauthAuthMethod != "" || !c.legacyOAuth {
		t.Fatal("stale authentication metadata")
	}
}

func TestOAuthEndpointSelection(t *testing.T) {
	for _, tc := range []struct{ name, credential, want string }{
		{"personal", encodeRefreshCredential(OAuthClientID, "refresh"), "https://daily-cloudcode-pa.googleapis.com/v1internal"},
		{"business", encodeRefreshCredential(BusinessOAuthClientID, "refresh"), CodeAssistBaseURL},
		{"legacy", "refresh", CodeAssistBaseURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewWithOAuth("access", "project")
			c.SetOAuthCredential(tc.credential)
			if c.codeAssistURL != tc.want || OAuthBaseURL(tc.credential) != tc.want {
				t.Fatalf("wrong endpoint: %s", c.codeAssistURL)
			}
			c.SetCodeAssistURL("https://custom.example/v1internal")
			c.SetOAuthCredential(tc.credential)
			if c.codeAssistURL != "https://custom.example/v1internal" {
				t.Fatal("override lost")
			}
		})
	}
	c := NewWithOAuth("access", "project")
	c.SetOAuthCredential(encodeRefreshCredential(OAuthClientID, "refresh"))
	c.SetOAuthCredential("legacy")
	if c.codeAssistURL != CodeAssistBaseURL {
		t.Fatal("stale personal endpoint")
	}
}

func TestGoogleAccountSetupCredentialRouting(t *testing.T) {
	oldURL, oldClient := CodeAssistBaseURL, oauthHTTPClient
	defer func() { CodeAssistBaseURL, oauthHTTPClient = oldURL, oldClient }()
	for _, id := range []string{OAuthClientID, BusinessOAuthClientID} {
		credential := encodeRefreshCredential(id, "refresh")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("User-Agent") != OAuthUserAgent(credential) {
				t.Error("missing account authentication metadata")
			}
			if r.URL.Path != "/v1internal:loadCodeAssist" {
				t.Error(r.URL.Path)
			}
			io.WriteString(w, `{"currentTier":{"id":"free-tier"},"cloudaicompanionProject":"project"}`)
		}))
		CodeAssistBaseURL = server.URL + "/v1internal"
		project, err := SetupGoogleAccountWithCredential(context.Background(), "access", "", credential)
		server.Close()
		if err != nil || project != "project" {
			t.Fatalf("project=%s err=%v", project, err)
		}
	}
}

func TestOAuthModelEffortMapping(t *testing.T) {
	c := NewWithOAuth("access", "project")
	for _, tc := range []struct{ model, effort, want string }{
		{"gemini-4-argon", "medium", "gemini-4-argon-medium"},
		{"gemini-4-neon", "xhigh", "gemini-4-neon-xhigh"},
		{"gemini-4-argon", "", "gemini-4-argon-medium"},
		{"gemini-4-argon-high", "", "gemini-4-argon-high"},
		{"gemini-3.8-flash", "", "gemini-3.8-flash"},
	} {
		if got := c.oauthModel(tc.model, tc.effort); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.model, got, tc.want)
		}
	}
	c.SetLegacyOAuth(true)
	if c.oauthModel("gemini-4-argon", "high") != "gemini-4-argon" {
		t.Fatal("legacy model changed")
	}
	if NewWithAPIKey("key").searchModel("gemini-4-argon", "high") != "gemini-4-argon" {
		t.Fatal("API-key model changed")
	}
}
