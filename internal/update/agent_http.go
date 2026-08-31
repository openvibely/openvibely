package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type APIErrorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

type AgentHTTPClient struct {
	base   *url.URL
	token  string
	client *http.Client
}

func NewAgentHTTPClient(rawURL, token string, supplied *http.Client) (*AgentHTTPClient, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("agent token is required")
	}
	if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "unix" {
		return nil, errors.New("agent URL must use https, loopback http, or unix")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("agent HTTP URL must be loopback")
		}
	}
	if u.Scheme == "unix" {
		if !strings.HasPrefix(u.Path, "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("agent unix URL must be an absolute socket path")
		}
		socket := u.Path
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		supplied = &http.Client{Transport: transport, Timeout: 15 * time.Second}
		u, _ = url.Parse("http://openvibely-agent")
	}
	if supplied == nil {
		supplied = &http.Client{Timeout: 15 * time.Second}
	}
	clone := *supplied
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &AgentHTTPClient{base: u, token: token, client: &clone}, nil
}

func (c *AgentHTTPClient) Do(ctx context.Context, method, path, idempotency string, requestBody, responseBody any, expected ...int) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
		if idempotency == "" {
			return errors.New("Idempotency-Key is required for agent POST")
		}
		req.Header.Set("Idempotency-Key", idempotency)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.Join(ErrUpdateRetryable, err)
	}
	defer resp.Body.Close()
	ok := false
	for _, status := range expected {
		if resp.StatusCode == status {
			ok = true
			break
		}
	}
	limited := io.LimitReader(resp.Body, 1<<20+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return errors.Join(ErrUpdateRetryable, err)
	}
	if len(data) > 1<<20 {
		return errors.New("agent response exceeds size limit")
	}
	if !ok {
		var envelope APIErrorEnvelope
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Code != "" {
			err := fmt.Errorf("agent %s: %s", envelope.Error.Code, envelope.Error.Message)
			if envelope.Error.Retryable {
				return errors.Join(ErrUpdateRetryable, err)
			}
			return err
		}
		err := fmt.Errorf("agent returned HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooEarly || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return errors.Join(ErrUpdateRetryable, err)
		}
		return err
	}
	if responseBody == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(responseBody); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("agent response contains trailing JSON")
	}
	return nil
}
