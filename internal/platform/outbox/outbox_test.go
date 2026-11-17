package outbox

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"constella/internal/platform/events"
	"constella/internal/platform/sqlitex"
)

// fakePublisher fails while failing is true and records what it published.
type fakePublisher struct {
	failing bool
	got     []string
}

func (f *fakePublisher) Publish(_ context.Context, env events.Envelope) error {
	if f.failing {
		return errors.New("broker unavailable")
	}
	f.got = append(f.got, env.ID)
	return nil
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), filepath.Join(t.TempDir(), "svc.db"), Schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func addEvents(t *testing.T, db *sql.DB, n int, failTx bool) []string {
	t.Helper()
	var ids []string
	err := sqlitex.InTx(context.Background(), db, func(tx *sql.Tx) error {
		for range n {
			env, err := events.New(events.ReleasePublished, "test", map[string]string{"v": "1"})
			if err != nil {
				return err
			}
			if err := Add(context.Background(), tx, env); err != nil {
				return err
			}
			ids = append(ids, env.ID)
		}
		if failTx {
			return errors.New("state change failed")
		}
		return nil
	})
	if failTx {
		if err == nil {
			t.Fatal("want tx error")
		}
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestRolledBackTxLeavesNoEvent(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	addEvents(t, db, 2, true)
	pending, sent, err := Stats(context.Background(), db)
	if err != nil || pending != 0 || sent != 0 {
		t.Fatalf("stats = %d/%d, %v; want 0/0", pending, sent, err)
	}
}

func TestRelayKeepsEventsWhileBrokerIsDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openDB(t)
	ids := addEvents(t, db, 3, false)
	pub := &fakePublisher{failing: true}
	relay := NewRelay(db, pub, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)

	if n := relay.Flush(ctx); n != 0 {
		t.Fatalf("published %d while broker down", n)
	}
	if pending, _, _ := Stats(ctx, db); pending != 3 {
		t.Fatalf("pending = %d, want 3", pending)
	}

	pub.failing = false
	if n := relay.Flush(ctx); n != 3 {
		t.Fatalf("published %d after recovery, want 3", n)
	}
	for i, id := range ids {
		if pub.got[i] != id {
			t.Fatalf("order broken at %d: got %s want %s", i, pub.got[i], id)
		}
	}
	if pending, sent, _ := Stats(ctx, db); pending != 0 || sent != 3 {
		t.Fatalf("stats = %d/%d, want 0/3", pending, sent)
	}
	if n := relay.Flush(ctx); n != 0 {
		t.Fatalf("republished %d already sent events", n)
	}
}
