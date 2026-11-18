package cosmosdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"constella/internal/domain/catalog"
)

func sampleDataset() catalog.Dataset {
	ci := 0.65
	stars := []catalog.Star{
		{ID: 0, Name: "Sol", Mag: -26.7, Spect: "G2V", CI: &ci},
		{ID: 1, HIP: 100, Con: "Ori", Name: "Alpha", Bayer: "Alp", Mag: 0.5, RA: 5.9, Dec: 7.4},
		{ID: 2, HIP: 200, Con: "Ori", Mag: 3.1, RA: 5.5, Dec: -1.2},
		{ID: 3, HIP: 300, Con: "CMa", Name: "Sirius", Mag: -1.44},
	}
	cultures := []catalog.Culture{{
		ID: catalog.IAUCultureID, Region: "World", Classification: []string{"traditional"},
		Constellations: []catalog.CultureConstellation{
			{ID: "Ori", NameEN: "Orion", NameNative: "Orion", Polylines: [][]int64{{100, 200}, {200, 999}}},
		},
	}}
	return catalog.NewDataset(stars, cultures, map[int64]string{300: "Сіріус"})
}

func TestWriteThenInspect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cosmos.db")
	ds := sampleDataset()

	w, err := Write(ctx, path, ds)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.ContentHash != catalog.HashDataset(ds) || len(w.SHA256) != 64 || w.Size == 0 {
		t.Errorf("unexpected result %+v", w)
	}

	snap, err := Inspect(ctx, path, w.SHA256)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if snap.StoredContentHash != snap.ComputedContentHash || snap.StoredContentHash != w.ContentHash {
		t.Errorf("content hash: stored %s, computed %s, written %s",
			snap.StoredContentHash, snap.ComputedContentHash, w.ContentHash)
	}
	if snap.ActualSHA256 != w.SHA256 || snap.SchemaVersion != catalog.SchemaVersion {
		t.Errorf("file hash or schema wrong: %+v", snap)
	}
	if snap.Constellations != 2 || snap.NoBrightest != 0 || snap.Cultures != 1 || snap.Edges != 1 ||
		snap.EdgesNoStar != 0 || snap.DroppedEdges != 1 {
		t.Errorf("counts: %+v", snap)
	}
	if snap.TierCounts[0] != 4 { // all four stars, Sol included, are brighter than 5
		t.Errorf("tier 0 = %d, want 4", snap.TierCounts[0])
	}
}

func TestInspectDetectsTampering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cosmos.db")
	w, err := Write(ctx, path, sampleDataset())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	db, err := sql.Open("sqlite", uri(path, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE stars SET mag = 9.9 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO culture_edges VALUES ('modern_iau', 'Ori', 5, 0, 100, 424242)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := Inspect(ctx, path, w.SHA256)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	report := catalog.Validate(snap)
	failed := map[string]bool{}
	for _, c := range report.Failed() {
		failed[c] = true
	}
	for _, want := range []string{catalog.CheckArtifactSHA, catalog.CheckContentHash, catalog.CheckEdgeStars} {
		if !failed[want] {
			t.Errorf("check %s should fail; failed=%v", want, report.Failed())
		}
	}
}

func TestInspectRejectsForeignFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", uri(path, "rwc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (x)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, path, ""); err == nil {
		t.Fatal("Inspect accepted a database without cosmos tables")
	}
}
