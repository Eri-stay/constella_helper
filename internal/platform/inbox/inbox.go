// Package inbox makes event consumers idempotent: an event id is recorded in
// the same transaction as the consumer's state change, so a redelivered event
// is detected and skipped.
package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"constella/internal/platform/sqlitex"
)

// Schema creates the inbox table. Services pass it to sqlitex.Open.
const Schema = `CREATE TABLE IF NOT EXISTS inbox (
	consumer     TEXT NOT NULL,
	event_id     TEXT NOT NULL,
	processed_at TEXT NOT NULL,
	PRIMARY KEY (consumer, event_id)
)`

// Process runs fn exactly once per (consumer, eventID). It returns
// duplicate=true without calling fn when the event was already processed.
// If fn fails, nothing is recorded and the event can be retried.
func Process(ctx context.Context, db *sql.DB, consumer, eventID string, fn func(tx *sql.Tx) error) (duplicate bool, err error) {
	err = sqlitex.InTx(ctx, db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO inbox (consumer, event_id, processed_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			consumer, eventID, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("record inbox %s/%s: %w", consumer, eventID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("inbox rows affected: %w", err)
		}
		if n == 0 {
			duplicate = true
			return nil
		}
		return fn(tx)
	})
	if err != nil {
		return false, err
	}
	return duplicate, nil
}
