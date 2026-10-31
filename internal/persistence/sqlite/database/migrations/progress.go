package migrations

import (
	"log"
	"sync/atomic"
	"time"
)

// migrationProgress never logs conversation contents or identifiers. Its
// heartbeat remains visible even while one large row or SQL operation runs.
func migrationProgress(stage string) (advance func(), finish func()) {
	started := time.Now()
	var completed atomic.Int64
	stop, stopped := make(chan struct{}), make(chan struct{})
	log.Printf("[migration] stage=%s started", stage)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				log.Printf("[migration] stage=%s processed=%d elapsed=%s", stage, completed.Load(), time.Since(started).Round(time.Second))
			}
		}
	}()
	return func() { completed.Add(1) }, func() {
		close(stop)
		<-stopped
		log.Printf("[migration] stage=%s ended processed=%d elapsed=%s (commit pending)", stage, completed.Load(), time.Since(started))
	}
}
