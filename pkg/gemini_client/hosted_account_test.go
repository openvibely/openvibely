package geminiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostedAccountTypeSurvivesExchangeAndRefresh(t *testing.T) {
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "hosted-client")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "hosted-secret")
	for _, accountType := range []string{"personal", "business"} {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rotate=%t", accountType, rotate), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.ParseForm()
					if r.Form.Get("client_id") != "hosted-client" || r.Form.Get("client_secret") != "hosted-secret" {
						t.Error("wrong client")
					}
					if r.Form.Get("grant_type") == "refresh_token" {
						if r.Form.Get("refresh_token") != "original" {
							t.Error("encoded token sent upstream")
						}
						if rotate {
							fmt.Fprint(w, `{"access_token":"fresh","refresh_token":"rotated","expires_in":3600}`)
						} else {
							fmt.Fprint(w, `{"access_token":"fresh","expires_in":3600}`)
						}
					} else {
						fmt.Fprint(w, `{"access_token":"access","refresh_token":"original","expires_in":3600}`)
					}
				}))
				defer server.Close()
				old := OAuthTokenURL
				OAuthTokenURL = server.URL
				defer func() { OAuthTokenURL = old }()
				tokens, err := ExchangeCode(context.Background(), OAuthClient{ID: "hosted-client", Secret: "hosted-secret", AccountType: accountType}, "code", "https://example.com/oauth2callback", "verifier")
				if err != nil {
					t.Fatal(err)
				}
				for step := 0; step < 2; step++ {
					wantMethod, wantHost := "consumer", "https://daily-cloudcode-pa.googleapis.com/v1internal"
					if accountType == "business" {
						wantMethod, wantHost = "gcp", "https://cloudcode-pa.googleapis.com/v1internal"
					}
					if OAuthAuthMethod(tokens.RefreshToken) != wantMethod || OAuthBaseURL(tokens.RefreshToken) != wantHost || !strings.Contains(OAuthUserAgent(tokens.RefreshToken), "auth_method="+wantMethod) {
						t.Fatal("account routing lost")
					}
					c := NewWithOAuth(tokens.AccessToken, "project")
					c.SetOAuthCredential(tokens.RefreshToken)
					if c.codeAssistURL != wantHost {
						t.Fatal("generation uses wrong endpoint")
					}
					if step == 0 {
						tokens, err = RefreshTokenContext(context.Background(), tokens.RefreshToken)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}
