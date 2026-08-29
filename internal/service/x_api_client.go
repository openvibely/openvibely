package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // OAuth 1.0a requires HMAC-SHA1.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const xAPIBaseURL = "https://api.x.com"

type XCredentials struct{ ConsumerKey, ConsumerSecret, AccessToken, AccessTokenSecret string }

func (c XCredentials) Ready() bool {
	return strings.TrimSpace(c.ConsumerKey) != "" && strings.TrimSpace(c.ConsumerSecret) != "" && strings.TrimSpace(c.AccessToken) != "" && strings.TrimSpace(c.AccessTokenSecret) != ""
}

type XUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}
type XTweet struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	AuthorID       string `json:"author_id"`
	ConversationID string `json:"conversation_id"`
}
type XMentionsResponse struct {
	Data     []XTweet `json:"data"`
	Includes struct {
		Users []XUser `json:"users"`
	} `json:"includes"`
	Meta struct {
		NewestID  string `json:"newest_id"`
		NextToken string `json:"next_token"`
	} `json:"meta"`
}

type xMentionsResponse = XMentionsResponse

type XAPIClient struct {
	baseURL     string
	client      *http.Client
	credentials XCredentials
	now         func() time.Time
	nonce       func() string
	sleep       func(context.Context, time.Duration) error
	maxAttempts int
}

func NewXAPIClient(credentials XCredentials) *XAPIClient {
	return &XAPIClient{baseURL: xAPIBaseURL, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, credentials: credentials, now: time.Now, nonce: xNonce, sleep: sleepContext, maxAttempts: 3}
}

func xNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b[:])
}
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *XAPIClient) Me(ctx context.Context) (XUser, error) {
	var out struct {
		Data XUser `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/2/users/me", nil, nil, &out); err != nil {
		return XUser{}, err
	}
	if out.Data.ID == "" {
		return XUser{}, fmt.Errorf("X API returned no authenticated user")
	}
	return out.Data, nil
}
func (c *XAPIClient) Mentions(ctx context.Context, userID, sinceID, paginationToken string) (xMentionsResponse, error) {
	q := url.Values{"max_results": {"100"}, "tweet.fields": {"author_id,conversation_id"}, "expansions": {"author_id"}, "user.fields": {"username,name"}}
	if sinceID != "" {
		q.Set("since_id", sinceID)
	}
	if paginationToken != "" {
		q.Set("pagination_token", paginationToken)
	}
	var out xMentionsResponse
	err := c.doJSON(ctx, http.MethodGet, "/2/users/"+url.PathEscape(userID)+"/mentions", q, nil, &out)
	return out, err
}
func (c *XAPIClient) Post(ctx context.Context, text, replyTo string) (string, error) {
	payload := map[string]any{"text": text}
	if replyTo != "" {
		payload["reply"] = map[string]string{"in_reply_to_tweet_id": replyTo}
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/2/tweets", nil, payload, &out); err != nil {
		return "", err
	}
	if out.Data.ID == "" {
		return "", fmt.Errorf("X API returned no tweet id")
	}
	return out.Data.ID, nil
}

func (c *XAPIClient) doJSON(ctx context.Context, method, path string, query url.Values, payload any, out any) error {
	if !c.credentials.Ready() {
		return fmt.Errorf("X OAuth 1.0a credentials are incomplete")
	}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	attempts := c.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		u, err := url.Parse(strings.TrimRight(c.baseURL, "/") + path)
		if err != nil {
			return err
		}
		u.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", c.authorization(method, u))
		resp, err := c.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !xMethodRetryable(method) || attempt == attempts {
				return fmt.Errorf("X API request failed: %w", err)
			}
			if err := c.sleep(ctx, time.Duration(attempt)*time.Second); err != nil {
				return err
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read X API response: %w", readErr)
		}
		if len(data) > 1<<20 {
			return fmt.Errorf("X API response exceeded 1 MiB")
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if len(bytes.TrimSpace(data)) == 0 {
				return fmt.Errorf("X API returned an empty response")
			}
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode X API response: %w", err)
			}
			return nil
		}
		if xMethodRetryable(method) && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < attempts {
			delay := xRetryDelay(resp, attempt, c.now())
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("X API returned %s: %s", resp.Status, xProviderError(data))
	}
	return fmt.Errorf("X API request failed")
}

func xMethodRetryable(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func xRetryDelay(resp *http.Response, attempt int, now time.Time) time.Duration {
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if seconds, ok := xBoundedDecimal(v, 60); ok {
			return time.Duration(seconds) * time.Second
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("x-rate-limit-reset")); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			d := time.Unix(unix, 0).Sub(now)
			if d > 0 {
				if d > time.Minute {
					return time.Minute
				}
				return d
			}
		}
	}
	return time.Duration(attempt) * time.Second
}

func xBoundedDecimal(value string, maximum uint64) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return 0, true
	}
	maxText := strconv.FormatUint(maximum, 10)
	if len(value) > len(maxText) || (len(value) == len(maxText) && value > maxText) {
		return maximum, true
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return parsed, err == nil
}

func xProviderError(data []byte) string {
	var v struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
	}
	if json.Unmarshal(data, &v) == nil {
		if strings.TrimSpace(v.Detail) != "" {
			return strings.TrimSpace(v.Detail)
		}
		if strings.TrimSpace(v.Title) != "" {
			return strings.TrimSpace(v.Title)
		}
	}
	return "provider request failed"
}
func (c *XAPIClient) authorization(method string, u *url.URL) string {
	oauth := map[string]string{"oauth_consumer_key": c.credentials.ConsumerKey, "oauth_nonce": c.nonce(), "oauth_signature_method": "HMAC-SHA1", "oauth_timestamp": strconv.FormatInt(c.now().Unix(), 10), "oauth_token": c.credentials.AccessToken, "oauth_version": "1.0"}
	params := make([][2]string, 0, len(oauth)+len(u.Query()))
	for k, v := range oauth {
		params = append(params, [2]string{k, v})
	}
	for k, values := range u.Query() {
		for _, v := range values {
			params = append(params, [2]string{k, v})
		}
	}
	sort.Slice(params, func(i, j int) bool {
		a, b := xEscape(params[i][0]), xEscape(params[j][0])
		if a == b {
			return xEscape(params[i][1]) < xEscape(params[j][1])
		}
		return a < b
	})
	pairs := make([]string, len(params))
	for i, p := range params {
		pairs[i] = xEscape(p[0]) + "=" + xEscape(p[1])
	}
	baseURL := u.Scheme + "://" + u.Host + u.EscapedPath()
	base := strings.ToUpper(method) + "&" + xEscape(baseURL) + "&" + xEscape(strings.Join(pairs, "&"))
	key := xEscape(c.credentials.ConsumerSecret) + "&" + xEscape(c.credentials.AccessTokenSecret)
	mac := hmac.New(sha1.New, []byte(key))
	_, _ = mac.Write([]byte(base))
	oauth["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	keys := make([]string, 0, len(oauth))
	for k := range oauth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]string, len(keys))
	for i, k := range keys {
		fields[i] = xEscape(k) + `="` + xEscape(oauth[k]) + `"`
	}
	return "OAuth " + strings.Join(fields, ", ")
}
func xEscape(v string) string { return strings.ReplaceAll(url.QueryEscape(v), "+", "%20") }
