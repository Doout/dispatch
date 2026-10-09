package tenancy

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"time"
)

// AcquireControllerLease allows one active hosted controller per catalog. It
// uses a dedicated PostgreSQL session so a crashed process releases the lock.
// SQLite callers must also hold the hosted data-directory file lock.
func (c *Catalog) AcquireControllerLease(ctx context.Context, lost func()) (func(), error) {
	if !c.postgres {
		return func() {}, nil
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	const key int64 = 0x6469737061746368
	var acquired bool
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil || !acquired {
		_ = conn.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("another hosted controller is using this catalog")
	}
	watch, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watch.Done():
				return
			case <-ticker.C:
			}
			ping, stop := context.WithTimeout(watch, 5*time.Second)
			err := conn.PingContext(ping)
			stop()
			if err != nil {
				if watch.Err() == nil && lost != nil {
					lost()
				}
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			unlock, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			// A failed unlock must discard the physical connection, not return a
			// possibly still locked session to the pool.
			if _, err := conn.ExecContext(unlock, "SELECT pg_advisory_unlock($1)", key); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
			_ = conn.Close()
		})
	}, nil
}
