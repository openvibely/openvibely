// Package nativehost provides lifecycle control for test-only native hosts.
package nativehost

import (
	"os"
	"time"
)

const ShutdownFileEnv = "OPENVIBELY_NATIVE_SHUTDOWN_FILE"

// WatchShutdown calls quit when the parent requests normal application shutdown.
// Register after creating the app; quit should dispatch onto its UI thread.
func WatchShutdown(quit func()) func() {
	path := os.Getenv(ShutdownFileEnv)
	if path == "" {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if _, err := os.Stat(path); err == nil {
					quit()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}
