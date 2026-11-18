// Package adapters connects release to SQLite, files, other services and HTTP.
package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"constella/internal/domain/release"
	"constella/internal/platform/events"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
)

// Schema is the release database: the event store of the Release aggregate.
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS release_events (
		release_id TEXT    NOT NULL,
		revision   INTEGER NOT NULL,
		type       TEXT    NOT NULL,
		at         TEXT    NOT NULL,
		data       TEXT    NOT NULL,
		PRIMARY KEY (release_id, revision)
	) WITHOUT ROWID`,
	outbox.Schema,
}

// ErrConflict is returned when another writer appended to the stream first.
var ErrConflict = errors.New("release stream changed concurrently")

// Store is the event store (release.db).
type Store struct{ DB *sql.DB }

// Append implements app.Store. The primary key enforces the expected revision.
func (s Store) Append(ctx context.Context, id string, revision int, evs []release.Event, out []events.Envelope) error {
	err := sqlitex.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		for i, e := range evs {
			data, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("marshal event: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO release_events (release_id, revision, type, at, data)
				VALUES (?, ?, ?, ?, ?)`, id, revision+i, string(e.Type), e.At.Format(time.RFC3339Nano), string(data)); err != nil {
				if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
					return ErrConflict
				}
				return fmt.Errorf("insert event %s/%d: %w", id, revision+i, err)
			}
		}
		// Integration events join the same transaction: the outbox guarantee.
		for _, env := range out {
			if err := outbox.Add(ctx, tx, env); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// Load implements app.Store.
func (s Store) Load(ctx context.Context, id string) ([]release.Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT data FROM release_events WHERE release_id = ? ORDER BY revision`, id)
	if err != nil {
		return nil, fmt.Errorf("load events of %s: %w", id, err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	var out []release.Event
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		var e release.Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return nil, fmt.Errorf("decode event of %s: %w", id, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return out, nil
}

// Recent implements app.Store: newest streams first (ids sort by time).
func (s Store) Recent(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT release_id FROM release_events WHERE revision = 0 ORDER BY release_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan release id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Open implements app.Store: event-mode streams without a final event.
func (s Store) Open(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT release_id FROM release_events e
		WHERE revision = 0 AND data LIKE '%"mode":"eda"%'
		AND NOT EXISTS (SELECT 1 FROM release_events f WHERE f.release_id = e.release_id AND f.type IN ('published', 'failed'))
		ORDER BY release_id`)
	if err != nil {
		return nil, fmt.Errorf("list open releases: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan release id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
