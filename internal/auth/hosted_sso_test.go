package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var hostedTestKey = []byte("0123456789abcdef0123456789abcdef")

func fixedRandomReader() io.Reader {
	return strings.NewReader(strings.Repeat("r", 96))
}

func TestGenerateHostedStateAndPKCE(t *testing.T) {
	state, verifier, challenge, err := GenerateStateAndPKCE(fixedRandomReader())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"state": state, "verifier": verifier, "challenge": challenge} {
		if len(value) != 43 {
			t.Fatalf("%s length=%d", name, len(value))
		}
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			t.Fatalf("%s is not canonical 32-byte base64url: %v", name, err)
		}
	}
	want := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(want[:]) {
		t.Fatal("unexpected S256 challenge")
	}
}

func TestHostedAuthorizationURLAndWorstCaseLengths(t *testing.T) {
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	control := "https://" + host + ":65535"
	callback := control + "/auth/sso/callback"
	client := &HostedSSOClient{ControlURL: control, ClientID: strings.Repeat("é", 64), CallbackURL: callback}
	got, err := client.AuthorizationURL(strings.Repeat("s", 43), strings.Repeat("c", 43))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	if len(control) != 267 || len(control+"/sso/token") != 277 || len(callback) != 285 || len(url.QueryEscape(callback)) != 299 || len(u.RawQuery) != 862 || len(got) != 1144 {
		t.Fatalf("unexpected worst-case lengths: control=%d token=%d callback=%d escaped=%d query=%d url=%d", len(control), len(control+"/sso/token"), len(callback), len(url.QueryEscape(callback)), len(u.RawQuery), len(got))
	}
	q := u.Query()
	for key, want := range map[string]string{
		"response_type": "code", "client_id": strings.Repeat("é", 64), "redirect_uri": callback,
		"state": strings.Repeat("s", 43), "code_challenge": strings.Repeat("c", 43), "code_challenge_method": "S256",
	} {
		if q.Get(key) != want || len(q[key]) != 1 {
			t.Fatalf("%s=%q", key, q[key])
		}
	}

	client.ControlURL = "https://" + strings.Repeat("x", 2040)
	if _, err := client.AuthorizationURL(strings.Repeat("s", 43), strings.Repeat("c", 43)); err == nil {
		t.Fatal("expected defensive authorization URL size failure")
	}
}

func TestHostedSessionAndBrowserPurposeSeparation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	claims := SessionClaims{Version: 1, Subject: "subject", Email: "a@example.com", Display: "a@example.com", InstanceID: "instance-1", AuthSource: HostedAuthSource, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	token, err := SignHostedSession(claims, hostedTestKey)
	if err != nil {
		t.Fatal(err)
	}
	const expectedSessionToken = "eyJ2IjoxLCJzdWIiOiJzdWJqZWN0IiwiZW1haWwiOiJhQGV4YW1wbGUuY29tIiwiZGlzcGxheSI6ImFAZXhhbXBsZS5jb20iLCJpbnN0YW5jZV9pZCI6Imluc3RhbmNlLTEiLCJhdXRoX3NvdXJjZSI6Imhvc3RlZF9zc28iLCJpYXQiOjE3MDAwMDAwMDAsImV4cCI6MTcwMDAwMzYwMH0.IY8kPRYqCgl5rB9S7Ih5xcdm4nmgXF8dXR73x4rVjZI"
	if token != expectedSessionToken {
		t.Fatalf("hosted session golden mismatch:\n got %s\nwant %s", token, expectedSessionToken)
	}
	payload, _ := json.Marshal(claims)
	if got, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0]); string(got) != string(payload) {
		t.Fatalf("noncanonical payload: %s", got)
	}
	verified, err := VerifyHostedSession(token, hostedTestKey, "instance-1", now)
	if err != nil || *verified != claims {
		t.Fatalf("verify=%#v err=%v", verified, err)
	}

	nonce := base64.RawURLEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyzABCDEF"))
	browser, err := SignBrowserBinding(nonce, hostedTestKey)
	if err != nil {
		t.Fatal(err)
	}
	const expectedBrowserBinding = "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXpBQkNERUY.IdgRk-QJq_RBYywUSo_Ztz9l2ZJ6e93OH0-ZSYw7NFI"
	if browser != expectedBrowserBinding {
		t.Fatalf("browser-binding golden mismatch:\n got %s\nwant %s", browser, expectedBrowserBinding)
	}
	if len(browser) != 87 {
		t.Fatalf("browser binding length=%d", len(browser))
	}
	if _, err := VerifyHostedSession(browser, hostedTestKey, "instance-1", now); err == nil {
		t.Fatal("browser binding accepted as hosted session")
	}
	if _, err := VerifyBrowserBinding(token, hostedTestKey); err == nil {
		t.Fatal("hosted session accepted as browser binding")
	}
	if got, err := VerifyBrowserBinding(browser, hostedTestKey); err != nil || got != nonce {
		t.Fatalf("browser verify=%q err=%v", got, err)
	}
}

func TestHostedSessionStrictClaims(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	valid := SessionClaims{Version: 1, Subject: strings.Repeat("s", 128), Email: strings.Repeat("e", 320), Display: strings.Repeat("e", 320), InstanceID: strings.Repeat("i", 128), AuthSource: HostedAuthSource, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	if _, err := SignHostedSession(valid, hostedTestKey); err != nil {
		t.Fatalf("boundary claims rejected: %v", err)
	}
	mutations := []func(*SessionClaims){
		func(c *SessionClaims) { c.Version = 0 }, func(c *SessionClaims) { c.Version = 2 },
		func(c *SessionClaims) { c.Subject = "" }, func(c *SessionClaims) { c.Subject = strings.Repeat("s", 129) },
		func(c *SessionClaims) { c.Email = "bad\n@example.com" }, func(c *SessionClaims) { c.Display += "x" },
		func(c *SessionClaims) { c.AuthSource = "local" }, func(c *SessionClaims) { c.ExpiresAt++ },
	}
	for i, mutate := range mutations {
		claims := valid
		mutate(&claims)
		if _, err := SignHostedSession(claims, hostedTestKey); err == nil {
			t.Fatalf("mutation %d accepted", i)
		}
	}
	future := valid
	future.IssuedAt = now.Add(time.Second).Unix()
	future.ExpiresAt = future.IssuedAt + int64(time.Hour/time.Second)
	futureToken, err := SignHostedSession(future, hostedTestKey)
	if err != nil {
		t.Fatalf("sign future fixture: %v", err)
	}
	if _, err := VerifyHostedSession(futureToken, hostedTestKey, valid.InstanceID, now); err == nil {
		t.Fatal("future-issued token accepted")
	}
	if _, err := VerifyHostedSession(strings.Repeat("x", 2049), hostedTestKey, valid.InstanceID, now); err == nil {
		t.Fatal("oversized token accepted")
	}
}

func TestSafeDestinationAndBoundedNext(t *testing.T) {
	for _, unsafe := range []string{"//evil.example", "/\\evil.example", "/%5cevil", "/%2fevil", "/%00evil", "https://evil.example", "/bad%zz", "/bad\n"} {
		if got := SanitizeDestination(unsafe); got != "/" {
			t.Fatalf("SanitizeDestination(%q)=%q", unsafe, got)
		}
	}
	safe := "/tasks?project_id=p1&tab=thread"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(safe))
	if got, err := DecodeHostedNext("next="+encoded, len("/auth/sso/start?next=")+len(encoded)); err != nil || got != safe {
		t.Fatalf("got=%q err=%v", got, err)
	}
	for _, raw := range []string{"next=" + encoded + "&next=" + encoded, "next=" + encoded + "&x=1", "next=" + encoded + "=", "next=***"} {
		if _, err := DecodeHostedNext(raw, len(raw)); err == nil {
			t.Fatalf("accepted malformed next query %q", raw)
		}
	}
	if _, err := DecodeHostedNext("", 8193); err == nil {
		t.Fatal("accepted oversized request URI")
	}
	if got := HostedSSOStartURL(safe); !strings.HasPrefix(got, "/auth/sso/start?next=") || strings.Contains(got, "project_id") {
		t.Fatalf("bad start URL %q", got)
	}
}

func TestPendingStoreBoundsConsumptionAndExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Unix(1_700_000_000, 0)
	store := NewPendingStore(ctx, func() time.Time { return now })
	defer store.Close()
	browser := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	verifierFixture := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("v", 32)))
	counter := 0
	gen := func() (string, string, error) {
		counter++
		return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('a'+counter)), 32))), verifierFixture, nil
	}
	var first, state string
	for i := 0; i < 5; i++ {
		tx, err := store.Admit(browser, "/", gen)
		if err != nil {
			t.Fatal(err)
		}
		state = tx.State
		if i == 0 {
			first = tx.State
		}
	}
	if store.Count() != 4 || store.BrowserCount(browser) != 4 {
		t.Fatalf("counts global=%d browser=%d", store.Count(), store.BrowserCount(browser))
	}
	if _, ok, _ := store.Consume(first, browser); ok {
		t.Fatal("oldest per-browser transaction was not evicted")
	}
	if _, ok, remaining := store.Consume(state, strings.Repeat("x", 43)); ok || !remaining {
		t.Fatal("wrong browser consumed transaction")
	}
	if _, ok, _ := store.Consume(state, browser); !ok {
		t.Fatal("matching transaction not consumed")
	}
	if _, ok, _ := store.Consume(state, browser); ok {
		t.Fatal("transaction replay succeeded")
	}

	now = now.Add(11 * time.Minute)
	store.Prune()
	if store.Count() != 0 || store.BrowserCount(browser) != 0 {
		t.Fatal("expired capacity not reclaimed")
	}
}

func TestHostedTokenExchangeStrictValidationAndRetry(t *testing.T) {
	var attempts atomic.Int32
	code := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
	verifier := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("v", 32)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected request %s %q", r.Method, r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		for key, want := range map[string]string{"grant_type": "authorization_code", "code": code, "client_id": "instance-1", "redirect_uri": "https://alice.openvibely.ai/auth/sso/callback", "code_verifier": verifier} {
			if r.Form.Get(key) != want {
				t.Errorf("%s=%q", key, r.Form.Get(key))
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if attempt == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
			return
		}
		_, _ = io.WriteString(w, `{"sub":"subject","email":"user@example.com","email_verified":true,"instance_id":"instance-1","instance_slug":"alice","instance_host":"alice.openvibely.ai"}`)
	}))
	defer server.Close()
	client := NewHostedSSOClient(server.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
	var delays []time.Duration
	client.Sleep = func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	identity, err := client.Exchange(context.Background(), code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Email != "user@example.com" || identity.InstanceID != "instance-1" || attempts.Load() != 2 || len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("identity=%#v attempts=%d delays=%v", identity, attempts.Load(), delays)
	}
}

func TestParseHostedCallbackStrictRawQuery(t *testing.T) {
	state := canonicalAuthTestValue("s")
	code := canonicalAuthTestValue("c")
	got, err := ParseHostedCallback("code="+code+"&state="+state, 100)
	if err != nil || got.Code != code || got.State != state || got.IsError {
		t.Fatalf("success callback=%#v err=%v", got, err)
	}
	description := strings.Repeat("é", 128)
	rawDescription := url.QueryEscape(description)
	if len(rawDescription) != 768 {
		t.Fatalf("encoded description length=%d", len(rawDescription))
	}
	got, err = ParseHostedCallback("error="+strings.Repeat("e", 64)+"&state="+state+"&error_description="+rawDescription, 1000)
	if err != nil || !got.IsError || got.ErrorDescription != description {
		t.Fatalf("error callback=%#v err=%v", got, err)
	}
	for _, raw := range []string{
		"code=" + code + "&state=" + state + "&x=1",
		"code=" + code + "&state=" + state + "&state=" + state,
		"code=" + code + "&state=" + state[:42] + "+",
		"code=" + code + "&state=%" + strings.ToUpper(fmt.Sprintf("%x", state[0])) + state[1:],
		"code=" + code + ";state=" + state,
		"code=" + code + "&&state=" + state,
		"code=" + code + "=x&state=" + state,
		"Code=" + code + "&state=" + state,
		"error=&state=" + state,
		"error=bad%00value&state=" + state,
		"error=access_denied&state=" + state + "&error_description=%00",
	} {
		if _, err := ParseHostedCallback(raw, len(raw)); err == nil {
			t.Fatalf("accepted malformed callback %q", raw)
		}
	}
	if _, err := ParseHostedCallback("code="+code+"&state="+state, 8193); err == nil {
		t.Fatal("accepted oversized callback request URI")
	}
}

func canonicalAuthTestValue(fill string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(fill, 32)))
}

func TestPendingStoreRateCapacityFailureAndShutdown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	store := NewPendingStore(ctx, func() time.Time { return now })
	browser := canonicalAuthTestValue("b")
	counter := 0
	generate := func() (string, string, error) {
		counter++
		digest := sha256.Sum256([]byte(fmt.Sprintf("state-%d", counter)))
		return base64.RawURLEncoding.EncodeToString(digest[:]), canonicalAuthTestValue("v"), nil
	}
	var first string
	for i := 0; i < pendingPerBrowser; i++ {
		tx, err := store.Admit(browser, "/", generate)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = tx.State
		}
	}
	if _, err := store.Admit(browser, "/", func() (string, string, error) { return "", "", errors.New("entropy failure") }); err == nil {
		t.Fatal("expected failed generation")
	}
	if _, ok, _ := store.Consume(first, browser); !ok {
		t.Fatal("failed admission evicted the oldest live transaction")
	}

	store.Close()
	if store.Count() != 0 {
		t.Fatal("shutdown did not clear live capacity")
	}
	if _, err := store.Admit(browser, "/", generate); err == nil {
		t.Fatal("closed store admitted a transaction")
	}
	cancel()
}

func TestPendingStoreRollingRateIndependentOfRemoval(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewPendingStore(context.Background(), func() time.Time { return now })
	defer store.Close()
	counter := 0
	generate := func() (string, string, error) {
		counter++
		digest := sha256.Sum256([]byte(fmt.Sprintf("rate-state-%d", counter)))
		return base64.RawURLEncoding.EncodeToString(digest[:]), canonicalAuthTestValue("v"), nil
	}
	var states []string
	for i := 0; i < startsPerMinute; i++ {
		browserDigest := sha256.Sum256([]byte(fmt.Sprintf("browser-%d", i)))
		tx, err := store.Admit(base64.RawURLEncoding.EncodeToString(browserDigest[:]), "/", generate)
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		states = append(states, tx.State)
	}
	for _, state := range states {
		store.Discard(state)
	}
	if store.Count() != 0 {
		t.Fatal("discard did not reclaim live capacity")
	}
	if _, err := store.Admit(canonicalAuthTestValue("z"), "/", generate); !errors.Is(err, ErrStartRateLimited) {
		t.Fatalf("removal erased rolling rate events: %v", err)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if _, err := store.Admit(canonicalAuthTestValue("z"), "/", generate); err != nil {
		t.Fatalf("expired rolling timestamps still blocked admission: %v", err)
	}
}

func TestPendingStoreConcurrentAdmissionConsumptionPruningAndShutdown(t *testing.T) {
	var nowUnix atomic.Int64
	nowUnix.Store(1_700_000_000)
	store := NewPendingStore(context.Background(), func() time.Time { return time.Unix(nowUnix.Load(), 0) })
	var counter atomic.Int64
	generate := func() (string, string, error) {
		value := counter.Add(1)
		digest := sha256.Sum256([]byte(fmt.Sprintf("concurrent-state-%d", value)))
		return base64.RawURLEncoding.EncodeToString(digest[:]), canonicalAuthTestValue("v"), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			browserDigest := sha256.Sum256([]byte(fmt.Sprintf("concurrent-browser-%d", i%12)))
			browser := base64.RawURLEncoding.EncodeToString(browserDigest[:])
			tx, err := store.Admit(browser, "/", generate)
			if err == nil && i%2 == 0 {
				store.Consume(tx.State, browser)
			}
			if i%7 == 0 {
				store.Prune()
			}
		}(i)
	}
	wg.Wait()
	store.mu.Lock()
	total := 0
	for _, count := range store.browserCounts {
		if count <= 0 {
			store.mu.Unlock()
			t.Fatal("non-positive browser live count")
		}
		total += count
	}
	if total != len(store.entries) {
		store.mu.Unlock()
		t.Fatalf("live count total=%d entries=%d", total, len(store.entries))
	}
	store.mu.Unlock()

	nowUnix.Add(int64((pendingLifetime + time.Second) / time.Second))
	var pruneWG sync.WaitGroup
	for i := 0; i < 20; i++ {
		pruneWG.Add(1)
		go func() { defer pruneWG.Done(); store.Prune() }()
	}
	pruneWG.Wait()
	if store.Count() != 0 {
		t.Fatalf("expired concurrent entries remain=%d", store.Count())
	}
	store.Close()
}

func TestPendingStoreGlobalCapacityAndImmediateReuse(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewPendingStore(context.Background(), func() time.Time { return now })
	defer store.Close()
	counter := 0
	generate := func() (string, string, error) {
		counter++
		digest := sha256.Sum256([]byte(fmt.Sprintf("global-state-%d", counter)))
		return base64.RawURLEncoding.EncodeToString(digest[:]), canonicalAuthTestValue("v"), nil
	}
	var firstState string
	for i := 0; i < pendingGlobal; i++ {
		if i > 0 && i%startsPerMinute == 0 {
			now = now.Add(time.Minute + time.Nanosecond)
		}
		browserDigest := sha256.Sum256([]byte(fmt.Sprintf("global-browser-%d", i)))
		tx, err := store.Admit(base64.RawURLEncoding.EncodeToString(browserDigest[:]), "/", generate)
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		if i == 0 {
			firstState = tx.State
		}
	}
	extraBrowser := canonicalAuthTestValue("x")
	if _, err := store.Admit(extraBrowser, "/", generate); !errors.Is(err, ErrPendingFull) {
		t.Fatalf("global full error=%v", err)
	}
	if !store.Discard(firstState) || store.Discard(firstState) {
		t.Fatal("discard was not idempotent")
	}
	if _, err := store.Admit(extraBrowser, "/", generate); err != nil {
		t.Fatalf("reclaimed capacity not immediately reusable: %v", err)
	}
	if store.Count() != pendingGlobal {
		t.Fatalf("global count=%d", store.Count())
	}
}

func TestHostedSessionStrictWireDecoder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	claims := SessionClaims{Version: 1, Subject: "subject", Email: "a@example.com", Display: "a@example.com", InstanceID: "instance-1", AuthSource: HostedAuthSource, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	payload, _ := json.Marshal(claims)
	signPayload := func(raw []byte) string {
		return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(purposeMAC(hostedTestKey, hostedSessionDomain, raw))
	}
	for name, raw := range map[string][]byte{
		"alternate serialization": []byte(`{"sub":"subject","v":1,"email":"a@example.com","display":"a@example.com","instance_id":"instance-1","auth_source":"hosted_sso","iat":1700000000,"exp":1700003600}`),
		"duplicate":               []byte(`{"v":1,"sub":"subject","sub":"other","email":"a@example.com","display":"a@example.com","instance_id":"instance-1","auth_source":"hosted_sso","iat":1700000000,"exp":1700003600}`),
		"unknown":                 []byte(`{"v":1,"sub":"subject","email":"a@example.com","display":"a@example.com","instance_id":"instance-1","auth_source":"hosted_sso","iat":1700000000,"exp":1700003600,"future":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyHostedSession(signPayload(raw), hostedTestKey, "instance-1", now); err == nil {
				t.Fatal("strict decoder accepted noncanonical payload")
			}
		})
	}
	valid := signPayload(payload)
	for _, token := range []string{valid + ".extra", strings.Replace(valid, ".", "", 1), strings.Split(valid, ".")[0] + "=." + strings.Split(valid, ".")[1]} {
		if _, err := VerifyHostedSession(token, hostedTestKey, "instance-1", now); err == nil {
			t.Fatalf("accepted malformed token %q", token)
		}
	}
}

func TestHostedExchangeRejectsRedirectsAndMalformedMediaWithoutRetry(t *testing.T) {
	code := canonicalAuthTestValue("c")
	verifier := canonicalAuthTestValue("v")
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprintf("redirect_%d", status), func(t *testing.T) {
			var targetRequests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) }))
			defer target.Close()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", target.URL)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"server_error"}`)
			}))
			defer provider.Close()
			client := NewHostedSSOClient(provider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
			client.HTTPClient = &http.Client{}
			if _, err := client.Exchange(context.Background(), code, verifier); err == nil {
				t.Fatal("redirect response accepted")
			}
			if targetRequests.Load() != 0 {
				t.Fatal("token form followed redirect")
			}
		})
	}

	t.Run("malformed 429 content type", func(t *testing.T) {
		var attempts atomic.Int32
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
		}))
		defer provider.Close()
		client := NewHostedSSOClient(provider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
		client.Sleep = func(context.Context, time.Duration) error { t.Fatal("malformed 429 retried"); return nil }
		if _, err := client.Exchange(context.Background(), code, verifier); err == nil {
			t.Fatal("malformed media type accepted")
		}
		if attempts.Load() != 1 {
			t.Fatalf("attempts=%d", attempts.Load())
		}
	})
}

func TestHostedExchangeRetryAttemptAndTotalBudgets(t *testing.T) {
	code := canonicalAuthTestValue("c")
	verifier := canonicalAuthTestValue("v")
	t.Run("at most three attempts", func(t *testing.T) {
		var attempts atomic.Int32
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
		}))
		defer provider.Close()
		client := NewHostedSSOClient(provider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
		var delays []time.Duration
		client.Sleep = func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil }
		if _, err := client.Exchange(context.Background(), code, verifier); err == nil {
			t.Fatal("permanent slow_down unexpectedly succeeded")
		}
		if attempts.Load() != 3 || len(delays) != 2 {
			t.Fatalf("attempts=%d delays=%v", attempts.Load(), delays)
		}
	})

	t.Run("skip retry at remaining budget", func(t *testing.T) {
		var attempts atomic.Int32
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
		}))
		defer provider.Close()
		fakeNow := time.Unix(1_700_000_000, 0)
		client := NewHostedSSOClient(provider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
		client.Now = func() time.Time { return fakeNow }
		sleeps := 0
		client.Sleep = func(_ context.Context, delay time.Duration) error {
			sleeps++
			if delay != 3*time.Second {
				t.Fatalf("delay=%s", delay)
			}
			fakeNow = fakeNow.Add(9 * time.Second)
			return nil
		}
		if _, err := client.Exchange(context.Background(), code, verifier); err == nil {
			t.Fatal("slow_down unexpectedly succeeded")
		}
		if attempts.Load() != 2 || sleeps != 1 {
			t.Fatalf("attempts=%d sleeps=%d", attempts.Load(), sleeps)
		}
	})
}

func TestHostedExchangeHonorsShorterParentDeadlineBeforeRetry(t *testing.T) {
	code := canonicalAuthTestValue("c")
	verifier := canonicalAuthTestValue("v")
	var attempts atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"slow_down"}`)
	}))
	defer provider.Close()

	parent, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	client := NewHostedSSOClient(provider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
	client.Sleep = func(context.Context, time.Duration) error {
		t.Fatal("retry sleeper called when delay exceeds the effective parent deadline")
		return nil
	}
	if _, err := client.Exchange(parent, code, verifier); err == nil {
		t.Fatal("slow_down unexpectedly succeeded")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts=%d", attempts.Load())
	}
}

func TestRetryDelayPolicy(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"1": time.Second, "2": 2 * time.Second, "3": 3 * time.Second, "4": 3 * time.Second,
		"999999": 3 * time.Second, strings.Repeat("9", 128): 3 * time.Second,
		"": time.Second, "0": time.Second, "-1": time.Second,
		"+2": time.Second, "1.5": time.Second, " 2 ": time.Second, "Wed, 21 Oct 2015 07:28:00 GMT": time.Second,
	} {
		if got := retryDelay(value); got != want {
			t.Fatalf("Retry-After %q delay=%s want=%s", value, got, want)
		}
	}
}

func TestProviderErrorCategoriesAreBounded(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":"invalid_grant"}`:                       "invalid_grant",
		`{"error":"provider_added_future_code"}`:          "unknown_provider_error",
		`{"error":"bad value"}`:                           "malformed_provider_error",
		`{"error":"slow_down","unknown":"provider text"}`: "malformed_provider_error",
		`{"error":"slow_down","error":"invalid_grant"}`:   "malformed_provider_error",
	} {
		if got := decodeProviderErrorCategory([]byte(body)); got != want {
			t.Fatalf("body=%s got=%q want=%q", body, got, want)
		}
	}
}

func TestHostedTokenExchangeRejectsMalformedIdentity(t *testing.T) {
	code := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("c", 32)))
	verifier := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("v", 32)))
	tests := []string{
		`{"sub":"s","email":"e","instance_id":"instance-1"}`,
		`{"sub":"s","email":"e","email_verified":false,"instance_id":"instance-1"}`,
		`{"sub":"s","email":"e","email_verified":null,"instance_id":"instance-1"}`,
		`{"sub":"s","email":"e","email_verified":true,"instance_id":"wrong"}`,
		`{"sub":"s","email":"e","email_verified":true,"instance_id":"instance-1","unknown":1}`,
		`{"sub":"s","sub":"other","email":"e","email_verified":true,"instance_id":"instance-1"}`,
		`{"sub":"s","email":"e","email_verified":true,"instance_id":"instance-1","instance_id":"instance-1"}`,
		`{"sub":"s","email":"e","email_verified":true,"email_verified":true,"instance_id":"instance-1"}`,
		`{"sub":"s","email":"e","email_verified":true,"instance_id":"instance-1"} {}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			client := NewHostedSSOClient(server.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
			if _, err := client.Exchange(context.Background(), code, verifier); err == nil {
				t.Fatal("malformed identity accepted")
			}
		})
	}
}
