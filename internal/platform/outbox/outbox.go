// Package outbox implements the transactional outbox: an event is stored in
// the same SQLite transaction as the state change that produced it, and a
// relay publishes stored events to the broker afterwards.
//
// This is how "append first, then dispatch" is kept across a process crash or
// a broker outage: nothing is published that was not committed, and nothing
// committed is lost.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"constella/internal/platform/events"
)

// Schema creates the outbox table. Services pass it to sqlitex.Open.
const Schema = `CREATE TABLE IF NOT EXISTS outbox (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT    NOT NULL UNIQUE,
	type       TEXT    NOT NULL,
	envelope   BLOB    NOT NULL,
	trace      TEXT    NOT NULL DEFAULT '{}',
	created_at TEXT    NOT NULL,
	sent_at    TEXT,
	attempts   INTEGER NOT NULL DEFAULT 0,
	last_error TEXT
)`

// Add stores env inside tx. The trace context of ctx is saved with it so the
// relay can continue the same trace when it publishes later.
func Add(ctx context.Context, tx *sql.Tx, env events.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope %s: %w", env.ID, err)
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	trace, err := json.Marshal(carrier)
	if err != nil {
		return fmt.Errorf("marshal trace carrier: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO outbox (id, type, envelope, trace, created_at) VALUES (?, ?, ?, ?, ?)`,
		env.ID, env.Type, body, string(trace), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert outbox %s: %w", env.ID, err)
	}
	return nil
}

// Pending is one stored, not yet published event.
type Pending struct {
	Seq      int64
	Envelope events.Envelope
	Trace    propagation.MapCarrier
	Attempts int
}

// Fetch returns up to limit unsent events in commit order.
func Fetch(ctx context.Context, db *sql.DB, limit int) ([]Pending, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT seq, envelope, trace, attempts FROM outbox WHERE sent_at IS NULL ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query outbox: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only cursor; Err() below reports failures

	var out []Pending
	for rows.Next() {
		var (
			p           Pending
			body, trace []byte
		)
		if err := rows.Scan(&p.Seq, &body, &trace, &p.Attempts); err != nil {
			return nil, fmt.Errorf("scan outbox row: %w", err)
		}
		if err := json.Unmarshal(body, &p.Envelope); err != nil {
			return nil, fmt.Errorf("decode outbox row %d: %w", p.Seq, err)
		}
		if err := json.Unmarshal(trace, &p.Trace); err != nil {
			return nil, fmt.Errorf("decode trace of outbox row %d: %w", p.Seq, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox: %w", err)
	}
	return out, nil
}

// MarkSent records a successful publish.
func MarkSent(ctx context.Context, db *sql.DB, seq int64) error {
	_, err := db.ExecContext(ctx, `UPDATE outbox SET sent_at = ?, attempts = attempts + 1 WHERE seq = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), seq)
	if err != nil {
		return fmt.Errorf("mark outbox %d sent: %w", seq, err)
	}
	return nil
}

// MarkFailed records a failed publish attempt; the row stays pending.
func MarkFailed(ctx context.Context, db *sql.DB, seq int64, cause error) error {
	_, err := db.ExecContext(ctx, `UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE seq = ?`,
		cause.Error(), seq)
	if err != nil {
		return fmt.Errorf("mark outbox %d failed: %w", seq, err)
	}
	return nil
}

// Stats counts pending and sent events; used by health and debug endpoints.
func Stats(ctx context.Context, db *sql.DB) (pending, sent int, err error) {
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FILTER (WHERE sent_at IS NULL), COUNT(*) FILTER (WHERE sent_at IS NOT NULL) FROM outbox`).
		Scan(&pending, &sent)
	if err != nil {
		return 0, 0, fmt.Errorf("count outbox: %w", err)
	}
	return pending, sent, nil
}
