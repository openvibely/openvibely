package database

import (
	"context"
	"database/sql"
	"sync"
)

// database/sql does not promise FIFO connection assignment. Keep maintenance
// out of its waiter lottery while application writes are queued or active.
// users includes writers waiting for maintenance, waiting for a pool connection,
// and using a connection. Readers and other processes are not admitted here.
type writerAdmission struct {
	mu          sync.Mutex
	changed     chan struct{}
	users       int
	maintenance bool
	refs        int // protected by writerAdmissions.Mutex
}

var writerAdmissions = struct {
	sync.Mutex
	entries map[*sql.DB]*writerAdmission
}{entries: make(map[*sql.DB]*writerAdmission)}

// RegisterWriterPriority shares admission between the application's writer
// helpers and background maintenance. The owner must unregister after use.
// References allow backend routing and the maintenance loop to own it together.
func RegisterWriterPriority(db *sql.DB) func() {
	writerAdmissions.Lock()
	g := writerAdmissions.entries[db]
	if g == nil {
		g = &writerAdmission{changed: make(chan struct{})}
		writerAdmissions.entries[db] = g
	}
	g.refs++
	writerAdmissions.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			writerAdmissions.Lock()
			defer writerAdmissions.Unlock()
			g.refs--
			if g.refs == 0 {
				delete(writerAdmissions.entries, db)
			}
		})
	}
}

func admissionFor(db *sql.DB) *writerAdmission {
	writerAdmissions.Lock()
	defer writerAdmissions.Unlock()
	return writerAdmissions.entries[db]
}

func (g *writerAdmission) notifyLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *writerAdmission) acquire(ctx context.Context, maintenance bool) (func(), error) {
	g.mu.Lock()
	if !maintenance {
		// Register BEFORE waiting; cleanup cannot win another batch while this
		// request is waking up or waiting inside database/sql.
		g.users++
		g.notifyLocked()
	}
	for {
		if err := ctx.Err(); err != nil {
			if !maintenance {
				g.users--
				g.notifyLocked()
			}
			g.mu.Unlock()
			return nil, err
		}
		if !g.maintenance && (!maintenance || g.users == 0) {
			break
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
	if maintenance {
		g.maintenance = true
	}
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if maintenance {
				g.maintenance = false
			} else {
				g.users--
			}
			g.notifyLocked()
		})
	}, nil
}

// AcquireWriteConn registers an application write before checking out its
// connection. Call release after rollback and restoration of local settings;
// release closes the connection BEFORE allowing maintenance to proceed.
// All application mutations must use this helper (including transactions).
func AcquireWriteConn(ctx context.Context, db *sql.DB) (*sql.Conn, func(), error) {
	releaseAdmission := func() {}
	if g := admissionFor(db); g != nil {
		var err error
		releaseAdmission, err = g.acquire(ctx, false)
		if err != nil {
			return nil, nil, err
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		releaseAdmission()
		return nil, nil, err
	}
	var once sync.Once
	return conn, func() { once.Do(func() { _ = conn.Close(); releaseAdmission() }) }, nil
}

func yieldToApplicationWrites(ctx context.Context, db *sql.DB) (func(), error) {
	if g := admissionFor(db); g != nil {
		return g.acquire(ctx, true)
	}
	// Single-attempt callers may have no registered maintenance loop.
	return func() {}, ctx.Err()
}
