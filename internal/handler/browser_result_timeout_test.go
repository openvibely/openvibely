package handler

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPagedBrowserResultBudgets(t *testing.T) {
	t.Run("slow native startup leaves a full scenario budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ready := make(chan struct{})
			results := make(chan string, 1)
			go func() {
				time.Sleep(30 * time.Second)
				close(ready)
				time.Sleep(15 * time.Second)
				results <- "pass"
			}()
			// The old shared 40-second deadline expired during the scenario.
			require.Equal(t, "pass", waitPagedBrowserResult(context.Background(), ready, results, 40*time.Second))
		})
	})
	t.Run("startup remains bounded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			require.Equal(t, "timeout: native startup (initial document not delivered)", waitPagedBrowserResult(context.Background(), make(chan struct{}), make(chan string), 40*time.Second))
			require.Equal(t, 40*time.Second, time.Since(start))
		})
	})
	t.Run("scenario remains bounded after document delivery", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ready := make(chan struct{})
			go func() {
				time.Sleep(30 * time.Second)
				close(ready)
			}()
			start := time.Now()
			require.Equal(t, "timeout: browser scenario", waitPagedBrowserResult(context.Background(), ready, make(chan string), 40*time.Second))
			require.Equal(t, 70*time.Second, time.Since(start))
		})
	})
	t.Run("Chrome keeps a single budget", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			require.Equal(t, "timeout: browser scenario", waitPagedBrowserResult(context.Background(), nil, make(chan string), 40*time.Second))
			require.Equal(t, 40*time.Second, time.Since(start))
		})
	})
	t.Run("early browser errors are returned immediately", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			results := make(chan string, 1)
			results <- "startup error"
			start := time.Now()
			require.Equal(t, "startup error", waitPagedBrowserResult(context.Background(), make(chan struct{}), results, 40*time.Second))
			require.Zero(t, time.Since(start))
		})
	})
}
