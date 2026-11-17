// Package sqlitex opens the per-service SQLite databases with the settings
// every service needs (WAL, busy timeout, full sync) and runs transactions.
package sqlitex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	// Pure-Go SQLite driver (no CGO), registered as "sqlite".
	_ "modernc.org/sqlite"
)

// Open opens (and creates if needed) the database file at path and applies
// the schema statements in order. Use ":memory:" for tests.
//
// The pool is limited to one connection: every service is small, and a single
// writer removes SQLITE_BUSY races between the outbox relay and request
// handlers.
func Open(ctx context.Context, path string, schema ...string) (*sql.DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir for %s: %w", path, err)
		}
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping sqlite %s: %w", path, err), db.Close())
	}
	for i, stmt := range schema {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return nil, errors.Join(fmt.Errorf("apply schema statement %d: %w", i, err), db.Close())
		}
	}
	return db, nil
}

func dsn(path string) string {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"synchronous(FULL)",
		"busy_timeout(5000)",
		"foreign_keys(ON)",
	} {
		q.Add("_pragma", p)
	}
	return "file:" + path + "?" + q.Encode()
}

// InTx runs fn inside a transaction and commits it when fn returns nil.
func InTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
