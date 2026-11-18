// Package adapters connects catalog to SQLite, the cosmos.db files, HTTP and
// the broker.
package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"constella/internal/domain/catalog"
	"constella/internal/platform/events"
	"constella/internal/platform/inbox"
	"constella/internal/platform/outbox"
	"constella/services/catalog/internal/app"
)

// Schema is the catalog database: reports, inbox and outbox.
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS reports (
		build_id   TEXT PRIMARY KEY,
		verdict    TEXT NOT NULL,
		checked_at TEXT NOT NULL,
		artifact   TEXT NOT NULL,
		checks     TEXT NOT NULL
	)`,
	inbox.Schema,
	outbox.Schema,
}

// consumerName identifies catalog in the inbox table.
const consumerName = "catalog"

// Repo stores reports in SQLite (catalog.db).
type Repo struct{ DB *sql.DB }

// SaveReport implements app.Repository.
func (r Repo) SaveReport(ctx context.Context, inEventID string, rec app.Record, out events.Envelope) (bool, error) {
	checks, err := json.Marshal(rec.Checks)
	if err != nil {
		return false, fmt.Errorf("marshal checks: %w", err)
	}
	return inbox.Process(ctx, r.DB, consumerName, inEventID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reports (build_id, verdict, checked_at, artifact, checks)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(build_id) DO UPDATE SET verdict = excluded.verdict, checked_at = excluded.checked_at,
				artifact = excluded.artifact, checks = excluded.checks`,
			rec.BuildID, rec.Verdict, rec.CheckedAt.Format(time.RFC3339Nano), rec.Artifact, string(checks)); err != nil {
			return fmt.Errorf("upsert report %s: %w", rec.BuildID, err)
		}
		return outbox.Add(ctx, tx, out)
	})
}

const reportColumns = `build_id, verdict, checked_at, artifact, checks`

// Report implements app.Repository.
func (r Repo) Report(ctx context.Context, buildID string) (app.Record, error) {
	rec, err := scanRecord(r.DB.QueryRowContext(ctx, `SELECT `+reportColumns+` FROM reports WHERE build_id = ?`, buildID))
	if errors.Is(err, sql.ErrNoRows) {
		return app.Record{}, app.ErrNotFound
	}
	return rec, err
}

// Reports implements app.Repository.
func (r Repo) Reports(ctx context.Context, limit int) ([]app.Record, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT `+reportColumns+` FROM reports ORDER BY checked_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query reports: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	var out []app.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reports: %w", err)
	}
	return out, nil
}

// Active implements app.Repository.
func (r Repo) Active(ctx context.Context) (app.Record, bool, error) {
	rec, err := scanRecord(r.DB.QueryRowContext(ctx, `SELECT `+reportColumns+` FROM reports
		WHERE verdict = ? ORDER BY checked_at DESC, rowid DESC LIMIT 1`, app.VerdictValidated))
	if errors.Is(err, sql.ErrNoRows) {
		return app.Record{}, false, nil
	}
	if err != nil {
		return app.Record{}, false, err
	}
	return rec, true, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanRecord(s scanner) (app.Record, error) {
	var (
		rec     app.Record
		checked string
		checks  string
	)
	if err := s.Scan(&rec.BuildID, &rec.Verdict, &checked, &rec.Artifact, &checks); err != nil {
		return app.Record{}, fmt.Errorf("scan report: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil {
		return app.Record{}, fmt.Errorf("parse checked_at %q: %w", checked, err)
	}
	rec.CheckedAt = t
	if err := json.Unmarshal([]byte(checks), &rec.Checks); err != nil {
		return app.Record{}, fmt.Errorf("decode checks of %s: %w", rec.BuildID, err)
	}
	if rec.Checks == nil {
		rec.Checks = []catalog.Check{}
	}
	return rec, nil
}
