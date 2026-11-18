package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"constella/internal/domain/release"
	"constella/internal/platform/sqlitex"
	"constella/services/release/internal/app"
)

func shaOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestPublisherSwitchRestoreAndVerify(t *testing.T) {
	t.Parallel()
	artifacts, releases := t.TempDir(), t.TempDir()
	content := []byte("cosmos bytes")
	if err := os.WriteFile(filepath.Join(artifacts, "cosmos-b1.db"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	p := &FilePublisher{ArtifactsDir: artifacts, ReleasesDir: releases}
	ctx := context.Background()
	art := app.Reserved{Artifact: "../../etc/cosmos-b1.db", SHA256: shaOf(content), SizeBytes: int64(len(content))}

	prev, err := p.Switch(ctx, "r1", art, time.Now()) // the path part of the name is ignored
	if err != nil || prev != nil {
		t.Fatalf("first switch: prev %q err %v", prev, err)
	}
	var m Manifest
	raw, _ := p.Current(ctx)
	if err := json.Unmarshal(raw, &m); err != nil || m.Data.Version != "b1" || m.Data.SHA256 != art.SHA256 || m.Data.URL != "data/cosmos-b1.db" {
		t.Fatalf("manifest: %s (err %v)", raw, err)
	}
	if b, err := os.ReadFile(filepath.Join(releases, "data", "cosmos-b1.db")); err != nil || string(b) != string(content) {
		t.Errorf("copied file: %q (err %v)", b, err)
	}

	// A second release whose hash is wrong must leave the first one published.
	bad := app.Reserved{Artifact: "cosmos-b1.db", SHA256: strings.Repeat("0", 64)}
	if _, err := p.Switch(ctx, "r2", bad, time.Now()); err == nil {
		t.Fatal("a wrong sha256 was accepted")
	}
	if now, _ := p.Current(ctx); string(now) != string(raw) {
		t.Error("manifest changed after a failed switch")
	}

	// Restore puts the old manifest back, or removes it when there was none.
	prev, err = p.Switch(ctx, "r3", art, time.Now())
	if err != nil || string(prev) != string(raw) {
		t.Fatalf("second switch: err %v", err)
	}
	if err := p.Restore(ctx, prev); err != nil {
		t.Fatal(err)
	}
	if now, _ := p.Current(ctx); string(now) != string(raw) {
		t.Error("restore did not bring the previous manifest back")
	}
	if err := p.Restore(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if now, _ := p.Current(ctx); now != nil {
		t.Errorf("manifest still there: %s", now)
	}
}

func TestEventStoreOptimisticConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlitex.Open(ctx, ":memory:", Schema...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := Store{DB: db}

	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	r, first, _ := release.Request("r1", "b1", release.ModeClassic, at)
	if err := st.Append(ctx, "r1", 0, []release.Event{first}, nil); err != nil {
		t.Fatal(err)
	}
	second, _ := r.Reserve("cosmos-b1.db", "h", 1, at)
	if err := st.Append(ctx, "r1", 0, []release.Event{second}, nil); err != ErrConflict {
		t.Errorf("stale revision: err = %v, want ErrConflict", err)
	}
	if err := st.Append(ctx, "r1", 1, []release.Event{second}, nil); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Load(ctx, "r1")
	if err != nil || len(evs) != 2 || evs[1].Type != release.EvReserved || evs[1].SHA256 != "h" {
		t.Errorf("loaded: %+v (err %v)", evs, err)
	}
	if ids, _ := st.Recent(ctx, 5); len(ids) != 1 || ids[0] != "r1" {
		t.Errorf("recent: %v", ids)
	}
}
