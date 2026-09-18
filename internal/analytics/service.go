// Package analytics owns the asynchronous historical read model. Request handlers
// only read immutable summaries; they never run DuckDB queries.
package analytics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/doout/dispatch/internal/core"
	_ "github.com/duckdb/duckdb-go/v2"
)

type Source interface {
	ListAnalyticsEvents(context.Context, int) ([]core.AnalyticsEvent, error)
	AckAnalyticsEvents(context.Context, []core.AnalyticsEvent) error
}

// Reader can later be backed by an external analytics service.
type Reader interface {
	Summary(map[string]bool, int) Summary
}

type Counts struct {
	Runs            int64   `json:"runs"`
	Succeeded       int64   `json:"succeeded"`
	Failed          int64   `json:"failed"`
	Cancelled       int64   `json:"cancelled"`
	Reused          int64   `json:"reused"`
	DurationSeconds float64 `json:"durationSeconds"`
}
type Day struct {
	Date        string `json:"date"`
	Deployments Counts `json:"deployments"`
	Workflows   Counts `json:"workflows"`
	Jobs        Counts `json:"jobs"`
}
type Summary struct {
	State     string     `json:"state"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Days      int        `json:"days"`
	Daily     []Day      `json:"daily"`
	Totals    Day        `json:"totals"`
}
type row struct {
	Project, Date, Kind string
	Counts              Counts
}
type snapshot struct {
	State     string
	UpdatedAt *time.Time
	Rows      []row
}
type Service struct {
	source    Source
	directory string
	logger    *slog.Logger
	current   atomic.Pointer[snapshot]
}

func New(source Source, directory string, logger *slog.Logger) *Service {
	s := &Service{source: source, directory: directory, logger: logger}
	s.current.Store(&snapshot{State: "starting"})
	return s
}

func (s *Service) Run(ctx context.Context) {
	// Initialization runs in this worker, never on controller startup's critical path.
	for ctx.Err() == nil {
		err := s.run(ctx)
		if ctx.Err() != nil {
			return
		}
		s.setState("unavailable")
		if s.logger != nil {
			s.logger.Error("Analytics worker paused; operational requests remain available", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}
func (s *Service) setState(state string) {
	old := s.current.Load()
	s.current.Store(&snapshot{State: state, UpdatedAt: old.UpdatedAt, Rows: old.Rows})
}
func (s *Service) run(ctx context.Context) error {
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		return err
	}
	parquet := filepath.Join(s.directory, "parquet")
	if err := os.MkdirAll(parquet, 0700); err != nil {
		return err
	}
	path, err := filepath.Abs(filepath.Join(s.directory, "history.duckdb"))
	if err != nil {
		return err
	}
	params := url.Values{"threads": {"1"}, "memory_limit": {"128MB"}, "max_temp_directory_size": {"512MB"}, "autoinstall_known_extensions": {"false"}, "autoload_known_extensions": {"false"}}
	db, err := sql.Open("duckdb", path+"?"+params.Encode())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS history (
 kind VARCHAR,entity_id VARCHAR,project_id VARCHAR,name VARCHAR,state VARCHAR,
 started_at TIMESTAMP,finished_at TIMESTAMP,reused BOOLEAN,event_id BIGINT,
 PRIMARY KEY(kind,entity_id));
 CREATE TEMP TABLE IF NOT EXISTS export_batch (
 kind VARCHAR,entity_id VARCHAR,project_id VARCHAR,name VARCHAR,state VARCHAR,
 started_at TIMESTAMP,finished_at TIMESTAMP,reused BOOLEAN,event_id BIGINT);`)
	if err != nil {
		return err
	}
	if err = s.restore(ctx, db); err != nil {
		return err
	}
	if err = s.refresh(ctx, db, "catching_up"); err != nil {
		return err
	}
	for ctx.Err() == nil {
		// Bounded batches release SQLite between reads and keep live writes responsive.
		batchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		events, err := s.source.ListAnalyticsEvents(batchCtx, 250)
		if err == nil && len(events) > 0 {
			err = s.importBatch(batchCtx, db, events)
		}
		if err == nil {
			state := "ready"
			if len(events) == 250 {
				state = "catching_up"
			}
			err = s.refresh(batchCtx, db, state)
		}
		cancel()
		if err != nil {
			return err
		}
		pause := 15 * time.Second
		if len(events) == 250 {
			pause = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
	return ctx.Err()
}

func (s *Service) importBatch(ctx context.Context, db *sql.DB, events []core.AnalyticsEvent) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM export_batch"); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO export_batch VALUES (?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	hash := sha256.New()
	for _, e := range events {
		fmt.Fprintf(hash, "%d,", e.ID)
		if _, err = stmt.ExecContext(ctx, e.Kind, e.EntityID, e.ProjectID, e.Name, e.State, e.StartedAt.UTC(), e.FinishedAt.UTC(), e.Reused, e.ID); err != nil {
			return err
		}
	}
	// Retries after a crash overwrite the same entities. Late older events cannot
	// replace a newer correction already imported from PostgreSQL.
	_, err = tx.ExecContext(ctx, `INSERT INTO history SELECT * FROM export_batch QUALIFY row_number() OVER (PARTITION BY kind,entity_id ORDER BY event_id DESC)=1
 ON CONFLICT(kind,entity_id) DO UPDATE SET project_id=excluded.project_id,name=excluded.name,
 state=excluded.state,started_at=excluded.started_at,finished_at=excluded.finished_at,
 reused=excluded.reused,event_id=excluded.event_id WHERE excluded.event_id>=history.event_id`)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	filename := filepath.Join(s.directory, "parquet", fmt.Sprintf("%x.parquet", hash.Sum(nil)))
	temp := filename + ".tmp"
	if err := os.Remove(temp); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Deterministic filenames make a retry safe after export but before queue ack.
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	if _, err = db.ExecContext(ctx, "COPY export_batch TO "+quote(temp)+" (FORMAT PARQUET, COMPRESSION ZSTD)"); err != nil {
		return err
	}
	file, err := os.OpenFile(temp, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = file.Sync()
	file.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(temp, filename); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(filename))
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	return s.source.AckAnalyticsEvents(ctx, events)
}

func (s *Service) refresh(ctx context.Context, db *sql.DB, state string) error {
	rows, err := db.QueryContext(ctx, `SELECT project_id,strftime(finished_at,'%Y-%m-%d'),kind,
 count(*),count(*) FILTER(WHERE state='succeeded'),count(*) FILTER(WHERE state='failed'),
 count(*) FILTER(WHERE state='cancelled'),count(*) FILTER(WHERE reused),
 sum(greatest(0,epoch(finished_at)-epoch(started_at)))
 FROM history WHERE finished_at>=? GROUP BY 1,2,3 ORDER BY 2`, time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -89))
	if err != nil {
		return err
	}
	defer rows.Close()
	values := []row{}
	for rows.Next() {
		var value row
		c := &value.Counts
		if err = rows.Scan(&value.Project, &value.Date, &value.Kind, &c.Runs, &c.Succeeded, &c.Failed, &c.Cancelled, &c.Reused, &c.DurationSeconds); err != nil {
			return err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	s.current.Store(&snapshot{State: state, UpdatedAt: &now, Rows: values})
	return nil
}
func add(a *Counts, b Counts) {
	a.Runs += b.Runs
	a.Succeeded += b.Succeeded
	a.Failed += b.Failed
	a.Cancelled += b.Cancelled
	a.Reused += b.Reused
	a.DurationSeconds += b.DurationSeconds
}
func counts(day *Day, kind string) *Counts {
	switch kind {
	case "deployment":
		return &day.Deployments
	case "workflow":
		return &day.Workflows
	default:
		return &day.Jobs
	}
}
func (s *Service) Summary(projects map[string]bool, days int) Summary {
	if days != 7 && days != 30 && days != 90 {
		days = 30
	}
	current := s.current.Load()
	result := Summary{State: current.State, UpdatedAt: current.UpdatedAt, Days: days, Daily: make([]Day, days)}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1-days)
	indices := map[string]int{}
	for i := range result.Daily {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		result.Daily[i].Date = date
		indices[date] = i
	}
	for _, row := range current.Rows {
		if !projects[row.Project] {
			continue
		}
		i, ok := indices[row.Date]
		if !ok {
			continue
		}
		add(counts(&result.Daily[i], row.Kind), row.Counts)
		add(counts(&result.Totals, row.Kind), row.Counts)
	}
	return result
}

// Parquet is an independent, portable history copy. Rebuild a missing DuckDB
// read model from it without touching operational deployment records.
func (s *Service) restore(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS analytics_meta (version INTEGER PRIMARY KEY)"); err != nil {
		return err
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM analytics_meta WHERE version=1").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(s.directory, "parquet", "*.parquet"))
	if err != nil {
		return err
	}
	if len(files) > 0 {
		pattern := strings.ReplaceAll(filepath.Join(s.directory, "parquet", "*.parquet"), "'", "''")
		_, err = db.ExecContext(ctx, "INSERT INTO history SELECT * FROM read_parquet('"+pattern+"') QUALIFY row_number() OVER(PARTITION BY kind,entity_id ORDER BY event_id DESC)=1 ON CONFLICT(kind,entity_id) DO NOTHING")
		if err != nil {
			return err
		}
	}
	_, err = db.ExecContext(ctx, "INSERT INTO analytics_meta VALUES(1) ON CONFLICT DO NOTHING")
	return err
}
