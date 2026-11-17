package inbox

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"constella/internal/platform/sqlitex"
)

func TestProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlitex.Open(ctx, filepath.Join(t.TempDir(), "svc.db"), Schema,
		`CREATE TABLE audit (n INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	insert := func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO audit VALUES (1)`)
		return err
	}
	failing := func(tx *sql.Tx) error {
		if err := insert(tx); err != nil {
			return err
		}
		return errors.New("handler failed")
	}

	steps := []struct {
		name     string
		consumer string
		eventID  string
		fn       func(*sql.Tx) error
		wantDup  bool
		wantErr  bool
		wantRows int
	}{
		{"first delivery", "telemetry", "e1", insert, false, false, 1},
		{"redelivery is skipped", "telemetry", "e1", insert, true, false, 1},
		{"other consumer sees it once", "gateway", "e1", insert, false, false, 2},
		{"failed handler records nothing", "telemetry", "e2", failing, false, true, 2},
		{"retry after failure runs", "telemetry", "e2", insert, false, false, 3},
	}
	for _, s := range steps {
		dup, err := Process(ctx, db, s.consumer, s.eventID, s.fn)
		if (err != nil) != s.wantErr || dup != s.wantDup {
			t.Fatalf("%s: dup=%v err=%v, want dup=%v err=%v", s.name, dup, err, s.wantDup, s.wantErr)
		}
		var rows int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != s.wantRows {
			t.Fatalf("%s: audit rows = %d, want %d", s.name, rows, s.wantRows)
		}
	}
}
