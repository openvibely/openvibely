package customauth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrepareRequestAppliesRequiredHeadersAndExactBodySignature(t *testing.T) {
	body := []byte(`{"model":"premium","stream":true}`)
	req, err := http.NewRequest(http.MethodPost, "https://example.test/inference/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Enabled:           true,
		AuthorizationMode: "auto",
		UserAgent:         "custom-client/1.0",
		SigningSecret:     "signing-secret",
		TimestampHeader:   "x-request-timestamp",
		SignatureHeader:   "x-request-signature",
		InstanceHeader:    "x-instance-id",
		TeamHeader:        "x-team-id",
	}
	state := State{InstanceID: "instance-1", TeamID: "team-1"}
	if err := PrepareRequest(req, body, cfg, state, "opaque-token"); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Apikey opaque-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "custom-client/1.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := req.Header.Get("x-instance-id"); got != "instance-1" {
		t.Fatalf("x-instance-id = %q", got)
	}
	if got := req.Header.Get("x-team-id"); got != "team-1" {
		t.Fatalf("x-team-id = %q", got)
	}
	timestamp := req.Header.Get("x-request-timestamp")
	if matched := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(timestamp); !matched {
		t.Fatalf("timestamp = %q, want JavaScript ISO format with exactly three fractional digits", timestamp)
	}
	bodyHash := sha256.Sum256(body)
	canonical := "POST\n/inference/v1/chat/completions\n" + timestamp + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte("signing-secret"))
	_, _ = mac.Write([]byte(canonical))
	wantSignature := hex.EncodeToString(mac.Sum(nil))
	if got := req.Header.Get("x-request-signature"); got != wantSignature {
		t.Fatalf("signature = %q, want %q", got, wantSignature)
	}
}

func TestPrepareRequestSupportsCustomAccessTokenHeaderAndPrefix(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.test/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AccessTokenHeader: "X-Auth-Token",
		AccessTokenPrefix: "Token ",
	}
	if err := PrepareRequest(req, nil, cfg, State{}, "access-token"); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-Auth-Token"); got != "Token access-token" {
		t.Fatalf("X-Auth-Token = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}

	rawReq, err := http.NewRequest(http.MethodGet, "https://example.test/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareRequest(rawReq, nil, Config{AccessTokenHeader: "X-Raw-Token", AuthorizationMode: "raw"}, State{}, "raw-token"); err != nil {
		t.Fatal(err)
	}
	if got := rawReq.Header.Get("X-Raw-Token"); got != "raw-token" {
		t.Fatalf("X-Raw-Token = %q", got)
	}
}

func TestValidateHeadersRejectsInvalidNamesAndValues(t *testing.T) {
	for _, cfg := range []Config{
		{AccessTokenHeader: "Bad Header"},
		{AccessTokenPrefix: "Token \r\nInjected: value"},
		{StaticHeaders: map[string]string{"X-Good": "bad\nvalue"}},
		{TokenHeaders: map[string]string{"Bad:Name": "value"}},
		{RefreshHeaders: map[string]string{"": "value"}},
	} {
		if err := ValidateHeaders(cfg); err == nil {
			t.Fatalf("ValidateHeaders(%#v) succeeded", cfg)
		}
	}
	if err := ValidateHeaders(Config{
		AccessTokenHeader: "X-Auth-Token",
		StaticHeaders:     map[string]string{"X-Required-Header": "required"},
	}); err != nil {
		t.Fatalf("valid headers rejected: %v", err)
	}
}

func TestValidateRequestHeaderValuesRejectsProviderControlledCharacters(t *testing.T) {
	tests := []struct {
		name  string
		state State
		token string
	}{
		{name: "access token", token: "token\r\nX-Injected: true"},
		{name: "instance ID", state: State{InstanceID: "instance\ninjected"}},
		{name: "team ID", state: State{TeamID: "team\x7finjected"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateRequestHeaderValues(Config{}, tt.state, tt.token); err == nil {
				t.Fatal("expected invalid runtime header value to be rejected")
			}
		})
	}
}

func TestDecodeTokenResponseRejectsInvalidAccessTokenHeaderValue(t *testing.T) {
	_, err := DecodeTokenResponse(
		strings.NewReader(`{"access_token":"token\r\nX-Injected: true"}`),
		Config{},
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "invalid HTTP header") {
		t.Fatalf("DecodeTokenResponse error = %v, want invalid HTTP header error", err)
	}
}

func TestDecodeTokenResponseLeavesOpaqueTokenExpiryUnknown(t *testing.T) {
	cfg := Config{AccessTokenField: "token"}
	tokens, err := DecodeTokenResponse(bytes.NewBufferString(`{"token":"opaque-access-token"}`), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.ExpiresAt != 0 {
		t.Fatalf("ExpiresAt = %d, want 0 for an opaque token without expiry metadata", tokens.ExpiresAt)
	}
}

func TestDecodeTokenResponseRejectsOversizedMetadata(t *testing.T) {
	body := `{"access_token":"` + strings.Repeat("x", int(MaxMetadataResponseBytes)) + `"}`
	_, err := DecodeTokenResponse(strings.NewReader(body), Config{}, "")
	if err == nil || !strings.Contains(err.Error(), "response limit") {
		t.Fatalf("DecodeTokenResponse error = %v, want response limit", err)
	}
}

func TestCoordinatedRefreshCoalescesConcurrentRefreshes(t *testing.T) {
	var calls atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := CoordinatedRefresh(t.Name(), func() (TokenSet, error) {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return TokenSet{AccessToken: "fresh"}, nil
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

type fakeRefreshLeaseStore struct {
	mu      sync.Mutex
	owner   string
	expires time.Time
}

func (s *fakeRefreshLeaseStore) TryAcquireOAuthRefreshLease(_ context.Context, _ string, owner string, now time.Time, lease time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != "" && s.owner != owner && s.expires.After(now) {
		return false, nil
	}
	s.owner = owner
	s.expires = now.Add(lease)
	return true, nil
}

func (s *fakeRefreshLeaseStore) ReleaseOAuthRefreshLease(_ context.Context, _ string, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == owner {
		s.owner = ""
		s.expires = time.Time{}
	}
	return nil
}

func TestCoordinatedRefreshDistributedCoalescesDifferentProcesses(t *testing.T) {
	store := &fakeRefreshLeaseStore{}
	var calls atomic.Int32
	var current atomic.Value
	current.Store("old")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(process int) {
			defer wg.Done()
			<-start
			_, err := CoordinatedRefreshDistributed(
				context.Background(),
				fmt.Sprintf("%s-process-%d", t.Name(), process),
				store,
				"config-1",
				func() (TokenSet, bool, error) {
					token := current.Load().(string)
					return TokenSet{AccessToken: token}, token != "old", nil
				},
				func() (TokenSet, error) {
					calls.Add(1)
					time.Sleep(20 * time.Millisecond)
					current.Store("fresh")
					return TokenSet{AccessToken: "fresh"}, nil
				},
			)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("distributed refresh calls = %d, want 1", calls.Load())
	}
}

func TestValidateEndpointRequiresExplicitPrivateOptIn(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "")
	if _, err := ValidateEndpoint("http://127.0.0.1:8080/token", false); err == nil {
		t.Fatal("expected private HTTP endpoint to be rejected")
	}
	if _, err := ValidateEndpoint("https://127.0.0.1/token", false); err == nil {
		t.Fatal("expected private HTTPS endpoint to be rejected")
	}
	if _, err := ValidateEndpoint("http://127.0.0.1:8080/token", true); err == nil {
		t.Fatal("expected model opt-in alone to be rejected without server policy")
	}
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	if _, err := ValidateEndpoint("http://127.0.0.1:8080/token", true); err != nil {
		t.Fatalf("private endpoint with opt-in: %v", err)
	}
	if _, err := ValidateEndpoint("https://api.example.test/token", false); err != nil {
		t.Fatalf("public HTTPS endpoint: %v", err)
	}
	if _, err := ValidateEndpoint("http://8.8.8.8/token", true); err == nil {
		t.Fatal("expected public HTTP endpoint to be rejected even with private endpoint opt-in")
	}
}

func TestHTTPClientDoesNotFollowRedirectsWithCustomHeaders(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()

	req, err := http.NewRequest(http.MethodGet, source.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Provider-Secret", "secret")
	resp, err := NewHTTPClient(time.Second, true).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want redirect response", resp.StatusCode)
	}
	if redirected.Load() {
		t.Fatal("custom provider client followed a redirect")
	}
}

func TestValidateAuthorizationParametersRejectsProtocolOverrides(t *testing.T) {
	for _, key := range []string{"state", "client_id", "redirect_uri", "code_challenge", "custom_callback"} {
		cfg := Config{
			CallbackParameter:       "custom_callback",
			AuthorizationParameters: map[string]string{key: "override"},
		}
		if err := ValidateAuthorizationParameters(cfg); err == nil {
			t.Fatalf("expected reserved authorization parameter %q to be rejected", key)
		}
	}
	if err := ValidateAuthorizationParameters(Config{
		AuthorizationParameters: map[string]string{"audience": "models"},
	}); err != nil {
		t.Fatalf("non-reserved authorization parameter rejected: %v", err)
	}
	for _, callbackParameter := range []string{"state", "client_id", "scope", "response_type", "code_challenge"} {
		if err := ValidateAuthorizationParameters(Config{CallbackParameter: callbackParameter}); err == nil {
			t.Fatalf("expected reserved callback parameter %q to be rejected", callbackParameter)
		}
	}
	for _, callbackParameter := range []string{"redirect_uri", "callback_uri", "return_to"} {
		if err := ValidateAuthorizationParameters(Config{CallbackParameter: callbackParameter}); err != nil {
			t.Fatalf("valid callback parameter %q rejected: %v", callbackParameter, err)
		}
	}
}

func TestRedactedConfigRemovesRefreshParameters(t *testing.T) {
	raw := MarshalConfig(Config{
		SigningSecret:     "signing-secret",
		RefreshParameters: map[string]string{"client_assertion": "secret"},
	})
	redacted, err := ParseConfig(RedactedConfigJSON(raw))
	if err != nil {
		t.Fatal(err)
	}
	if redacted.SigningSecret != "" || redacted.RefreshParameters != nil {
		t.Fatalf("sensitive configuration was rendered: %#v", redacted)
	}
}

func TestRefreshSupportsGenericClientParametersAndHeaders(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	var gotHeaders http.Header
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"rotated"}`))
	}))
	defer server.Close()

	cfg := Config{
		RefreshURL:              server.URL,
		AllowPrivateEndpoints:   true,
		RefreshRequestFormat:    "json",
		RefreshIncludeGrantType: true,
		RefreshIncludeClient:    true,
		TokenHeaders:            map[string]string{"X-Token-Header": "token-value"},
		RefreshHeaders:          map[string]string{"X-Refresh-Header": "refresh-value"},
		RefreshParameters:       map[string]string{"audience": "custom-api"},
	}
	tokens, err := Refresh(context.Background(), NewHTTPClient(time.Second, true), cfg, "old-refresh", RefreshOptions{
		ClientID: "client-id", ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "fresh" || tokens.RefreshToken != "rotated" {
		t.Fatalf("unexpected tokens: %#v", tokens)
	}
	for key, want := range map[string]string{
		"refresh_token": "old-refresh",
		"grant_type":    "refresh_token",
		"client_id":     "client-id",
		"client_secret": "client-secret",
		"audience":      "custom-api",
	} {
		if gotBody[key] != want {
			t.Fatalf("body[%q] = %q, want %q", key, gotBody[key], want)
		}
	}
	if gotHeaders.Get("X-Token-Header") != "token-value" || gotHeaders.Get("X-Refresh-Header") != "refresh-value" {
		t.Fatalf("refresh headers missing: %#v", gotHeaders)
	}
}

func TestDecodeTokenResponseSupportsConfiguredTokenAlias(t *testing.T) {
	cfg := Config{AccessTokenField: "token", RefreshTokenField: "refresh_token", ExpiresInField: "expires_in"}
	tokens, err := DecodeTokenResponse(bytes.NewBufferString(`{"token":"access-1","refresh_token":"refresh-1","expires_in":3600}`), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access-1" || tokens.RefreshToken != "refresh-1" || tokens.ExpiresAt == 0 {
		t.Fatalf("unexpected tokens: %#v", tokens)
	}
}

func TestExtractProfileStateAndModelNames(t *testing.T) {
	payload := map[string]any{
		"instances": []any{map[string]any{
			"instance_id": "instance-1",
			"teams":       []any{map[string]any{"id": "team-1"}},
		}},
		"data": []any{
			map[string]any{"model_name": "premium"},
			map[string]any{"model_name": "fast"},
		},
	}
	cfg := Config{
		ProfileInstancePath: "instances.0.instance_id",
		ProfileTeamPath:     "instances.0.teams.0.id",
		ModelsArrayPath:     "data",
		ModelIDField:        "model_name",
	}
	state := ExtractState(payload, cfg)
	if state.InstanceID != "instance-1" || state.TeamID != "team-1" {
		t.Fatalf("unexpected state: %#v", state)
	}
	ids := ExtractModelIDs(payload, cfg)
	if len(ids) != 2 || ids[0] != "premium" || ids[1] != "fast" {
		t.Fatalf("unexpected model IDs: %#v", ids)
	}
	stringIDs := ExtractModelIDs(map[string]any{
		"models": []any{"premium", "fast"},
	}, Config{ModelsArrayPath: "models", ModelIDField: "id"})
	if len(stringIDs) != 2 || stringIDs[0] != "premium" || stringIDs[1] != "fast" {
		t.Fatalf("unexpected string model IDs: %#v", stringIDs)
	}
}
