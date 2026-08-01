package customauth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

type Config struct {
	Enabled                 bool              `json:"enabled"`
	RefreshURL              string            `json:"refresh_url,omitempty"`
	PKCE                    bool              `json:"pkce"`
	TokenRequestFormat      string            `json:"token_request_format,omitempty"`
	AccessTokenField        string            `json:"access_token_field,omitempty"`
	RefreshTokenField       string            `json:"refresh_token_field,omitempty"`
	ExpiresInField          string            `json:"expires_in_field,omitempty"`
	AuthorizationMode       string            `json:"authorization_mode,omitempty"`
	AccessTokenHeader       string            `json:"access_token_header,omitempty"`
	AccessTokenPrefix       string            `json:"access_token_prefix,omitempty"`
	UserAgent               string            `json:"user_agent,omitempty"`
	StaticHeaders           map[string]string `json:"static_headers,omitempty"`
	ProfileURL              string            `json:"profile_url,omitempty"`
	ProfileInstancePath     string            `json:"profile_instance_path,omitempty"`
	ProfileTeamPath         string            `json:"profile_team_path,omitempty"`
	InstanceHeader          string            `json:"instance_header,omitempty"`
	TeamHeader              string            `json:"team_header,omitempty"`
	SigningSecret           string            `json:"signing_secret,omitempty"`
	TimestampHeader         string            `json:"timestamp_header,omitempty"`
	SignatureHeader         string            `json:"signature_header,omitempty"`
	ModelsArrayPath         string            `json:"models_array_path,omitempty"`
	ModelIDField            string            `json:"model_id_field,omitempty"`
	AuthorizationParameters map[string]string `json:"authorization_parameters,omitempty"`
	StandardTokenFields     bool              `json:"standard_token_fields"`
	CallbackParameter       string            `json:"callback_parameter,omitempty"`
	LocalCallbackHost       string            `json:"local_callback_host,omitempty"`
	LocalCallbackPath       string            `json:"local_callback_path,omitempty"`
	AllowPrivateEndpoints   bool              `json:"allow_private_endpoints"`
	TokenHeaders            map[string]string `json:"token_headers,omitempty"`
	RefreshRequestFormat    string            `json:"refresh_request_format,omitempty"`
	RefreshParameters       map[string]string `json:"refresh_parameters,omitempty"`
	RefreshHeaders          map[string]string `json:"refresh_headers,omitempty"`
	RefreshIncludeGrantType bool              `json:"refresh_include_grant_type"`
	RefreshIncludeClient    bool              `json:"refresh_include_client"`
}

type State struct {
	InstanceID string `json:"instance_id,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
}

type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

type RefreshOptions struct {
	ClientID     string
	ClientSecret string
}

type RefreshLeaseStore interface {
	TryAcquireOAuthRefreshLease(ctx context.Context, configID, ownerToken string, now time.Time, leaseDuration time.Duration) (bool, error)
	ReleaseOAuthRefreshLease(ctx context.Context, configID, ownerToken string) error
}

type RefreshObserver func() (tokens TokenSet, completed bool, err error)

var refreshGroup singleflight.Group

const MaxMetadataResponseBytes int64 = 1 << 20

func ParseConfig(raw string) (Config, error) {
	var cfg Config
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid custom authentication configuration: %w", err)
	}
	cfg.applyDefaults()
	return cfg, nil
}

func ParseState(raw string) (State, error) {
	var state State
	if strings.TrimSpace(raw) == "" {
		return state, nil
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return State{}, fmt.Errorf("invalid custom authentication state: %w", err)
	}
	return state, nil
}

func MarshalConfig(cfg Config) string {
	cfg.applyDefaults()
	data, _ := json.Marshal(cfg)
	return string(data)
}

func MarshalState(state State) string {
	data, _ := json.Marshal(state)
	return string(data)
}

func RedactedConfigJSON(raw string) string {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return ""
	}
	cfg.SigningSecret = ""
	cfg.StaticHeaders = nil
	cfg.TokenHeaders = nil
	cfg.RefreshHeaders = nil
	cfg.RefreshParameters = nil
	return MarshalConfig(cfg)
}

func ValidateAuthorizationParameters(cfg Config) error {
	cfg.applyDefaults()
	protocolParameters := map[string]struct{}{
		"state": {}, "client_id": {}, "scope": {}, "response_type": {},
		"code_challenge": {}, "code_challenge_method": {},
	}
	protected := map[string]struct{}{
		"state": {}, "client_id": {}, "scope": {}, "response_type": {},
		"code_challenge": {}, "code_challenge_method": {},
		"redirect_uri": {}, "callback_uri": {},
	}
	callbackParameter := strings.ToLower(strings.TrimSpace(cfg.CallbackParameter))
	if _, reserved := protocolParameters[callbackParameter]; reserved {
		return fmt.Errorf("callback parameter %q conflicts with a reserved OAuth parameter", cfg.CallbackParameter)
	}
	protected[callbackParameter] = struct{}{}
	for key := range cfg.AuthorizationParameters {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if _, reserved := protected[normalized]; reserved {
			return fmt.Errorf("authorization parameter %q is reserved and must use its dedicated setting", key)
		}
	}
	return nil
}

func ValidateHeaders(cfg Config) error {
	cfg.applyDefaults()
	for label, name := range map[string]string{
		"access token header": cfg.AccessTokenHeader,
		"instance header":     cfg.InstanceHeader,
		"team header":         cfg.TeamHeader,
		"timestamp header":    cfg.TimestampHeader,
		"signature header":    cfg.SignatureHeader,
	} {
		if !validHeaderName(name) {
			return fmt.Errorf("%s %q is not a valid HTTP header name", label, name)
		}
	}
	for label, value := range map[string]string{
		"access token prefix": cfg.AccessTokenPrefix,
		"user agent":          cfg.UserAgent,
	} {
		if !validHeaderValue(value) {
			return fmt.Errorf("%s contains invalid HTTP header characters", label)
		}
	}
	for label, headers := range map[string]map[string]string{
		"additional request header": cfg.StaticHeaders,
		"token endpoint header":     cfg.TokenHeaders,
		"refresh endpoint header":   cfg.RefreshHeaders,
	} {
		for name, value := range headers {
			if !validHeaderName(name) {
				return fmt.Errorf("%s %q is not a valid HTTP header name", label, name)
			}
			if !validHeaderValue(value) {
				return fmt.Errorf("%s %q contains invalid HTTP header characters", label, name)
			}
		}
	}
	return nil
}

// ValidateRequestHeaderValues validates values obtained at runtime before they
// are placed into request headers. Provider responses are untrusted input and
// therefore need the same control-character checks as configured values.
func ValidateRequestHeaderValues(cfg Config, state State, accessToken string) error {
	cfg.applyDefaults()
	if accessToken != "" {
		value := authorizationValue(cfg.AuthorizationMode, accessToken)
		if cfg.AccessTokenPrefix != "" {
			value = cfg.AccessTokenPrefix + accessToken
		}
		if !validHeaderValue(value) {
			return fmt.Errorf("access token contains invalid HTTP header characters")
		}
	}
	for label, value := range map[string]string{
		"instance ID": state.InstanceID,
		"team ID":     state.TeamID,
	} {
		if !validHeaderValue(value) {
			return fmt.Errorf("%s contains invalid HTTP header characters", label)
		}
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == '\t' {
			continue
		}
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

func (cfg *Config) applyDefaults() {
	if cfg.TokenRequestFormat == "" {
		cfg.TokenRequestFormat = "json"
	}
	if cfg.AccessTokenField == "" {
		cfg.AccessTokenField = "access_token"
	}
	if cfg.RefreshTokenField == "" {
		cfg.RefreshTokenField = "refresh_token"
	}
	if cfg.ExpiresInField == "" {
		cfg.ExpiresInField = "expires_in"
	}
	if cfg.AuthorizationMode == "" {
		cfg.AuthorizationMode = "bearer"
	}
	if cfg.AccessTokenHeader == "" {
		cfg.AccessTokenHeader = "Authorization"
	}
	if cfg.TimestampHeader == "" {
		cfg.TimestampHeader = "x-request-timestamp"
	}
	if cfg.SignatureHeader == "" {
		cfg.SignatureHeader = "x-request-signature"
	}
	if cfg.InstanceHeader == "" {
		cfg.InstanceHeader = "x-instance-id"
	}
	if cfg.TeamHeader == "" {
		cfg.TeamHeader = "x-team-id"
	}
	if cfg.ModelsArrayPath == "" {
		cfg.ModelsArrayPath = "data"
	}
	if cfg.ModelIDField == "" {
		cfg.ModelIDField = "id"
	}
	if cfg.CallbackParameter == "" {
		cfg.CallbackParameter = "callback_uri"
	}
	if cfg.LocalCallbackHost == "" {
		cfg.LocalCallbackHost = "localhost"
	}
	if cfg.LocalCallbackPath == "" {
		cfg.LocalCallbackPath = "/callback"
	}
	if cfg.RefreshRequestFormat == "" {
		cfg.RefreshRequestFormat = cfg.TokenRequestFormat
	}
}

func PrepareRequest(req *http.Request, exactBody []byte, cfg Config, state State, accessToken string) error {
	cfg.applyDefaults()
	if err := ValidateHeaders(cfg); err != nil {
		return err
	}
	if err := ValidateRequestHeaderValues(cfg, state, accessToken); err != nil {
		return err
	}
	for name, value := range cfg.StaticHeaders {
		if strings.TrimSpace(name) != "" {
			req.Header.Set(name, value)
		}
	}
	if cfg.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		value := authorizationValue(cfg.AuthorizationMode, accessToken)
		if cfg.AccessTokenPrefix != "" {
			value = cfg.AccessTokenPrefix + accessToken
		}
		req.Header.Set(cfg.AccessTokenHeader, value)
	}
	if state.InstanceID != "" {
		req.Header.Set(cfg.InstanceHeader, state.InstanceID)
	}
	if state.TeamID != "" {
		req.Header.Set(cfg.TeamHeader, state.TeamID)
	}
	if cfg.SigningSecret == "" {
		return nil
	}
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	bodyHash := sha256.Sum256(exactBody)
	canonical := strings.ToUpper(req.Method) + "\n" + req.URL.EscapedPath() + "\n" + timestamp + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(cfg.SigningSecret))
	_, _ = mac.Write([]byte(canonical))
	req.Header.Set(cfg.TimestampHeader, timestamp)
	req.Header.Set(cfg.SignatureHeader, hex.EncodeToString(mac.Sum(nil)))
	return nil
}

func authorizationValue(mode, token string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "api_key", "apikey":
		return "Apikey " + token
	case "raw":
		return token
	case "auto":
		if isJWT(token) {
			return "Bearer " + token
		}
		return "Apikey " + token
	default:
		return "Bearer " + token
	}
}

func isJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]any
	return json.Unmarshal(payload, &claims) == nil
}

func DecodeTokenResponse(body io.Reader, cfg Config, previousRefreshToken string) (TokenSet, error) {
	cfg.applyDefaults()
	var payload any
	if err := DecodeMetadataJSON(body, &payload, "OAuth token response"); err != nil {
		return TokenSet{}, err
	}
	accessToken := stringValueAt(payload, cfg.AccessTokenField)
	if accessToken == "" {
		return TokenSet{}, fmt.Errorf("OAuth token response did not include %q", cfg.AccessTokenField)
	}
	refreshToken := stringValueAt(payload, cfg.RefreshTokenField)
	if refreshToken == "" {
		refreshToken = previousRefreshToken
	}
	if err := ValidateRequestHeaderValues(cfg, State{}, accessToken); err != nil {
		return TokenSet{}, err
	}
	expiresIn := int64ValueAt(payload, cfg.ExpiresInField)
	var expiresAt int64
	if expiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli()
	} else if exp := jwtExpiryMillis(accessToken); exp > 0 {
		expiresAt = exp
	}
	return TokenSet{AccessToken: accessToken, RefreshToken: refreshToken, ExpiresAt: expiresAt}, nil
}

func DecodeMetadataJSON(body io.Reader, target any, label string) error {
	data, err := io.ReadAll(io.LimitReader(body, MaxMetadataResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(data)) > MaxMetadataResponseBytes {
		return fmt.Errorf("%s exceeded the %d-byte response limit", label, MaxMetadataResponseBytes)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	return nil
}

// CoordinatedRefresh serializes refresh-token use across custom-provider
// inference and discovery paths within this process.
func CoordinatedRefresh(key string, refresh func() (TokenSet, error)) (TokenSet, error) {
	value, err, _ := refreshGroup.Do(strings.TrimSpace(key), func() (any, error) {
		return refresh()
	})
	if err != nil {
		return TokenSet{}, err
	}
	tokens, ok := value.(TokenSet)
	if !ok {
		return TokenSet{}, fmt.Errorf("custom OAuth refresh returned an invalid result")
	}
	return tokens, nil
}

// CoordinatedRefreshDistributed combines in-process singleflight with a
// database-backed lease so separate OpenVibely processes cannot rotate the same
// refresh token concurrently.
func CoordinatedRefreshDistributed(
	ctx context.Context,
	key string,
	store RefreshLeaseStore,
	configID string,
	observe RefreshObserver,
	refresh func() (TokenSet, error),
) (TokenSet, error) {
	return CoordinatedRefresh(key, func() (TokenSet, error) {
		if store == nil {
			return refresh()
		}
		owner, err := newRefreshLeaseOwner()
		if err != nil {
			return TokenSet{}, err
		}
		const leaseDuration = 45 * time.Second
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if observe != nil {
				tokens, completed, err := observe()
				if err != nil {
					return TokenSet{}, err
				}
				if completed {
					return tokens, nil
				}
			}
			acquired, err := store.TryAcquireOAuthRefreshLease(ctx, configID, owner, time.Now(), leaseDuration)
			if err != nil {
				return TokenSet{}, err
			}
			if acquired {
				defer func() {
					releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = store.ReleaseOAuthRefreshLease(releaseCtx, configID, owner)
				}()
				return refresh()
			}
			select {
			case <-ctx.Done():
				return TokenSet{}, ctx.Err()
			case <-ticker.C:
			}
		}
	})
}

func newRefreshLeaseOwner() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate OAuth refresh lease owner: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// ValidateEndpoint enforces the trust boundary for server-side custom-provider
// requests. Private/local destinations require an explicit opt-in.
func PrivateEndpointPolicyEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS")))
	return err == nil && enabled
}

func ValidateEndpoint(raw string, requestPrivate bool) (*url.URL, error) {
	allowPrivate := requestPrivate && PrivateEndpointPolicyEnabled()
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if u.Scheme != "https" && !(allowPrivate && u.Scheme == "http") {
		if allowPrivate {
			return nil, fmt.Errorf("endpoint URL must use https or http")
		}
		return nil, fmt.Errorf("endpoint URL must use https")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("endpoint URL must include a host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("endpoint URL must not include credentials")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isUnsafeDestinationIP(ip) && !allowPrivate {
		return nil, fmt.Errorf("endpoint URL resolves to a private or local address")
	}
	if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "localhost") && !allowPrivate {
		return nil, fmt.Errorf("endpoint URL uses localhost; enable private endpoints to allow it")
	}
	if u.Scheme == "http" {
		if !allowPrivate {
			return nil, fmt.Errorf("endpoint URL must use https")
		}
		ips, resolveErr := resolveEndpointIPs(u.Hostname())
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve HTTP endpoint host: %w", resolveErr)
		}
		for _, ip := range ips {
			if !isUnsafeDestinationIP(ip) {
				return nil, fmt.Errorf("public endpoint URLs must use https")
			}
		}
	}
	return u, nil
}

// NewHTTPClient returns a client that re-checks resolved addresses at dial time
// and validates redirects, preventing DNS and redirect bypasses.
func NewHTTPClient(timeout time.Duration, requestPrivate bool) *http.Client {
	allowPrivate := requestPrivate && PrivateEndpointPolicyEnabled()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, resolved := range ips {
			if isUnsafeDestinationIP(resolved.IP) && !allowPrivate {
				return nil, fmt.Errorf("custom provider endpoint %q resolved to a private or local address", host)
			}
			if scheme, _ := ctx.Value(endpointSchemeContextKey{}).(string); scheme == "http" && !isUnsafeDestinationIP(resolved.IP) {
				return nil, fmt.Errorf("public custom provider endpoint %q must use https", host)
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("custom provider endpoint %q did not resolve", host)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: endpointGuardTransport{base: transport, allowPrivate: allowPrivate},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func isUnsafeDestinationIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		!ip.IsGlobalUnicast() || isSpecialUseIPv4(ip)
}

type endpointSchemeContextKey struct{}

type endpointGuardTransport struct {
	base         *http.Transport
	allowPrivate bool
}

func (t endpointGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := ValidateEndpoint(req.URL.String(), t.allowPrivate); err != nil {
		return nil, err
	}
	ctx := context.WithValue(req.Context(), endpointSchemeContextKey{}, strings.ToLower(req.URL.Scheme))
	return t.base.RoundTrip(req.Clone(ctx))
}

func resolveEndpointIPs(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("host did not resolve")
	}
	ips := make([]net.IP, 0, len(resolved))
	for _, address := range resolved {
		ips = append(ips, address.IP)
	}
	return ips, nil
}

func isSpecialUseIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return (v4[0] == 100 && v4[1]&0xc0 == 64) ||
		(v4[0] == 192 && v4[1] == 0 && v4[2] == 0) ||
		(v4[0] == 192 && v4[1] == 0 && v4[2] == 2) ||
		(v4[0] == 198 && (v4[1] == 18 || v4[1] == 19)) ||
		(v4[0] == 198 && v4[1] == 51 && v4[2] == 100) ||
		(v4[0] == 203 && v4[1] == 0 && v4[2] == 113) ||
		v4[0] >= 240
}

func Refresh(ctx context.Context, client *http.Client, cfg Config, refreshToken string, options ...RefreshOptions) (TokenSet, error) {
	if err := ValidateHeaders(cfg); err != nil {
		return TokenSet{}, err
	}
	if strings.TrimSpace(cfg.RefreshURL) == "" {
		return TokenSet{}, fmt.Errorf("custom OAuth refresh URL is not configured")
	}
	if _, err := ValidateEndpoint(cfg.RefreshURL, cfg.AllowPrivateEndpoints); err != nil {
		return TokenSet{}, fmt.Errorf("invalid custom OAuth refresh URL: %w", err)
	}
	fields := make(map[string]string, len(cfg.RefreshParameters)+4)
	for key, value := range cfg.RefreshParameters {
		fields[key] = value
	}
	fields["refresh_token"] = refreshToken
	if cfg.RefreshIncludeGrantType {
		fields["grant_type"] = "refresh_token"
	}
	var refreshOptions RefreshOptions
	if len(options) > 0 {
		refreshOptions = options[0]
	}
	if cfg.RefreshIncludeClient {
		fields["client_id"] = refreshOptions.ClientID
		fields["client_secret"] = refreshOptions.ClientSecret
	}
	body, contentType, err := encodeFields(cfg.RefreshRequestFormat, fields)
	if err != nil {
		return TokenSet{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.RefreshURL, bytes.NewReader(body))
	if err != nil {
		return TokenSet{}, err
	}
	req.Header.Set("Content-Type", contentType)
	if cfg.UserAgent != "" {
		req.Header.Set("User-Agent", cfg.UserAgent)
	}
	for name, value := range cfg.TokenHeaders {
		if strings.TrimSpace(name) != "" {
			req.Header.Set(name, value)
		}
	}
	for name, value := range cfg.RefreshHeaders {
		if strings.TrimSpace(name) != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return TokenSet{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return TokenSet{}, fmt.Errorf("custom OAuth refresh returned %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return DecodeTokenResponse(resp.Body, cfg, refreshToken)
}

func EncodeTokenExchange(cfg Config, fields map[string]string) ([]byte, string, error) {
	return encodeFields(cfg.TokenRequestFormat, fields)
}

func encodeFields(format string, fields map[string]string) ([]byte, string, error) {
	if strings.EqualFold(strings.TrimSpace(format), "form") {
		values := make([]string, 0, len(fields))
		for key, value := range fields {
			if value != "" {
				values = append(values, urlQueryEscape(key)+"="+urlQueryEscape(value))
			}
		}
		return []byte(strings.Join(values, "&")), "application/x-www-form-urlencoded", nil
	}
	body, err := json.Marshal(fields)
	return body, "application/json", err
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

func ExtractState(payload any, cfg Config) State {
	return State{
		InstanceID: stringValueAt(payload, cfg.ProfileInstancePath),
		TeamID:     stringValueAt(payload, cfg.ProfileTeamPath),
	}
}

func ExtractModelIDs(payload any, cfg Config) []string {
	cfg.applyDefaults()
	value := valueAt(payload, cfg.ModelsArrayPath)
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		id, isString := item.(string)
		if !isString {
			id = stringValueAt(item, cfg.ModelIDField)
		}
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func valueAt(payload any, path string) any {
	current := payload
	for _, part := range strings.Split(strings.TrimSpace(path), ".") {
		if part == "" {
			continue
		}
		if index, err := strconv.Atoi(part); err == nil {
			items, ok := current.([]any)
			if !ok || index < 0 || index >= len(items) {
				return nil
			}
			current = items[index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[part]
	}
	return current
}

func stringValueAt(payload any, path string) string {
	value := valueAt(payload, path)
	text, _ := value.(string)
	return text
}

func int64ValueAt(payload any, path string) int64 {
	switch value := valueAt(payload, path).(type) {
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(value, 10, 64)
		return n
	default:
		return 0
	}
}

func jwtExpiryMillis(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.ExpiresAt <= 0 {
		return 0
	}
	return claims.ExpiresAt * 1000
}
