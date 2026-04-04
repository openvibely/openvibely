package anthropicclient

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

const (
	maxRetries      = 3
	maxRetryBackoff = 30 * time.Second
)

// retryableStatusCodes are HTTP status codes that trigger a retry.
var retryableStatusCodes = map[int]bool{
	429: true, // Too Many Requests
	500: true, // Internal Server Error
	502: true, // Bad Gateway
	503: true, // Service Unavailable
	529: true, // Overloaded
}

// isRetryable returns true if the HTTP status code should be retried.
func isRetryable(statusCode int) bool {
	return retryableStatusCodes[statusCode]
}

// retryBackoff returns the backoff duration for the given attempt (0-indexed).
// For 429 responses, it checks the Retry-After header and uses that value if present.
func retryBackoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil && resp.StatusCode == 429 {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if seconds, err := strconv.Atoi(ra); err == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	// Exponential backoff: 1s, 2s, 4s
	return time.Duration(1<<uint(attempt)) * time.Second
}

// doWithRetry executes an HTTP request with retry logic for retryable status codes.
// It returns the response (with status 200) or an error after exhausting retries.
// The caller must close the response body.
// The buildReq function is called for each attempt to create a fresh request
// (since the request body reader is consumed on each attempt).
func doWithRetry(ctx context.Context, client *http.Client, buildReq func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			log.Printf("[anthropicclient] retry attempt %d/%d", attempt, maxRetries)
		}

		req, err := buildReq()
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		// Not retryable — return error immediately
		if !isRetryable(resp.StatusCode) || attempt == maxRetries {
			return resp, nil
		}

		backoff := retryBackoff(attempt, resp)

		// If server asks us to wait a very long time (common for quota windows),
		// surface the 429 immediately instead of keeping executions "running"
		// for minutes/hours with no user-visible failure.
		if backoff > maxRetryBackoff {
			log.Printf("[anthropicclient] received HTTP %d with retry delay %v (> %v), skipping retry",
				resp.StatusCode, backoff, maxRetryBackoff)
			return resp, nil
		}

		// Retryable — close body, backoff, and retry
		resp.Body.Close()
		log.Printf("[anthropicclient] received HTTP %d, retrying in %v", resp.StatusCode, backoff)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}

		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return nil, fmt.Errorf("exhausted retries: %w", lastErr)
}
