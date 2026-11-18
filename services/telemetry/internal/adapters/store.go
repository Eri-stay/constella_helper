// Package adapters connects telemetry to SQLite, HTTP and Sentry.
package adapters

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"constella/internal/platform/inbox"
	"constella/services/telemetry/internal/app"
)

// Schema is the telemetry database: audit, client logs and the inbox.
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS audit (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		at             TEXT NOT NULL,
		source         TEXT NOT NULL,
		type           TEXT NOT NULL,
		correlation_id TEXT NOT NULL DEFAULT '',
		via            TEXT NOT NULL,
		dedupe_key     TEXT UNIQUE,
		data           TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS audit_type ON audit(type)`,
	`CREATE TABLE IF NOT EXISTS client_logs (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		at           TEXT NOT NULL,
		installation TEXT NOT NULL DEFAULT '',
		app_version  TEXT NOT NULL DEFAULT '',
		level        TEXT NOT NULL,
		message      TEXT NOT NULL,
		stack        TEXT NOT NULL DEFAULT ''
	)`,
	inbox.Schema,
}

const consumerName = "telemetry"

// Store keeps telemetry in SQLite (telemetry.db).
type Store struct{ DB *sql.DB }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

const insertAudit = `INSERT INTO audit (at, source, type, correlation_id, via, dedupe_key, data) VALUES (?, ?, ?, ?, ?, ?, ?)`

// AddAudit implements app.Store.
func (s Store) AddAudit(ctx context.Context, e app.AuditEntry, key string) (int64, bool, error) {
	var dedupe any
	if key != "" {
		dedupe = key
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO audit (at, source, type, correlation_id, via, dedupe_key, data)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(dedupe_key) DO NOTHING`,
		ts(e.At), e.Source, e.Type, e.CorrelationID, e.Via, dedupe, string(e.Data))
	if err != nil {
		return 0, false, fmt.Errorf("insert audit: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 { // RowsAffected cannot fail on SQLite
		var id int64
		if err := s.DB.QueryRowContext(ctx, `SELECT id FROM audit WHERE dedupe_key = ?`, key).Scan(&id); err != nil {
			return 0, false, fmt.Errorf("find audit by key: %w", err)
		}
		return id, false, nil
	}
	id, err := res.LastInsertId()
	return id, true, err
}

// AddEventAudit implements app.Store.
func (s Store) AddEventAudit(ctx context.Context, eventID string, e app.AuditEntry) (bool, error) {
	return inbox.Process(ctx, s.DB, consumerName, eventID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, insertAudit,
			ts(e.At), e.Source, e.Type, e.CorrelationID, e.Via, "event:"+eventID, string(e.Data))
		if err != nil {
			return fmt.Errorf("insert event audit: %w", err)
		}
		return nil
	})
}

// Audit implements app.Store.
func (s Store) Audit(ctx context.Context, limit int, typePrefix string) ([]app.AuditEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, at, source, type, correlation_id, via, data FROM audit
		WHERE type LIKE ? ESCAPE '\' ORDER BY id DESC LIMIT ?`, escapeLike(typePrefix)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out := []app.AuditEntry{}
	for rows.Next() {
		var (
			e    app.AuditEntry
			at   string
			data string
		)
		if err := rows.Scan(&e.ID, &at, &e.Source, &e.Type, &e.CorrelationID, &e.Via, &data); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("parse audit time: %w", err)
		}
		if json.Valid([]byte(data)) {
			e.Data = json.RawMessage(data)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit: %w", err)
	}
	return out, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// AddClientLog implements app.Store.
func (s Store) AddClientLog(ctx context.Context, l app.ClientLog) (app.ClientLog, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO client_logs (at, installation, app_version, level, message, stack)
		VALUES (?, ?, ?, ?, ?, ?)`, ts(l.At), l.Installation, l.AppVersion, l.Level, l.Message, l.Stack)
	if err != nil {
		return app.ClientLog{}, fmt.Errorf("insert client log: %w", err)
	}
	l.ID, err = res.LastInsertId()
	return l, err
}

// ClientLogs implements app.Store.
func (s Store) ClientLogs(ctx context.Context, limit int) ([]app.ClientLog, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, at, installation, app_version, level, message, stack
		FROM client_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query client logs: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out := []app.ClientLog{}
	for rows.Next() {
		var (
			l  app.ClientLog
			at string
		)
		if err := rows.Scan(&l.ID, &at, &l.Installation, &l.AppVersion, &l.Level, &l.Message, &l.Stack); err != nil {
			return nil, fmt.Errorf("scan client log: %w", err)
		}
		if l.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("parse client log time: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client logs: %w", err)
	}
	return out, nil
}

// Counts implements app.Store.
func (s Store) Counts(ctx context.Context) (int, int, error) {
	var a, c int
	err := s.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM audit), (SELECT COUNT(*) FROM client_logs)`).Scan(&a, &c)
	if err != nil {
		return 0, 0, fmt.Errorf("count telemetry: %w", err)
	}
	return a, c, nil
}
