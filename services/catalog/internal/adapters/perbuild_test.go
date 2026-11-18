package adapters

import (
	"context"
	"path/filepath"
	"testing"

	"constella/internal/cosmosdb"
	"constella/internal/domain/catalog"
)

// Two builds with different content: a query scoped to a build must read that
// build, not the active one.
func TestQueriesCanBeScopedToABuild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name string, cultureIDs ...string) string {
		var cultures []catalog.Culture
		for _, id := range cultureIDs {
			cultures = append(cultures, catalog.Culture{ID: id, Constellations: []catalog.CultureConstellation{
				{ID: "x", Polylines: [][]int64{{1, 2}}}}})
		}
		stars := []catalog.Star{{ID: 1, HIP: 1, Con: "Ori", Mag: 1}, {ID: 2, HIP: 2, Con: "Ori", Mag: 2}}
		path := filepath.Join(dir, name)
		if _, err := cosmosdb.Write(context.Background(), path, catalog.NewDataset(stars, cultures, nil)); err != nil {
			t.Fatal(err)
		}
		return path
	}
	paths := map[string]string{"old": write("old.db", "maya"), "new": write("new.db", "maya", "norse")}

	ix := &Index{Resolve: func(_ context.Context, id string) (string, error) {
		if p, ok := paths[id]; ok {
			return p, nil
		}
		return "", ErrNotFound
	}}
	t.Cleanup(func() { _ = ix.Close() })
	if err := ix.Activate(paths["new"]); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	active, _ := ix.Cultures(ctx)
	old, err := ix.Cultures(WithBuild(ctx, "old"))
	if err != nil || len(active) != 2 || len(old) != 1 || old[0].ID != "maya" {
		t.Errorf("active %d cultures, old %+v (err %v)", len(active), old, err)
	}
	if _, err := ix.Cultures(WithBuild(ctx, "missing")); err != ErrNotFound {
		t.Errorf("unknown build: %v, want ErrNotFound", err)
	}
}
