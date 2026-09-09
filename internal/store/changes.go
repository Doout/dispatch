package store

import (
	"context"
	"database/sql"
	"sync"
)

// changeDB broadcasts committed writes. Subscribers re-read their filtered view;
// notifications contain no resource data and are coalesced through one channel.
type changeDB struct {
	*sql.DB
	mu      sync.Mutex
	changed chan struct{}
}

func (d *changeDB) changes() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	return d.changed
}

func (d *changeDB) notify() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed != nil {
		close(d.changed)
	}
	d.changed = make(chan struct{})
}

func (d *changeDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := d.DB.ExecContext(ctx, query, args...)
	if err == nil {
		if rows, e := result.RowsAffected(); e == nil && rows > 0 {
			d.notify()
		}
	}
	return result, err
}

func (d *changeDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*changeTx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &changeTx{Tx: tx, db: d}, nil
}

type changeTx struct {
	*sql.Tx
	db *changeDB
}

func (t *changeTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil {
		t.db.notify()
	}
	return err
}

// Changes returns a channel closed by the next committed local write.
func (s *SQLStore) Changes() <-chan struct{} { return s.db.changes() }
