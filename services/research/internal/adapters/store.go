// Package adapters connects research to SQLite, the catalog (gRPC) and HTTP.
package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"constella/internal/platform/events"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
	"constella/services/research/internal/app"
)

// Schema is the research database: sweeps, runs and the outbox.
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS sweeps (
		id           TEXT PRIMARY KEY,
		status       TEXT NOT NULL,
		created_at   TEXT NOT NULL,
		finished_at  TEXT,
		build_id     TEXT NOT NULL,
		culture      TEXT NOT NULL,
		total        INTEGER NOT NULL,
		done         INTEGER NOT NULL DEFAULT 0,
		error_code   TEXT NOT NULL DEFAULT '',
		error_detail TEXT NOT NULL DEFAULT '',
		spec         TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS sweeps_created ON sweeps(created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS runs (
		sweep_id TEXT    NOT NULL,
		seq      INTEGER NOT NULL,
		data     TEXT    NOT NULL,
		PRIMARY KEY (sweep_id, seq)
	) WITHOUT ROWID`,
	outbox.Schema,
}

// Store keeps research results in SQLite (research.db).
type Store struct{ DB *sql.DB }

const sweepColumns = `id, status, created_at, finished_at, build_id, culture, total, done, error_code, error_detail, spec`

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// CreateSweep implements app.Store.
func (s Store) CreateSweep(ctx context.Context, sw app.Sweep) error {
	spec, err := json.Marshal(sw.Spec)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO sweeps (id, status, created_at, build_id, culture, total, spec)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sw.ID, sw.Status, ts(sw.CreatedAt), sw.BuildID, sw.Culture, sw.Total, string(spec))
	if err != nil {
		return fmt.Errorf("insert sweep %s: %w", sw.ID, err)
	}
	return nil
}

// AddRun implements app.Store; it also advances the sweep's progress.
func (s Store) AddRun(ctx context.Context, sweepID string, r app.Run) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal run: %w", err)
	}
	return sqlitex.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs (sweep_id, seq, data) VALUES (?, ?, ?)`,
			sweepID, r.Seq, string(data)); err != nil {
			return fmt.Errorf("insert run %s/%d: %w", sweepID, r.Seq, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sweeps SET done = done + 1 WHERE id = ?`, sweepID); err != nil {
			return fmt.Errorf("advance sweep %s: %w", sweepID, err)
		}
		return nil
	})
}

// FinishSweep implements app.Store.
func (s Store) FinishSweep(ctx context.Context, sw app.Sweep, ev events.Envelope) error {
	return sqlitex.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		finished := ""
		if sw.FinishedAt != nil {
			finished = ts(*sw.FinishedAt)
		}
		res, err := tx.ExecContext(ctx, `UPDATE sweeps SET status = ?, finished_at = ?, error_code = ?, error_detail = ?
			WHERE id = ?`, sw.Status, finished, sw.ErrorCode, sw.ErrorDetail, sw.ID)
		if err != nil {
			return fmt.Errorf("update sweep %s: %w", sw.ID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 { // RowsAffected cannot fail on SQLite
			return app.ErrNotFound
		}
		return outbox.Add(ctx, tx, ev)
	})
}

// Get implements app.Store.
func (s Store) Get(ctx context.Context, id string) (app.Sweep, []app.Run, error) {
	sw, err := scanSweep(s.DB.QueryRowContext(ctx, `SELECT `+sweepColumns+` FROM sweeps WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return app.Sweep{}, nil, app.ErrNotFound
	}
	if err != nil {
		return app.Sweep{}, nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT data FROM runs WHERE sweep_id = ? ORDER BY seq`, id)
	if err != nil {
		return app.Sweep{}, nil, fmt.Errorf("query runs of %s: %w", id, err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	runs := []app.Run{}
	for rows.Next() {
		var data string
		var r app.Run
		if err := rows.Scan(&data); err != nil {
			return app.Sweep{}, nil, fmt.Errorf("scan run: %w", err)
		}
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return app.Sweep{}, nil, fmt.Errorf("decode run of %s: %w", id, err)
		}
		runs = append(runs, r)
	}
	return sw, runs, rows.Err()
}

// List implements app.Store.
func (s Store) List(ctx context.Context, limit int) ([]app.Sweep, error) {
	return s.sweeps(ctx, `SELECT `+sweepColumns+` FROM sweeps ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
}

// Running implements app.Store.
func (s Store) Running(ctx context.Context) ([]app.Sweep, error) {
	return s.sweeps(ctx, `SELECT `+sweepColumns+` FROM sweeps WHERE status = ?`, app.StatusRunning)
}

func (s Store) sweeps(ctx context.Context, q string, args ...any) ([]app.Sweep, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query sweeps: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out := []app.Sweep{}
	for rows.Next() {
		sw, err := scanSweep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sw)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanSweep(r scanner) (app.Sweep, error) {
	var (
		sw                app.Sweep
		created, finished sql.NullString
		spec              string
	)
	if err := r.Scan(&sw.ID, &sw.Status, &created, &finished, &sw.BuildID, &sw.Culture, &sw.Total, &sw.Done,
		&sw.ErrorCode, &sw.ErrorDetail, &spec); err != nil {
		return app.Sweep{}, fmt.Errorf("scan sweep: %w", err)
	}
	var err error
	if sw.CreatedAt, err = time.Parse(time.RFC3339Nano, created.String); err != nil {
		return app.Sweep{}, fmt.Errorf("parse created_at: %w", err)
	}
	if finished.String != "" {
		t, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return app.Sweep{}, fmt.Errorf("parse finished_at: %w", err)
		}
		sw.FinishedAt = &t
	}
	if err := json.Unmarshal([]byte(spec), &sw.Spec); err != nil {
		return app.Sweep{}, fmt.Errorf("decode spec of %s: %w", sw.ID, err)
	}
	return sw, nil
}
