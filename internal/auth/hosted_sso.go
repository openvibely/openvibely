package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	HostedAuthSource      = "hosted_sso"
	HostedSessionLifetime = time.Hour
	HostedTokenMaxBytes   = 2048
	providerBodyMaxBytes  = 16384
	pendingLifetime       = 10 * time.Minute
	pendingPerBrowser     = 4
	pendingGlobal         = 256
	startsPerMinute       = 60
)

var (
	hostedSessionDomain  = []byte("openvibely/session/v1\x00")
	browserBindingDomain = []byte("openvibely/sso-browser/v1\x00")
	ErrPendingNotFound   = errors.New("pending SSO transaction not found")
	ErrPendingFull       = errors.New("pending SSO transaction store is full")
	ErrStartRateLimited  = errors.New("SSO start rate limit exceeded")
)

type SessionClaims struct {
	Version    int    `json:"v"`
	Subject    string `json:"sub"`
	Email      string `json:"email"`
	Display    string `json:"display"`
	InstanceID string `json:"instance_id"`
	AuthSource string `json:"auth_source"`
	IssuedAt   int64  `json:"iat"`
	ExpiresAt  int64  `json:"exp"`
}

type HostedIdentity struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	InstanceID    string `json:"instance_id"`
	InstanceSlug  string `json:"instance_slug"`
	InstanceHost  string `json:"instance_host"`
}

type HostedSSOClient struct {
	ControlURL  string
	ClientID    string
	CallbackURL string
	HTTPClient  *http.Client
	Sleep       func(context.Context, time.Duration) error
	Now         func() time.Time
}

type ExchangeError struct {
	Status   int
	Category string
}

func (e *ExchangeError) Error() string {
	return fmt.Sprintf("hosted token exchange failed: status=%d category=%s", e.Status, e.Category)
}

func NewHostedSSOClient(controlURL, clientID, callbackURL string) *HostedSSOClient {
	return &HostedSSOClient{
		ControlURL:  controlURL,
		ClientID:    clientID,
		CallbackURL: callbackURL,
		HTTPClient: &http.Client{
			Timeout:       5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Sleep: sleepContext,
		Now:   time.Now,
	}
}

func GenerateStateAndPKCE(random io.Reader) (state, verifier, challenge string, err error) {
	if random == nil {
		random = rand.Reader
	}
	stateBytes := make([]byte, 32)
	verifierBytes := make([]byte, 32)
	if _, err = io.ReadFull(random, stateBytes); err != nil {
		return "", "", "", err
	}
	if _, err = io.ReadFull(random, verifierBytes); err != nil {
		return "", "", "", err
	}
	state = base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)
	digest := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	return state, verifier, challenge, nil
}

func (c *HostedSSOClient) AuthorizationURL(state, challenge string) (string, error) {
	if !isCanonical32ByteValue(state) || !isCanonical32ByteValue(challenge) {
		return "", errors.New("state and challenge must be canonical 32-byte base64url values")
	}
	u, err := url.Parse(c.ControlURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid hosted control URL")
	}
	u.Path = "/sso/authorize"
	u.RawPath = ""
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", c.CallbackURL)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	result := u.String()
	if len(result) > 2048 {
		return "", errors.New("hosted authorization URL exceeds 2048 bytes")
	}
	return result, nil
}

func (c *HostedSSOClient) TokenURL() (string, error) {
	u, err := url.Parse(c.ControlURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid hosted control URL")
	}
	u.Path = "/sso/token"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	result := u.String()
	if len(result) > 2048 {
		return "", errors.New("hosted token URL exceeds 2048 bytes")
	}
	return result, nil
}

func SignHostedSession(claims SessionClaims, key []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("hosted session key must be 32 bytes")
	}
	if err := validateSessionClaims(claims, "", time.Time{}, false); err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signature := purposeMAC(key, hostedSessionDomain, payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(token) > HostedTokenMaxBytes {
		return "", errors.New("hosted session token exceeds 2048 bytes")
	}
	return token, nil
}

func VerifyHostedSession(token string, key []byte, instanceID string, now time.Time) (*SessionClaims, error) {
	if len(key) != 32 || len(token) == 0 || len(token) > HostedTokenMaxBytes {
		return nil, ErrInvalidToken
	}
	if strings.Count(token, ".") != 1 {
		return nil, ErrInvalidToken
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts[0]) == 0 || len(parts[0]) > 2004 || len(parts[1]) != 43 {
		return nil, ErrInvalidToken
	}
	payload, err := decodeCanonicalBase64(parts[0])
	if err != nil || len(payload) == 0 || !utf8.Valid(payload) {
		return nil, ErrInvalidToken
	}
	signature, err := decodeCanonicalBase64(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return nil, ErrInvalidToken
	}
	expected := purposeMAC(key, hostedSessionDomain, payload)
	if subtle.ConstantTimeCompare(signature, expected) != 1 {
		return nil, ErrInvalidToken
	}
	allowed := map[string]struct{}{"v": {}, "sub": {}, "email": {}, "display": {}, "instance_id": {}, "auth_source": {}, "iat": {}, "exp": {}}
	if _, err := decodeStrictJSONObject(payload, allowed); err != nil {
		return nil, ErrInvalidToken
	}
	var claims SessionClaims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return nil, ErrInvalidToken
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrInvalidToken
	}
	canonical, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(canonical, payload) {
		return nil, ErrInvalidToken
	}
	if err := validateSessionClaims(claims, instanceID, now, true); err != nil {
		if errors.Is(err, ErrExpiredToken) {
			return nil, err
		}
		return nil, ErrInvalidToken
	}
	return &claims, nil
}

func validateSessionClaims(claims SessionClaims, instanceID string, now time.Time, checkTime bool) error {
	if claims.Version != 1 || claims.AuthSource != HostedAuthSource {
		return ErrInvalidToken
	}
	for _, field := range []struct {
		value string
		max   int
	}{{claims.Subject, 128}, {claims.Email, 320}, {claims.Display, 320}, {claims.InstanceID, 128}} {
		if err := validateHostedString(field.value, field.max, true); err != nil {
			return ErrInvalidToken
		}
	}
	if claims.Display != claims.Email || (instanceID != "" && claims.InstanceID != instanceID) {
		return ErrInvalidToken
	}
	if claims.IssuedAt > int64(^uint64(0)>>1)-3600 || claims.ExpiresAt != claims.IssuedAt+3600 {
		return ErrInvalidToken
	}
	if checkTime {
		nowUnix := now.Unix()
		if claims.IssuedAt > nowUnix {
			return ErrInvalidToken
		}
		if nowUnix >= claims.ExpiresAt {
			return ErrExpiredToken
		}
	}
	return nil
}

func SignBrowserBinding(nonce string, key []byte) (string, error) {
	if len(key) != 32 || !isCanonical32ByteValue(nonce) {
		return "", errors.New("invalid browser binding input")
	}
	signature := purposeMAC(key, browserBindingDomain, []byte(nonce))
	return nonce + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func VerifyBrowserBinding(value string, key []byte) (string, error) {
	if len(key) != 32 || len(value) != 87 || value[43] != '.' || strings.Count(value, ".") != 1 {
		return "", ErrInvalidToken
	}
	nonce := value[:43]
	if !isCanonical32ByteValue(nonce) {
		return "", ErrInvalidToken
	}
	signature, err := decodeCanonicalBase64(value[44:])
	if err != nil || len(signature) != sha256.Size {
		return "", ErrInvalidToken
	}
	expected := purposeMAC(key, browserBindingDomain, []byte(nonce))
	if subtle.ConstantTimeCompare(signature, expected) != 1 {
		return "", ErrInvalidToken
	}
	return nonce, nil
}

func purposeMAC(key, domain, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(domain)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func SanitizeDestination(destination string) string {
	if destination == "" || len(destination) > 4096 || !utf8.ValidString(destination) || hasASCIIControl(destination) || !strings.HasPrefix(destination, "/") || strings.HasPrefix(destination, "//") {
		return "/"
	}
	u, err := url.ParseRequestURI(destination)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" {
		return "/"
	}
	escapedPath := u.EscapedPath()
	if strings.Contains(escapedPath, "\\") || strings.Contains(u.Path, "\\") || hasASCIIControl(u.Path) || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return destination
}

func HostedSSOStartURL(destination string) string {
	destination = SanitizeDestination(destination)
	return "/auth/sso/start?next=" + base64.RawURLEncoding.EncodeToString([]byte(destination))
}

func DecodeHostedNext(rawQuery string, requestURILength int) (string, error) {
	if requestURILength > 8192 {
		return "", errors.New("request URI exceeds 8192 bytes")
	}
	if rawQuery == "" {
		return "/", nil
	}
	if strings.Contains(rawQuery, "&") || strings.Contains(rawQuery, ";") || strings.Count(rawQuery, "=") != 1 || !strings.HasPrefix(rawQuery, "next=") {
		return "", errors.New("invalid continuation query")
	}
	encoded := strings.TrimPrefix(rawQuery, "next=")
	if encoded == "" || len(encoded) > 5462 {
		return "", errors.New("invalid continuation length")
	}
	decoded, err := decodeCanonicalBase64(encoded)
	if err != nil || len(decoded) > 4096 || !utf8.Valid(decoded) {
		return "", errors.New("invalid continuation encoding")
	}
	safe := SanitizeDestination(string(decoded))
	return safe, nil
}

type HostedCallback struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
	IsError          bool
}

func ParseHostedCallback(rawQuery string, requestURILength int) (HostedCallback, error) {
	if requestURILength > 8192 || rawQuery == "" || strings.Contains(rawQuery, ";") {
		return HostedCallback{}, errors.New("invalid callback query")
	}
	parts := strings.Split(rawQuery, "&")
	values := make(map[string]string, len(parts))
	for _, component := range parts {
		if component == "" || strings.Count(component, "=") != 1 {
			return HostedCallback{}, errors.New("invalid callback component")
		}
		pair := strings.SplitN(component, "=", 2)
		switch pair[0] {
		case "code", "state", "error", "error_description":
		default:
			return HostedCallback{}, errors.New("unexpected callback parameter")
		}
		if _, exists := values[pair[0]]; exists {
			return HostedCallback{}, errors.New("duplicate callback parameter")
		}
		values[pair[0]] = pair[1]
	}
	state, hasState := values["state"]
	if !hasState || !isCanonical32ByteValue(state) {
		return HostedCallback{}, errors.New("invalid callback state")
	}
	code, hasCode := values["code"]
	errorRaw, hasError := values["error"]
	descriptionRaw, hasDescription := values["error_description"]
	if hasCode {
		if len(values) != 2 || hasError || hasDescription || !isCanonical32ByteValue(code) {
			return HostedCallback{}, errors.New("invalid callback success shape")
		}
		return HostedCallback{Code: code, State: state}, nil
	}
	if !hasError || len(values) < 2 || len(values) > 3 {
		return HostedCallback{}, errors.New("invalid callback error shape")
	}
	if len(errorRaw) == 0 || len(errorRaw) > 192 {
		return HostedCallback{}, errors.New("invalid encoded callback error")
	}
	decodedError, err := url.QueryUnescape(errorRaw)
	if err != nil || !validErrorCode(decodedError) {
		return HostedCallback{}, errors.New("invalid callback error")
	}
	decodedDescription := ""
	if hasDescription {
		if len(descriptionRaw) == 0 || len(descriptionRaw) > 768 {
			return HostedCallback{}, errors.New("invalid encoded callback description")
		}
		decodedDescription, err = url.QueryUnescape(descriptionRaw)
		if err != nil || validateHostedString(decodedDescription, 256, true) != nil {
			return HostedCallback{}, errors.New("invalid callback description")
		}
	}
	return HostedCallback{State: state, Error: decodedError, ErrorDescription: decodedDescription, IsError: true}, nil
}

type PendingTransaction struct {
	State        string
	BrowserNonce string
	Verifier     string
	Destination  string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	sequence     uint64
}

type PendingStore struct {
	mu             sync.Mutex
	entries        map[string]PendingTransaction
	browserCounts  map[string]int
	acceptedStarts []time.Time
	nextSequence   uint64
	now            func() time.Time
	cancel         context.CancelFunc
	done           chan struct{}
	closeOnce      sync.Once
	closed         bool
}

func NewPendingStore(parent context.Context, now func() time.Time) *PendingStore {
	ticker := time.NewTicker(time.Minute)
	return newPendingStore(parent, now, ticker.C, ticker.Stop)
}

func newPendingStore(parent context.Context, now func() time.Time, cleanupTicks <-chan time.Time, stopCleanup func()) *PendingStore {
	if parent == nil {
		parent = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(parent)
	s := &PendingStore{
		entries:       make(map[string]PendingTransaction),
		browserCounts: make(map[string]int),
		now:           now,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	go s.cleanup(ctx, cleanupTicks, stopCleanup)
	return s
}

func (s *PendingStore) Admit(browserNonce, destination string, generate func() (string, string, error)) (PendingTransaction, error) {
	if !isCanonical32ByteValue(browserNonce) || generate == nil {
		return PendingTransaction{}, errors.New("invalid pending transaction input")
	}
	destination = SanitizeDestination(destination)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.closed {
		return PendingTransaction{}, errors.New("pending SSO transaction store is closed")
	}
	s.pruneLocked(now)
	s.pruneRateLocked(now)
	if len(s.acceptedStarts) >= startsPerMinute {
		return PendingTransaction{}, ErrStartRateLimited
	}
	willEvict := s.browserCounts[browserNonce] >= pendingPerBrowser
	if len(s.entries) >= pendingGlobal && !willEvict {
		return PendingTransaction{}, ErrPendingFull
	}
	state, verifier, err := generate()
	if err != nil {
		return PendingTransaction{}, err
	}
	if !isCanonical32ByteValue(state) || !isCanonical32ByteValue(verifier) {
		return PendingTransaction{}, errors.New("generated state or verifier is invalid")
	}
	if _, exists := s.entries[state]; exists {
		return PendingTransaction{}, errors.New("generated duplicate state")
	}
	if willEvict {
		var oldest PendingTransaction
		found := false
		for _, existing := range s.entries {
			if existing.BrowserNonce == browserNonce && (!found || existing.CreatedAt.Before(oldest.CreatedAt) || (existing.CreatedAt.Equal(oldest.CreatedAt) && existing.sequence < oldest.sequence)) {
				oldest = existing
				found = true
			}
		}
		if found {
			s.removeLocked(oldest.State)
		}
	}
	s.nextSequence++
	tx := PendingTransaction{
		State:        state,
		BrowserNonce: browserNonce,
		Verifier:     verifier,
		Destination:  destination,
		CreatedAt:    now,
		ExpiresAt:    now.Add(pendingLifetime),
		sequence:     s.nextSequence,
	}
	s.entries[state] = tx
	s.browserCounts[browserNonce]++
	s.acceptedStarts = append(s.acceptedStarts, now)
	return tx, nil
}

func (s *PendingStore) Consume(state, browserNonce string) (PendingTransaction, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	tx, ok := s.entries[state]
	if !ok || tx.BrowserNonce != browserNonce {
		if ok {
			return PendingTransaction{}, false, s.browserCounts[tx.BrowserNonce] > 0
		}
		return PendingTransaction{}, false, s.browserCounts[browserNonce] > 0
	}
	s.removeLocked(state)
	return tx, true, s.browserCounts[browserNonce] > 0
}

func (s *PendingStore) Discard(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(state)
}

func (s *PendingStore) HasBrowser(browserNonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	return s.browserCounts[browserNonce] > 0
}

func (s *PendingStore) Prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	s.pruneRateLocked(now)
}

func (s *PendingStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	return len(s.entries)
}

func (s *PendingStore) BrowserCount(browserNonce string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	return s.browserCounts[browserNonce]
}

func (s *PendingStore) ForEach(fn func(PendingTransaction)) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	copyEntries := make([]PendingTransaction, 0, len(s.entries))
	for _, tx := range s.entries {
		copyEntries = append(copyEntries, tx)
	}
	s.mu.Unlock()
	for _, tx := range copyEntries {
		fn(tx)
	}
}

func (s *PendingStore) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
	})
}

func (s *PendingStore) cleanup(ctx context.Context, ticks <-chan time.Time, stop func()) {
	defer func() {
		if stop != nil {
			stop()
		}
		s.mu.Lock()
		s.closed = true
		for state := range s.entries {
			s.removeLocked(state)
		}
		s.mu.Unlock()
		close(s.done)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.Prune()
		}
	}
}

func (s *PendingStore) pruneLocked(now time.Time) {
	for state, tx := range s.entries {
		if !now.Before(tx.ExpiresAt) {
			s.removeLocked(state)
		}
	}
}

func (s *PendingStore) pruneRateLocked(now time.Time) {
	cutoff := now.Add(-time.Minute)
	idx := 0
	for idx < len(s.acceptedStarts) && !s.acceptedStarts[idx].After(cutoff) {
		idx++
	}
	if idx > 0 {
		s.acceptedStarts = append([]time.Time(nil), s.acceptedStarts[idx:]...)
	}
}

func (s *PendingStore) removeLocked(state string) bool {
	tx, ok := s.entries[state]
	if !ok {
		return false
	}
	delete(s.entries, state)
	count := s.browserCounts[tx.BrowserNonce] - 1
	if count <= 0 {
		delete(s.browserCounts, tx.BrowserNonce)
	} else {
		s.browserCounts[tx.BrowserNonce] = count
	}
	return true
}

func (c *HostedSSOClient) Exchange(parent context.Context, code, verifier string) (*HostedIdentity, error) {
	if !isCanonical32ByteValue(code) || !isCanonical32ByteValue(verifier) {
		return nil, &ExchangeError{Category: "invalid_request"}
	}
	tokenURL, err := c.TokenURL()
	if err != nil {
		return nil, &ExchangeError{Category: "client_configuration_error"}
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	exchangeDeadline := now().Add(12 * time.Second)
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	baseClient := c.HTTPClient
	if baseClient == nil {
		baseClient = NewHostedSSOClient(c.ControlURL, c.ClientID, c.CallbackURL).HTTPClient
	}
	clientCopy := *baseClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := &clientCopy
	sleeper := c.Sleep
	if sleeper == nil {
		sleeper = sleepContext
	}
	for attempt := 1; attempt <= 3; attempt++ {
		form := url.Values{}
		form.Set("grant_type", "authorization_code")
		form.Set("code", code)
		form.Set("client_id", c.ClientID)
		form.Set("redirect_uri", c.CallbackURL)
		form.Set("code_verifier", verifier)
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 5*time.Second)
		req, reqErr := http.NewRequestWithContext(attemptCtx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
		if reqErr != nil {
			attemptCancel()
			return nil, &ExchangeError{Category: "client_request_error"}
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, doErr := client.Do(req)
		if doErr != nil {
			attemptCancel()
			return nil, &ExchangeError{Category: "network_error"}
		}
		body, readErr := readBoundedBody(resp.Body, providerBodyMaxBytes)
		attemptCancel()
		if readErr != nil {
			return nil, &ExchangeError{Status: resp.StatusCode, Category: "malformed_provider_response"}
		}
		if err := validateJSONContentType(resp.Header); err != nil {
			return nil, &ExchangeError{Status: resp.StatusCode, Category: "invalid_content_type"}
		}
		if resp.StatusCode == http.StatusOK {
			identity, identityErr := decodeHostedIdentity(body, c.ClientID)
			if identityErr != nil {
				return nil, &ExchangeError{Status: resp.StatusCode, Category: "invalid_identity"}
			}
			return identity, nil
		}
		category := decodeProviderErrorCategory(body)
		if resp.StatusCode != http.StatusTooManyRequests || category != "slow_down" || attempt == 3 {
			return nil, &ExchangeError{Status: resp.StatusCode, Category: category}
		}
		delay := retryDelay(resp.Header.Get("Retry-After"))
		remaining := exchangeDeadline.Sub(now())
		if contextDeadline, ok := ctx.Deadline(); ok {
			if contextRemaining := time.Until(contextDeadline); contextRemaining < remaining {
				remaining = contextRemaining
			}
		}
		if delay >= remaining {
			return nil, &ExchangeError{Status: resp.StatusCode, Category: category}
		}
		if err := sleeper(ctx, delay); err != nil || ctx.Err() != nil {
			return nil, &ExchangeError{Status: resp.StatusCode, Category: category}
		}
	}
	return nil, &ExchangeError{Category: "exchange_failed"}
}

func decodeHostedIdentity(body []byte, expectedInstanceID string) (*HostedIdentity, error) {
	allowed := map[string]struct{}{"sub": {}, "email": {}, "email_verified": {}, "instance_id": {}, "instance_slug": {}, "instance_host": {}}
	members, err := decodeStrictJSONObject(body, allowed)
	if err != nil {
		return nil, err
	}
	var identity HostedIdentity
	if err := decodeRequiredString(members, "sub", &identity.Subject); err != nil {
		return nil, err
	}
	if err := decodeRequiredString(members, "email", &identity.Email); err != nil {
		return nil, err
	}
	if err := decodeRequiredString(members, "instance_id", &identity.InstanceID); err != nil {
		return nil, err
	}
	verifiedRaw, ok := members["email_verified"]
	if !ok || bytes.Equal(bytes.TrimSpace(verifiedRaw), []byte("null")) || json.Unmarshal(verifiedRaw, &identity.EmailVerified) != nil || !identity.EmailVerified {
		return nil, errors.New("email_verified must be present and true")
	}
	if raw, ok := members["instance_slug"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &identity.InstanceSlug) != nil {
			return nil, errors.New("invalid instance_slug")
		}
	}
	if raw, ok := members["instance_host"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &identity.InstanceHost) != nil {
			return nil, errors.New("invalid instance_host")
		}
	}
	if err := validateHostedString(identity.Subject, 128, true); err != nil {
		return nil, err
	}
	if err := validateHostedString(identity.Email, 320, true); err != nil {
		return nil, err
	}
	if err := validateHostedString(identity.InstanceID, 128, true); err != nil || identity.InstanceID != expectedInstanceID {
		return nil, errors.New("identity instance mismatch")
	}
	if err := validateHostedString(identity.InstanceSlug, 63, false); err != nil {
		return nil, err
	}
	if err := validateHostedString(identity.InstanceHost, 253, false); err != nil {
		return nil, err
	}
	return &identity, nil
}

func decodeProviderErrorCategory(body []byte) string {
	allowed := map[string]struct{}{"error": {}, "error_description": {}}
	members, err := decodeStrictJSONObject(body, allowed)
	if err != nil {
		return "malformed_provider_error"
	}
	var code string
	if err := decodeRequiredString(members, "error", &code); err != nil || !validErrorCode(code) {
		return "malformed_provider_error"
	}
	if raw, ok := members["error_description"]; ok {
		var description string
		if err := json.Unmarshal(raw, &description); err != nil || validateHostedString(description, 256, true) != nil {
			return "malformed_provider_error"
		}
	}
	switch code {
	case "invalid_request", "invalid_grant", "slow_down", "server_error", "access_denied":
		return code
	default:
		return "unknown_provider_error"
	}
}

func decodeStrictJSONObject(body []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	if len(body) == 0 || len(body) > providerBodyMaxBytes || !utf8.Valid(body) {
		return nil, errors.New("invalid JSON body")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("JSON body must be an object")
	}
	members := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("invalid JSON object key")
		}
		if _, exists := members[key]; exists {
			return nil, errors.New("duplicate JSON object key")
		}
		if _, ok := allowed[key]; !ok {
			return nil, errors.New("unknown JSON object key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		members[key] = raw
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("invalid JSON object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return members, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeRequiredString(members map[string]json.RawMessage, name string, target *string) error {
	raw, ok := members[name]
	if !ok || json.Unmarshal(raw, target) != nil || *target == "" {
		return fmt.Errorf("missing or invalid %s", name)
	}
	return nil
}

func validateHostedString(value string, max int, required bool) error {
	if required && value == "" {
		return errors.New("required string is empty")
	}
	if len(value) > max || !utf8.ValidString(value) || hasASCIIControl(value) {
		return errors.New("invalid hosted string")
	}
	return nil
}

func hasASCIIControl(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	return false
}

func validErrorCode(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for i := range value {
		b := value[i]
		if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '.' || b == '_' || b == '-') {
			return false
		}
	}
	return true
}

func isCanonical32ByteValue(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := decodeCanonicalBase64(value)
	return err == nil && len(decoded) == 32
}

func decodeCanonicalBase64(value string) ([]byte, error) {
	if strings.Contains(value, "=") {
		return nil, errors.New("padded base64url is not canonical")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("noncanonical base64url")
	}
	return decoded, nil
}

func readBoundedBody(body io.ReadCloser, limit int64) ([]byte, error) {
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("provider response exceeds limit")
	}
	return data, nil
}

func validateJSONContentType(header http.Header) error {
	values := header.Values("Content-Type")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return errors.New("exactly one Content-Type is required")
	}
	value := values[0]
	if strings.TrimSpace(value) == "" || strings.Count(value, ";") > 1 || strings.HasSuffix(strings.TrimSpace(value), ";") {
		return errors.New("invalid Content-Type shape")
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	if len(params) == 0 {
		return nil
	}
	charset, ok := params["charset"]
	if len(params) != 1 || !ok || !strings.EqualFold(charset, "utf-8") {
		return errors.New("unsupported Content-Type parameter")
	}
	return nil
}

func retryDelay(value string) time.Duration {
	validDigits := value != ""
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			validDigits = false
			break
		}
	}
	seconds, err := strconv.Atoi(value)
	if !validDigits || err != nil || seconds <= 0 {
		seconds = 1
	}
	if seconds > 3 {
		seconds = 3
	}
	return time.Duration(seconds) * time.Second
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
