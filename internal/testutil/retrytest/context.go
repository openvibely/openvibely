// Package retrytest removes retry backoff waits from tests without changing
// attempt limits, error classification, or cancellation propagation.
package retrytest

import (
	"context"
	"time"

	"github.com/openvibely/openvibely/internal/httpretry"
)

// Context returns a background context whose retry timers fire immediately.
func Context() context.Context {
	return httpretry.WithRetryTimer(context.Background(), func(time.Duration) <-chan time.Time {
		ready := make(chan time.Time, 1)
		ready <- time.Time{}
		return ready
	})
}
