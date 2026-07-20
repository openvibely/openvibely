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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type trackingReadCloser struct {
	reader io.Reader
	closed atomic.Bool
}

func (b *trackingReadCloser) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

func (b *trackingReadCloser) Close() error {
	b.closed.Store(true)
	return nil
}

func validHostedIdentityBody(instanceID string) []byte {
	body, _ := json.Marshal(HostedIdentity{
		Subject:       "subject",
		Email:         "user@example.com",
		EmailVerified: true,
		InstanceID:    instanceID,
		InstanceSlug:  "alice",
		InstanceHost:  "alice.openvibely.ai",
	})
	return body
}

func TestHostedIdentityExactBodyAndFieldBoundaries(t *testing.T) {
	exact := HostedIdentity{
		Subject:       strings.Repeat("s", 128),
		Email:         strings.Repeat("e", 320),
		EmailVerified: true,
		InstanceID:    strings.Repeat("i", 128),
		InstanceSlug:  strings.Repeat("g", 63),
		InstanceHost:  strings.Repeat("h", 253),
	}
	exactBody, err := json.Marshal(exact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeHostedIdentity(exactBody, exact.InstanceID); err != nil {
		t.Fatalf("exact field boundaries rejected: %v", err)
	}

	for name, mutate := range map[string]func(*HostedIdentity){
		"subject":       func(v *HostedIdentity) { v.Subject += "x" },
		"email":         func(v *HostedIdentity) { v.Email += "x" },
		"instance_id":   func(v *HostedIdentity) { v.InstanceID += "x" },
		"instance_slug": func(v *HostedIdentity) { v.InstanceSlug += "x" },
		"instance_host": func(v *HostedIdentity) { v.InstanceHost += "x" },
	} {
		t.Run(name+" over limit", func(t *testing.T) {
			candidate := exact
			mutate(&candidate)
			body, marshalErr := json.Marshal(candidate)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, decodeErr := decodeHostedIdentity(body, candidate.InstanceID); decodeErr == nil {
				t.Fatal("over-limit identity field accepted")
			}
		})
	}

	valid := validHostedIdentityBody("instance-1")
	if len(valid) >= providerBodyMaxBytes {
		t.Fatal("identity fixture unexpectedly exceeds body limit")
	}
	exactSized := append(append([]byte(nil), valid...), []byte(strings.Repeat(" ", providerBodyMaxBytes-len(valid)))...)
	if _, err := decodeHostedIdentity(exactSized, "instance-1"); err != nil {
		t.Fatalf("exact 16384-byte body rejected: %v", err)
	}
	exactProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(exactSized)
	}))
	client := NewHostedSSOClient(exactProvider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
	if identity, err := client.Exchange(context.Background(), canonicalAuthTestValue("c"), canonicalAuthTestValue("v")); err != nil || identity.Email != "user@example.com" {
		t.Fatalf("exact-size provider response identity=%#v err=%v", identity, err)
	}
	exactProvider.Close()

	oversized := append(exactSized, ' ')
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(oversized)
	}))
	defer provider.Close()
	overClient := NewHostedSSOClient(provider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
	if _, err := overClient.Exchange(context.Background(), canonicalAuthTestValue("c"), canonicalAuthTestValue("v")); err == nil {
		t.Fatal("over-16384-byte provider body accepted")
	}
}

func TestHostedIdentityRejectsMalformedRequiredValues(t *testing.T) {
	valid := `{"sub":"s","email":"e","email_verified":true,"instance_id":"instance-1","instance_slug":"g","instance_host":"h"}`
	cases := map[string][]byte{
		"invalid UTF-8":           append([]byte(`{"sub":"`), append([]byte{0xff}, []byte(`","email":"e","email_verified":true,"instance_id":"instance-1"}`)...)...),
		"subject control":         []byte(strings.Replace(valid, `"sub":"s"`, `"sub":"s\u000a"`, 1)),
		"email control":           []byte(strings.Replace(valid, `"email":"e"`, `"email":"e\u007f"`, 1)),
		"instance control":        []byte(strings.Replace(valid, `"instance_id":"instance-1"`, `"instance_id":"instance\u0000-1"`, 1)),
		"missing subject":         []byte(strings.Replace(valid, `"sub":"s",`, "", 1)),
		"missing email":           []byte(strings.Replace(valid, `"email":"e",`, "", 1)),
		"missing instance":        []byte(strings.Replace(valid, `,"instance_id":"instance-1"`, "", 1)),
		"nonboolean verification": []byte(strings.Replace(valid, `"email_verified":true`, `"email_verified":"true"`, 1)),
		"truncated JSON":          []byte(valid[:len(valid)-1]),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeHostedIdentity(body, "instance-1"); err == nil {
				t.Fatal("malformed identity accepted")
			}
		})
	}
}

func TestHostedJSONContentTypeMatrix(t *testing.T) {
	accepted := []string{"application/json", "Application/JSON; Charset=UTF-8", "application/json;charset=utf-8"}
	for _, value := range accepted {
		header := http.Header{"Content-Type": []string{value}}
		if err := validateJSONContentType(header); err != nil {
			t.Fatalf("accepted Content-Type %q rejected: %v", value, err)
		}
	}
	rejected := map[string][]string{
		"missing":               nil,
		"empty":                 {""},
		"multiple fields":       {"application/json", "application/json"},
		"malformed":             {"application/json;"},
		"non JSON":              {"text/plain"},
		"duplicate parameter":   {"application/json; charset=utf-8; charset=utf-8"},
		"unsupported parameter": {"application/json; charset=utf-8; profile=x"},
		"wrong charset":         {"application/json; charset=iso-8859-1"},
	}
	for name, values := range rejected {
		t.Run(name, func(t *testing.T) {
			header := make(http.Header)
			for _, value := range values {
				header.Add("Content-Type", value)
			}
			if err := validateJSONContentType(header); err == nil {
				t.Fatal("invalid Content-Type accepted")
			}
		})
	}
}

func TestHostedMalformed429ContentTypesNeverRetry(t *testing.T) {
	values := [][]string{
		nil,
		{""},
		{"application/json", "application/json"},
		{"application/json;"},
		{"text/plain"},
		{"application/json; charset=utf-8; charset=utf-8"},
		{"application/json; profile=x"},
		{"application/json; charset=iso-8859-1"},
	}
	for _, headerValues := range values {
		name := strings.Join(headerValues, "|")
		if name == "" {
			name = "missing_or_empty"
		}
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				for _, value := range headerValues {
					w.Header().Add("Content-Type", value)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":"slow_down"}`)
			}))
			defer server.Close()
			client := NewHostedSSOClient(server.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
			client.Sleep = func(context.Context, time.Duration) error {
				t.Fatal("invalid media type retried")
				return nil
			}
			if _, err := client.Exchange(context.Background(), canonicalAuthTestValue("c"), canonicalAuthTestValue("v")); err == nil {
				t.Fatal("invalid media type accepted")
			}
			if attempts.Load() != 1 {
				t.Fatalf("attempts=%d", attempts.Load())
			}
		})
	}
}

func TestHostedExchangeTerminalStatusAndErrorMatrix(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"permanent invalid grant", http.StatusBadRequest, `{"error":"invalid_grant"}`},
		{"slow down on wrong status", http.StatusBadRequest, `{"error":"slow_down"}`},
		{"unknown code on 429", http.StatusTooManyRequests, `{"error":"future_code"}`},
		{"server error", http.StatusInternalServerError, `{"error":"server_error"}`},
		{"malformed error", http.StatusTooManyRequests, `{"error":`},
		{"oversized error", http.StatusTooManyRequests, strings.Repeat("x", providerBodyMaxBytes+1)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client := NewHostedSSOClient(server.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
			client.Sleep = func(context.Context, time.Duration) error {
				t.Fatal("terminal response retried")
				return nil
			}
			_, err := client.Exchange(context.Background(), canonicalAuthTestValue("c"), canonicalAuthTestValue("v"))
			if err == nil || attempts.Load() != 1 {
				t.Fatalf("err=%v attempts=%d", err, attempts.Load())
			}
			if strings.Contains(err.Error(), "future_code") {
				t.Fatalf("raw provider code escaped diagnostic boundary: %v", err)
			}
		})
	}
}

func TestHostedRedirectResponseBodyIsBoundedClosedAndNeverReplayed(t *testing.T) {
	body := &trackingReadCloser{reader: strings.NewReader(strings.Repeat("x", providerBodyMaxBytes+1))}
	var requests atomic.Int32
	client := NewHostedSSOClient("https://provider.example", "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
	client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if requests.Add(1) != 1 {
			t.Fatal("redirect form was replayed")
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Location":     []string{"https://redirect-target.example/sso/token"},
			},
			Body:    body,
			Request: r,
		}, nil
	})
	if _, err := client.Exchange(context.Background(), canonicalAuthTestValue("c"), canonicalAuthTestValue("v")); err == nil {
		t.Fatal("redirect exchange unexpectedly succeeded")
	}
	if requests.Load() != 1 || !body.closed.Load() {
		t.Fatalf("requests=%d bodyClosed=%v", requests.Load(), body.closed.Load())
	}
}

func TestHostedExchangeNetworkDeadlineAndCancellation(t *testing.T) {
	t.Run("network failure", func(t *testing.T) {
		client := NewHostedSSOClient("https://provider.example", "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
		var attempts atomic.Int32
		client.HTTPClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)
			return nil, errors.New("network unavailable")
		})
		if _, err := client.Exchange(context.Background(), canonicalAuthTestValue("c"), canonicalAuthTestValue("v")); err == nil || attempts.Load() != 1 {
			t.Fatalf("err=%v attempts=%d", err, attempts.Load())
		}
	})

	t.Run("attempt has five second deadline", func(t *testing.T) {
		client := NewHostedSSOClient("https://provider.example", "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
		client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) < 4*time.Second {
				t.Fatalf("attempt deadline=%v ok=%v", deadline, ok)
			}
			return nil, context.DeadlineExceeded
		})
		if _, err := client.Exchange(context.Background(), canonicalAuthTestValue("c"), canonicalAuthTestValue("v")); err == nil {
			t.Fatal("attempt timeout accepted")
		}
	})

	t.Run("in-flight cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		client := NewHostedSSOClient("https://provider.example", "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
		entered := make(chan struct{})
		client.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		done := make(chan error, 1)
		go func() {
			_, err := client.Exchange(ctx, canonicalAuthTestValue("c"), canonicalAuthTestValue("v"))
			done <- err
		}()
		<-entered
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled exchange succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("in-flight exchange ignored cancellation")
		}
	})

	t.Run("retry wait cancellation", func(t *testing.T) {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow_down"}`)
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		client := NewHostedSSOClient(server.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
		client.Sleep = func(waitCtx context.Context, _ time.Duration) error {
			cancel()
			<-waitCtx.Done()
			return waitCtx.Err()
		}
		if _, err := client.Exchange(ctx, canonicalAuthTestValue("c"), canonicalAuthTestValue("v")); err == nil || attempts.Load() != 1 {
			t.Fatalf("err=%v attempts=%d", err, attempts.Load())
		}
	})
}

func TestProviderErrorDecoderBoundaryMatrix(t *testing.T) {
	validDescription := strings.Repeat("é", 128)
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"one byte code", []byte(`{"error":"a"}`), "unknown_provider_error"},
		{"64 byte code", []byte(`{"error":"` + strings.Repeat("a", 64) + `"}`), "unknown_provider_error"},
		{"one byte description", []byte(`{"error":"slow_down","error_description":"a"}`), "slow_down"},
		{"256 byte description", []byte(`{"error":"slow_down","error_description":"` + validDescription + `"}`), "slow_down"},
		{"missing code", []byte(`{"error_description":"x"}`), "malformed_provider_error"},
		{"over code", []byte(`{"error":"` + strings.Repeat("a", 65) + `"}`), "malformed_provider_error"},
		{"over description", []byte(`{"error":"slow_down","error_description":"` + validDescription + `x"}`), "malformed_provider_error"},
		{"empty description", []byte(`{"error":"slow_down","error_description":""}`), "malformed_provider_error"},
		{"unknown member", []byte(`{"error":"slow_down","extra":1}`), "malformed_provider_error"},
		{"duplicate member", []byte(`{"error":"slow_down","error":"slow_down"}`), "malformed_provider_error"},
		{"trailing JSON", []byte(`{"error":"slow_down"}{}`), "malformed_provider_error"},
		{"control", []byte(`{"error":"slow_down","error_description":"bad\u000a"}`), "malformed_provider_error"},
		{"invalid UTF-8", append([]byte(`{"error":"slow_down","error_description":"`), append([]byte{0xff}, []byte(`"}`)...)...), "malformed_provider_error"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeProviderErrorCategory(tt.body); got != tt.want {
				t.Fatalf("category=%q want=%q", got, tt.want)
			}
		})
	}
}

func signRawHostedSessionForTest(payload []byte) string {
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(purposeMAC(hostedTestKey, hostedSessionDomain, payload))
}

func TestHostedSessionVerificationAndSizeBoundaryMatrix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	valid := SessionClaims{
		Version: 1, Subject: strings.Repeat("s", 128), Email: strings.Repeat("é", 160), Display: strings.Repeat("é", 160),
		InstanceID: strings.Repeat("i", 128), AuthSource: HostedAuthSource,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	}
	validPayload, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHostedSession(signRawHostedSessionForTest(validPayload), hostedTestKey, valid.InstanceID, now); err != nil {
		t.Fatalf("verification boundaries rejected: %v", err)
	}

	mutations := map[string]func(*SessionClaims){
		"zero version":        func(c *SessionClaims) { c.Version = 0 },
		"negative version":    func(c *SessionClaims) { c.Version = -1 },
		"unsupported version": func(c *SessionClaims) { c.Version = 2 },
		"empty subject":       func(c *SessionClaims) { c.Subject = "" },
		"subject over limit":  func(c *SessionClaims) { c.Subject += "x" },
		"email over limit":    func(c *SessionClaims) { c.Email += "x"; c.Display = c.Email },
		"display mismatch":    func(c *SessionClaims) { c.Display = "different" },
		"instance over limit": func(c *SessionClaims) { c.InstanceID += "x" },
		"wrong source":        func(c *SessionClaims) { c.AuthSource = "local" },
		"future issue time": func(c *SessionClaims) {
			c.IssuedAt = now.Add(time.Second).Unix()
			c.ExpiresAt = c.IssuedAt + 3600
		},
		"short lifetime": func(c *SessionClaims) { c.ExpiresAt-- },
		"long lifetime":  func(c *SessionClaims) { c.ExpiresAt++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			claims := valid
			mutate(&claims)
			payload, marshalErr := json.Marshal(claims)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, verifyErr := VerifyHostedSession(signRawHostedSessionForTest(payload), hostedTestKey, claims.InstanceID, now); verifyErr == nil {
				t.Fatal("invalid signed claims accepted")
			}
		})
	}

	invalidUTF8 := []byte(`{"v":1,"sub":"`)
	invalidUTF8 = append(invalidUTF8, 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","email":"e","display":"e","instance_id":"i","auth_source":"hosted_sso","iat":1700000000,"exp":1700003600}`)...)
	if _, err := VerifyHostedSession(signRawHostedSessionForTest(invalidUTF8), hostedTestKey, "i", now); err == nil {
		t.Fatal("invalid UTF-8 signed claims accepted")
	}
	controlClaims := valid
	controlClaims.Subject = "bad\nsubject"
	controlPayload, _ := json.Marshal(controlClaims)
	if _, err := VerifyHostedSession(signRawHostedSessionForTest(controlPayload), hostedTestKey, controlClaims.InstanceID, now); err == nil {
		t.Fatal("control-containing signed claims accepted")
	}

	oversized := SessionClaims{
		Version: 1, Subject: strings.Repeat("<", 128), Email: strings.Repeat("<", 320), Display: strings.Repeat("<", 320),
		InstanceID: strings.Repeat("<", 128), AuthSource: HostedAuthSource,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	}
	if _, err := SignHostedSession(oversized, hostedTestKey); err == nil || !strings.Contains(err.Error(), "2048") {
		t.Fatalf("oversized encoded session creation error=%v", err)
	}
}

func TestBrowserBindingStrictWireMatrix(t *testing.T) {
	nonce := canonicalAuthTestValue("b")
	binding, err := SignBrowserBinding(nonce, hostedTestKey)
	if err != nil {
		t.Fatal(err)
	}
	decodedNonce, _ := base64.RawURLEncoding.DecodeString(nonce)
	wrongInputSignature := base64.RawURLEncoding.EncodeToString(purposeMAC(hostedTestKey, browserBindingDomain, decodedNonce))
	cases := map[string]string{
		"missing signature":            nonce,
		"wrong separator position":     binding[:42] + "." + binding[43:],
		"padded nonce":                 nonce + "=." + binding[44:],
		"padded signature":             binding + "=",
		"short nonce":                  nonce[:42] + binding[43:],
		"long nonce":                   nonce + "A" + binding[43:],
		"signature over decoded nonce": nonce + "." + wrongInputSignature,
		"unsigned nonce":               nonce + "." + strings.Repeat("A", 43),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyBrowserBinding(value, hostedTestKey); err == nil {
				t.Fatal("malformed browser binding accepted")
			}
		})
	}
}

func TestPendingTransactionLifetimeExceedsCodeLifetimeAndExpiresExactly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := NewPendingStore(context.Background(), func() time.Time { return now })
	defer store.Close()
	browser := canonicalAuthTestValue("b")
	var sequence int
	generate := func() (string, string, error) {
		sequence++
		digest := sha256.Sum256([]byte(fmt.Sprintf("lifetime-%d", sequence)))
		return base64.RawURLEncoding.EncodeToString(digest[:]), canonicalAuthTestValue("v"), nil
	}
	first, err := store.Admit(browser, "/", generate)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(61 * time.Second)
	if _, ok, _ := store.Consume(first.State, browser); !ok {
		t.Fatal("transaction expired at provider code lifetime")
	}
	second, err := store.Admit(browser, "/", generate)
	if err != nil {
		t.Fatal(err)
	}
	now = second.ExpiresAt
	if _, ok, _ := store.Consume(second.State, browser); ok {
		t.Fatal("transaction remained valid at exact ten-minute expiry")
	}
}

func TestPendingStorePeriodicCleanupAndConcurrentShutdown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ticks := make(chan time.Time)
	store := newPendingStore(context.Background(), func() time.Time { return now }, ticks, nil)
	var sequence atomic.Int64
	generate := func() (string, string, error) {
		digest := sha256.Sum256([]byte(fmt.Sprintf("periodic-%d", sequence.Add(1))))
		return base64.RawURLEncoding.EncodeToString(digest[:]), canonicalAuthTestValue("v"), nil
	}
	browser := canonicalAuthTestValue("b")
	if _, err := store.Admit(browser, "/", generate); err != nil {
		t.Fatal(err)
	}
	now = now.Add(pendingLifetime)
	ticks <- now
	deadline := time.Now().Add(time.Second)
	for {
		store.mu.Lock()
		entries := len(store.entries)
		browsers := len(store.browserCounts)
		store.mu.Unlock()
		if entries == 0 && browsers == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("periodic cleanup left entries=%d browsers=%d", entries, browsers)
		}
		time.Sleep(time.Millisecond)
	}

	now = now.Add(time.Minute + time.Second)
	var workers sync.WaitGroup
	for i := 0; i < 100; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			digest := sha256.Sum256([]byte(fmt.Sprintf("shutdown-browser-%d", i%12)))
			browserNonce := base64.RawURLEncoding.EncodeToString(digest[:])
			tx, err := store.Admit(browserNonce, "/", generate)
			if err == nil {
				if i%2 == 0 {
					store.Consume(tx.State, browserNonce)
				} else {
					store.Discard(tx.State)
				}
			}
			if i%5 == 0 {
				store.Prune()
			}
		}(i)
	}
	var closers sync.WaitGroup
	for i := 0; i < 8; i++ {
		closers.Add(1)
		go func() {
			defer closers.Done()
			store.Close()
		}()
	}
	workers.Wait()
	closers.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.closed || len(store.entries) != 0 || len(store.browserCounts) != 0 {
		t.Fatalf("shutdown invariant closed=%v entries=%d browsers=%d", store.closed, len(store.entries), len(store.browserCounts))
	}
}

func TestHostedContinuationAndCallbackExactBoundaries(t *testing.T) {
	destination := "/" + strings.Repeat("a", 4095)
	encoded := HostedSSOStartURL(destination)[len("/auth/sso/start?next="):]
	if len(destination) != 4096 || len(encoded) != 5462 {
		t.Fatalf("destination=%d encoded=%d", len(destination), len(encoded))
	}
	if got, err := DecodeHostedNext("next="+encoded, 8192); err != nil || got != destination {
		t.Fatalf("exact continuation got length=%d err=%v", len(got), err)
	}
	if _, err := DecodeHostedNext("next="+encoded, 8193); err == nil {
		t.Fatal("8193-byte request URI accepted")
	}
	decodedOver := []byte("/" + strings.Repeat("a", 4096))
	canonicalOver := base64.RawURLEncoding.EncodeToString(decodedOver)
	if len(canonicalOver) != 5463 {
		t.Fatalf("4097-byte encoding length=%d", len(canonicalOver))
	}
	if _, err := DecodeHostedNext("next="+canonicalOver, 8192); err == nil {
		t.Fatal("4097-byte continuation accepted")
	}

	state := canonicalAuthTestValue("s")
	for _, length := range []int{1, 64} {
		errorCode := strings.Repeat("a", length)
		if _, err := ParseHostedCallback("error="+errorCode+"&state="+state, 8192); err != nil {
			t.Fatalf("error code length %d rejected: %v", length, err)
		}
	}
	for _, description := range []string{"a", strings.Repeat("é", 128)} {
		raw := "error=access_denied&state=" + state + "&error_description=" + url.QueryEscape(description)
		if _, err := ParseHostedCallback(raw, 8192); err != nil {
			t.Fatalf("description length %d rejected: %v", len(description), err)
		}
	}
	invalid := []string{
		"error=" + strings.Repeat("a", 65) + "&state=" + state,
		"error=access_denied&state=" + state + "&error_description=" + url.QueryEscape(strings.Repeat("é", 128)+"x"),
		"error=access_denied&state=" + state + "&error_description=%FF",
		"error=bad%0Avalue&state=" + state,
		"error=access_denied&state=" + state + "&error_description=%",
	}
	for _, raw := range invalid {
		if _, err := ParseHostedCallback(raw, len("/auth/sso/callback?")+len(raw)); err == nil {
			t.Fatalf("invalid callback accepted: %q", raw)
		}
	}
}
