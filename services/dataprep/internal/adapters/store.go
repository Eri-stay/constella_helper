package adapters

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"constella/internal/platform/events"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
	"constella/services/dataprep/internal/app"
)

// Schema is the dataprep database: builds plus the outbox.
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS builds (
		id              TEXT PRIMARY KEY,
		status          TEXT    NOT NULL,
		started_at      TEXT    NOT NULL,
		finished_at     TEXT,
		artifact        TEXT    NOT NULL DEFAULT '',
		sha256          TEXT    NOT NULL DEFAULT '',
		size_bytes      INTEGER NOT NULL DEFAULT 0,
		stars           INTEGER NOT NULL DEFAULT 0,
		constellations  INTEGER NOT NULL DEFAULT 0,
		cultures        INTEGER NOT NULL DEFAULT 0,
		edges           INTEGER NOT NULL DEFAULT 0,
		dropped_edges   INTEGER NOT NULL DEFAULT 0,
		error_code      TEXT    NOT NULL DEFAULT '',
		error_detail    TEXT    NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS builds_started ON builds(started_at DESC)`,
	outbox.Schema,
}

// Store keeps builds in SQLite (dataprep.db).
type Store struct{ DB *sql.DB }

const buildColumns = `id, status, started_at, finished_at, artifact, sha256, size_bytes,
	stars, constellations, cultures, edges, dropped_edges, error_code, error_detail`

// Create implements app.Store.
func (s Store) Create(ctx context.Context, b app.Build, ev events.Envelope) error {
	return sqlitex.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		var running int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM builds WHERE status = ?`, app.StatusRunning).
			Scan(&running); err != nil {
			return fmt.Errorf("count running builds: %w", err)
		}
		if running > 0 {
			return app.ErrBusy
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO builds (id, status, started_at) VALUES (?, ?, ?)`,
			b.ID, b.Status, formatTime(b.StartedAt)); err != nil {
			return fmt.Errorf("insert build %s: %w", b.ID, err)
		}
		return outbox.Add(ctx, tx, ev)
	})
}

// Finish implements app.Store.
func (s Store) Finish(ctx context.Context, b app.Build, ev events.Envelope) error {
	return sqlitex.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		finished := ""
		if b.FinishedAt != nil {
			finished = formatTime(*b.FinishedAt)
		}
		res, err := tx.ExecContext(ctx, `UPDATE builds SET status = ?, finished_at = ?, artifact = ?, sha256 = ?,
			size_bytes = ?, stars = ?, constellations = ?, cultures = ?, edges = ?, dropped_edges = ?,
			error_code = ?, error_detail = ? WHERE id = ?`,
			b.Status, finished, b.Artifact, b.SHA256, b.SizeBytes, b.Stars, b.Constellations, b.Cultures,
			b.Edges, b.DroppedEdges, b.ErrorCode, b.ErrorDetail, b.ID)
		if err != nil {
			return fmt.Errorf("update build %s: %w", b.ID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 { // RowsAffected cannot fail on SQLite; 0 means unknown id
			return app.ErrNotFound
		}
		return outbox.Add(ctx, tx, ev)
	})
}

// Get implements app.Store.
func (s Store) Get(ctx context.Context, id string) (app.Build, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+buildColumns+` FROM builds WHERE id = ?`, id)
	b, err := scanBuild(row)
	if errors.Is(err, sql.ErrNoRows) {
		return app.Build{}, app.ErrNotFound
	}
	return b, err
}

// List implements app.Store.
func (s Store) List(ctx context.Context, limit int) ([]app.Build, error) {
	return s.query(ctx, `SELECT `+buildColumns+` FROM builds ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
}

// Running implements app.Store.
func (s Store) Running(ctx context.Context) ([]app.Build, error) {
	return s.query(ctx, `SELECT `+buildColumns+` FROM builds WHERE status = ?`, app.StatusRunning)
}

func (s Store) query(ctx context.Context, q string, args ...any) ([]app.Build, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query builds: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	var out []app.Build
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate builds: %w", err)
	}
	return out, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanBuild(r scanner) (app.Build, error) {
	var (
		b                 app.Build
		started, finished sql.NullString
	)
	if err := r.Scan(&b.ID, &b.Status, &started, &finished, &b.Artifact, &b.SHA256, &b.SizeBytes,
		&b.Stars, &b.Constellations, &b.Cultures, &b.Edges, &b.DroppedEdges, &b.ErrorCode, &b.ErrorDetail); err != nil {
		return app.Build{}, fmt.Errorf("scan build: %w", err)
	}
	var err error
	if b.StartedAt, err = parseTime(started.String); err != nil {
		return app.Build{}, err
	}
	if finished.String != "" {
		t, err := parseTime(finished.String)
		if err != nil {
			return app.Build{}, err
		}
		b.FinishedAt = &t
	}
	return b, nil
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t, nil
}
