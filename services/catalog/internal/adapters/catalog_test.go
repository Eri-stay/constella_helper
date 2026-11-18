package adapters

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"constella/internal/cosmosdb"
	"constella/internal/domain/catalog"
	"constella/internal/platform/events"
	"constella/internal/platform/httpx"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
	"constella/services/catalog/internal/app"
)

// sampleCosmos writes a small cosmos.db and returns its directory and file.
func sampleCosmos(t *testing.T) (dir, file string) {
	t.Helper()
	stars := []catalog.Star{
		{ID: 0, Name: "Sol", Mag: -26.7},
		{ID: 1, HIP: 32349, Name: "Sirius", Bayer: "Alp", Con: "CMa", Mag: -1.44, RA: 6.75, Dec: -16.7},
		{ID: 2, HIP: 30324, Bayer: "Bet", Con: "CMa", Mag: 1.98, RA: 6.38, Dec: -17.96},
		{ID: 3, HIP: 27989, Name: "Betelgeuse", Bayer: "Alp", Con: "Ori", Mag: 0.45, RA: 5.92, Dec: 7.4},
		{ID: 4, HIP: 100, Name: "Under_score", Con: "Ori", Mag: 5},
	}
	cultures := []catalog.Culture{{
		ID: catalog.IAUCultureID, Region: "World", Classification: []string{"traditional"},
		Constellations: []catalog.CultureConstellation{
			{ID: "CMa", NameEN: "Greater Dog", NameNative: "Canis Major", Polylines: [][]int64{{32349, 30324}}},
			{ID: "Ori", NameEN: "Orion", NameNative: "Orion", Polylines: [][]int64{{27989, 100}}},
		},
	}}
	ds := catalog.NewDataset(stars, cultures, map[int64]string{32349: "Сіріус"})
	dir = t.TempDir()
	if _, err := cosmosdb.Write(context.Background(), filepath.Join(dir, "cosmos-b1.db"), ds); err != nil {
		t.Fatal(err)
	}
	return dir, "cosmos-b1.db"
}

func activeIndex(t *testing.T) *Index {
	t.Helper()
	dir, file := sampleCosmos(t)
	ix := &Index{}
	t.Cleanup(func() { _ = ix.Close() })
	if err := ix.Activate(filepath.Join(dir, file)); err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestSearchStars(t *testing.T) {
	t.Parallel()
	ix := activeIndex(t)
	tests := []struct {
		q    string
		want []int64 // star ids in order
	}{
		{"сіріус", []int64{1}}, // Ukrainian name
		{"SIRIUS", []int64{1}},
		{"hip 32349", []int64{1}},
		{"HIP32349", []int64{1}},
		{"32349", []int64{1}},
		{"alp", []int64{1, 3}}, // Bayer, brightest first
		{"cma", []int64{1, 2}}, // constellation
		{"under_", []int64{4}}, // "_" is literal, not a wildcard
		{"%", nil},             // "%" is literal too
		{"nothing here", nil},
		{"", []int64{0, 1, 3}}, // no query: brightest first (limit 3)
	}
	for _, tc := range tests {
		got, err := ix.SearchStars(context.Background(), tc.q, 3)
		if err != nil {
			t.Errorf("%q: %v", tc.q, err)
			continue
		}
		var ids []int64
		for _, s := range got {
			ids = append(ids, s.ID)
		}
		if len(ids) != len(tc.want) {
			t.Errorf("%q: got %v, want %v", tc.q, ids, tc.want)
			continue
		}
		for i := range ids {
			if ids[i] != tc.want[i] {
				t.Errorf("%q: got %v, want %v", tc.q, ids, tc.want)
				break
			}
		}
	}
}

func TestIndexQueries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ix := activeIndex(t)

	cons, err := ix.Constellations(ctx)
	if err != nil || len(cons) != 2 || cons[0].Abbr != "CMa" || cons[0].Brightest.Name != "Sirius" || cons[0].NameEN != "Greater Dog" {
		t.Errorf("constellations: %+v (err %v)", cons, err)
	}
	cultures, err := ix.Cultures(ctx)
	if err != nil || len(cultures) != 1 || cultures[0].Constellations != 2 || cultures[0].Edges != 2 {
		t.Errorf("cultures: %+v (err %v)", cultures, err)
	}
	fig, err := ix.FigureLines(ctx, catalog.IAUCultureID, "CMa")
	if err != nil || len(fig.Lines) != 1 || len(fig.Stars) != 2 || fig.NameNative != "Canis Major" {
		t.Errorf("figure: %+v (err %v)", fig, err)
	}
	if _, err := ix.FigureLines(ctx, catalog.IAUCultureID, "Nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown figure: err = %v, want ErrNotFound", err)
	}
	if _, err := ix.CultureFigures(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown culture: err = %v, want ErrNotFound", err)
	}
	if _, err := (&Index{}).SearchStars(ctx, "x", 1); !errors.Is(err, ErrNoActive) {
		t.Errorf("empty index: err = %v, want ErrNoActive", err)
	}
}

// fixedInspector returns a chosen snapshot, so the verdict logic can be tested
// without a 120 000-star file.
type fixedInspector struct {
	snap catalog.Snapshot
	err  error
}

func (f fixedInspector) Inspect(context.Context, string, string) (catalog.Snapshot, error) {
	return f.snap, f.err
}

type noNotify struct{}

func (noNotify) Notify() {}

func goodSnapshot() catalog.Snapshot {
	counts := make([]int, len(catalog.Tiers))
	for i, tier := range catalog.Tiers {
		counts[i] = tier.Expected
	}
	return catalog.Snapshot{
		SchemaVersion: catalog.SchemaVersion, ExpectedSHA256: "a", ActualSHA256: "a",
		StoredContentHash: "h", ComputedContentHash: "h",
		TierCounts: counts, Constellations: catalog.IAUConstellations, Cultures: 3, Edges: 10,
	}
}

func completedEvent(t *testing.T, file string) events.Envelope {
	t.Helper()
	env, err := events.New(events.BuildCompleted, "dataprep",
		events.BuildCompletedData{BuildID: "b1", Artifact: file, SHA256: "a"})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func newCatalogService(t *testing.T, ins app.Inspector, dir string) (*app.Service, Repo, *Index) {
	t.Helper()
	db, err := sqlitex.Open(context.Background(), ":memory:", Schema...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := Repo{DB: db}
	ix := &Index{}
	t.Cleanup(func() { _ = ix.Close() })
	return app.New(repo, ins, ix, noNotify{}, dir, slog.New(slog.NewTextHandler(io.Discard, nil))), repo, ix
}

func TestHandleBuildCompletedValidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir, file := sampleCosmos(t)
	svc, repo, ix := newCatalogService(t, fixedInspector{snap: goodSnapshot()}, dir)
	env := completedEvent(t, file)

	if err := svc.HandleBuildCompleted(ctx, env); err != nil {
		t.Fatal(err)
	}
	rec, err := svc.Report(ctx, "b1")
	if err != nil || rec.Verdict != app.VerdictValidated {
		t.Fatalf("report: %+v (err %v)", rec, err)
	}
	if _, ok, _ := svc.Active(ctx); !ok {
		t.Error("validated build is not active")
	}
	if got, err := ix.SearchStars(ctx, "сіріус", 5); err != nil || len(got) != 1 {
		t.Errorf("active index not queryable: %v (err %v)", got, err)
	}

	pending, err := outbox.Fetch(ctx, repo.DB, 10)
	if err != nil || len(pending) != 1 || pending[0].Envelope.Type != events.BuildValidated ||
		pending[0].Envelope.CausationID != env.ID {
		t.Fatalf("outbox: %+v (err %v)", pending, err)
	}

	// The broker redelivers the same event: nothing new is written.
	if err := svc.HandleBuildCompleted(ctx, env); err != nil {
		t.Fatal(err)
	}
	if pending, _ := outbox.Fetch(ctx, repo.DB, 10); len(pending) != 1 {
		t.Errorf("duplicate delivery added outbox rows: %d", len(pending))
	}
}

func TestHandleBuildCompletedRejects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir, file := sampleCosmos(t)

	bad := goodSnapshot()
	bad.ActualSHA256 = "different"
	svc, repo, _ := newCatalogService(t, fixedInspector{snap: bad}, dir)
	if err := svc.HandleBuildCompleted(ctx, completedEvent(t, file)); err != nil {
		t.Fatal(err)
	}
	rec, _ := svc.Report(ctx, "b1")
	if rec.Verdict != app.VerdictRejected {
		t.Errorf("verdict = %s, want rejected", rec.Verdict)
	}
	if _, ok, _ := svc.Active(ctx); ok {
		t.Error("a rejected build became active")
	}
	pending, _ := outbox.Fetch(ctx, repo.DB, 10)
	if len(pending) != 1 || pending[0].Envelope.Type != events.BuildRejected {
		t.Errorf("outbox: %+v", pending)
	}
	var data events.BuildRejectedData
	if err := pending[0].Envelope.Decode(&data); err != nil || len(data.FailedChecks) != 1 || data.FailedChecks[0] != catalog.CheckArtifactSHA {
		t.Errorf("rejected data: %+v (err %v)", data, err)
	}
}

func TestHandleBuildCompletedMissingArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newCatalogService(t, Inspector{}, t.TempDir())
	if err := svc.HandleBuildCompleted(ctx, completedEvent(t, "cosmos-missing.db")); err != nil {
		t.Fatalf("a missing artifact is a verdict, not an error: %v", err)
	}
	rec, err := svc.Report(ctx, "b1")
	if err != nil || rec.Verdict != app.VerdictRejected || rec.Checks[0].Code != app.CheckArtifactMissing {
		t.Errorf("report: %+v (err %v)", rec, err)
	}
}

func TestHandleBuildCompletedTransientError(t *testing.T) {
	t.Parallel()
	svc, _, _ := newCatalogService(t, fixedInspector{err: errors.New("disk hiccup")}, t.TempDir())
	if err := svc.HandleBuildCompleted(context.Background(), completedEvent(t, "x.db")); err == nil {
		t.Error("a transient error must be returned so the broker retries")
	}
}

func TestRestoreReopensActiveCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir, file := sampleCosmos(t)
	svc, repo, _ := newCatalogService(t, fixedInspector{snap: goodSnapshot()}, dir)
	if err := svc.HandleBuildCompleted(ctx, completedEvent(t, file)); err != nil {
		t.Fatal(err)
	}

	// A new process: same database, empty index.
	ix2 := &Index{}
	t.Cleanup(func() { _ = ix2.Close() })
	svc2 := app.New(repo, fixedInspector{}, ix2, noNotify{}, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := ix2.SearchStars(ctx, "betelgeuse", 5); err != nil || len(got) != 1 {
		t.Errorf("restored index: %v (err %v)", got, err)
	}
}

func TestHTTPNoActiveCatalog(t *testing.T) {
	t.Parallel()
	svc, _, ix := newCatalogService(t, fixedInspector{}, t.TempDir())
	mux := http.NewServeMux()
	Routes(mux, svc, ix)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stars?q=sirius", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeNoCatalog) {
		t.Errorf("stars without catalog: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stars?limit=0", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), httpx.CodeBadRequest) {
		t.Errorf("bad limit: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/summary", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"active":false`) {
		t.Errorf("summary without catalog: %d %s", rec.Code, rec.Body.String())
	}
}
