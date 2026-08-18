package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("relay resource not found")

type Store struct {
	db       *sql.DB
	postgres bool
}

func OpenStore(ctx context.Context, databaseURL string) (*Store, error) {
	driver, source, postgres := "sqlite", databaseURL, false
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		driver, postgres = "pgx", true
	}
	if strings.TrimSpace(source) == "" {
		source = "relay.db"
	}
	db, err := sql.Open(driver, source)
	if err != nil {
		return nil, err
	}
	if !postgres {
		db.SetMaxOpenConns(1)
	}
	store := &Store{db: db, postgres: postgres}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) q(query string) string {
	if !s.postgres {
		return query
	}
	for i := 1; strings.Contains(query, "?"); i++ {
		query = strings.Replace(query, "?", fmt.Sprintf("$%d", i), 1)
	}
	return query
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS relay_hooks (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS relay_deliveries (
            id TEXT PRIMARY KEY, hook_id TEXT NOT NULL REFERENCES relay_hooks(id) ON DELETE CASCADE,
            sequence BIGINT NOT NULL, method TEXT NOT NULL, headers TEXT NOT NULL, body BYTEA NOT NULL,
            received_at TEXT NOT NULL, state TEXT NOT NULL, attempt INTEGER NOT NULL DEFAULT 0,
            lease_token TEXT NOT NULL DEFAULT '', lease_until TEXT, available_at TEXT NOT NULL,
            error TEXT NOT NULL DEFAULT '', acked_at TEXT, UNIQUE(hook_id, sequence))`,
		`CREATE INDEX IF NOT EXISTS relay_deliveries_ready ON relay_deliveries(state, available_at, sequence)`,
	}
	if !s.postgres {
		statements[1] = strings.Replace(statements[1], "BYTEA", "BLOB", 1)
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateHook(ctx context.Context, name string, now time.Time) (Hook, error) {
	hook := Hook{ID: ulid.Make().String(), Name: name, CreatedAt: now.UTC()}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO relay_hooks(id,name,created_at) VALUES(?,?,?)`), hook.ID, hook.Name, stamp(hook.CreatedAt))
	return hook, err
}

func (s *Store) ListHooks(ctx context.Context) ([]Hook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,created_at FROM relay_hooks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Hook{}
	for rows.Next() {
		var item Hook
		var created string
		if err := rows.Scan(&item.ID, &item.Name, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteHook(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM relay_hooks WHERE id=?`), id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, hookID, method string, headers map[string][]string, body []byte, now time.Time) (Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Delivery{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM relay_hooks WHERE id=?`), hookID).Scan(&exists); err != nil {
		return Delivery{}, err
	}
	if exists == 0 {
		return Delivery{}, ErrNotFound
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COALESCE(MAX(sequence),0)+1 FROM relay_deliveries WHERE hook_id=?`), hookID).Scan(&sequence); err != nil {
		return Delivery{}, err
	}
	headerJSON, _ := json.Marshal(headers)
	delivery := Delivery{ID: ulid.Make().String(), HookID: hookID, Sequence: sequence, Method: method, Headers: headers, Body: body, ReceivedAt: now.UTC()}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO relay_deliveries(id,hook_id,sequence,method,headers,body,received_at,state,available_at) VALUES(?,?,?,?,?,?,?,?,?)`),
		delivery.ID, delivery.HookID, delivery.Sequence, delivery.Method, string(headerJSON), delivery.Body, stamp(delivery.ReceivedAt), "pending", stamp(delivery.ReceivedAt))
	if err != nil {
		return Delivery{}, err
	}
	return delivery, tx.Commit()
}

func (s *Store) LeaseNext(ctx context.Context, leaseDuration time.Duration, now time.Time) (*Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT id,hook_id,sequence,method,headers,body,received_at,attempt FROM relay_deliveries
        WHERE (state='pending' OR (state='leased' AND lease_until < ?)) AND available_at <= ? ORDER BY received_at,sequence LIMIT 1`
	if s.postgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	var item Delivery
	var headersJSON, received string
	err = tx.QueryRowContext(ctx, s.q(query), stamp(now), stamp(now)).Scan(&item.ID, &item.HookID, &item.Sequence, &item.Method, &headersJSON, &item.Body, &received, &item.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(headersJSON), &item.Headers)
	item.ReceivedAt = parseTime(received)
	item.Attempt++
	item.LeaseToken = ulid.Make().String()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE relay_deliveries SET state='leased',attempt=?,lease_token=?,lease_until=? WHERE id=?`), item.Attempt, item.LeaseToken, stamp(now.Add(leaseDuration)), item.ID)
	if err != nil {
		return nil, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) Acknowledge(ctx context.Context, id, leaseToken, disposition, detail string, now time.Time) error {
	state := "acked"
	var acked any = stamp(now)
	available := stamp(now)
	if disposition == "retry" {
		state, acked = "pending", nil
		available = stamp(now.Add(5 * time.Second))
	} else if disposition == "dead" {
		state = "dead"
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE relay_deliveries SET state=?,lease_token='',lease_until=NULL,available_at=?,error=?,acked_at=? WHERE id=? AND lease_token=? AND state='leased'`), state, available, detail, acked, id, leaseToken)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	var status Status
	var oldest sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),MIN(received_at) FROM relay_deliveries WHERE state IN ('pending','leased')`).Scan(&status.Pending, &oldest)
	if err == nil && oldest.Valid {
		value := parseTime(oldest.String)
		status.OldestPending = &value
	}
	return status, err
}

func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
