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
