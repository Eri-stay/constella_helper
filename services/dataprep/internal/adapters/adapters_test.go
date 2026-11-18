package adapters

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"constella/internal/cosmosdb"
	"constella/internal/domain/catalog"
	"constella/internal/domain/compare"
	"constella/internal/domain/genconst"
	"constella/internal/platform/events"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
	"constella/services/dataprep/internal/app"
)

const hygSample = `"id","hip","hd","hr","gl","bf","proper","ra","dec","dist","pmra","pmdec","rv","mag","absmag","spect","ci","x","y","z","vx","vy","vz","rarad","decrad","pmrarad","pmdecrad","bayer","flam","con","comp","comp_primary","base","lum","var","var_min","var_max"
0,,,,"","",Sol,0.0,0.0,0.0,0.0,0.0,0.0,-26.7,4.85,G2V,0.656,0,0,0,0,0,0,0,0,0,0,"","","",1,0,"",1.0,"",,
32349,32349,48915,2491,"Gl 244A","9Alp CMa",Sirius,6.752481,-16.716116,2.6371,-546.01,-1223.07,-5.5,-1.44,1.454,A0m,0.009,-0.49,2.47,-0.76,0,0,0,1.767,-0.29,0,0,Alp,9,CMa,1,32349,"",25.4,"",,
99,,,,"","","",1.5,2.5,100.0,1,2,3,9.5,5.0,,,0,0,0,0,0,0,0,0,0,0,"","","",1,99,"",1.0,"",,
`

func TestReadHYG(t *testing.T) {
	t.Parallel()
	stars, err := ReadHYG(strings.NewReader(hygSample))
	if err != nil {
		t.Fatal(err)
	}
	if len(stars) != 3 {
		t.Fatalf("got %d stars, want 3", len(stars))
	}
	sirius := stars[1]
	if sirius.HIP != 32349 || sirius.Name != "Sirius" || sirius.Con != "CMa" || sirius.Bayer != "Alp" ||
		sirius.Flam != 9 || sirius.Mag != -1.44 || sirius.RA != 6.752481 || sirius.CI == nil || *sirius.CI != 0.009 {
		t.Errorf("Sirius parsed wrong: %+v", sirius)
	}
	if stars[0].HIP != 0 || stars[0].Con != "" || stars[2].CI != nil {
		t.Errorf("empty fields: sol=%+v faint=%+v", stars[0], stars[2])
	}
}

func TestReadHYGErrors(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"missing column": "id,hip\n1,2\n",
		"bad number":     "id,hip,proper,ra,dec,mag,con,bayer,flam,spect,ci\nx,,,0,0,0,,,,,\n",
	}
	for name, in := range tests {
		if _, err := ReadHYG(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestParseSkyculture(t *testing.T) {
	t.Parallel()
	data := `{"id":"demo","region":"Nowhere","classification":["historical"],"constellations":[
		{"id":"CON demo 001","lines":[[10,20,"DSO:M42",30],[[-5.5,-12.4],40,50]],
		 "common_name":{"english":"Bull","native":"Taurus"}},
		{"id":"CON demo 002","lines":[[7,8]]}]}`
	cu, err := ParseSkyculture([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := catalog.Culture{ID: "demo", Region: "Nowhere", Classification: []string{"historical"},
		Constellations: []catalog.CultureConstellation{
			{ID: "001", NameEN: "Bull", NameNative: "Taurus", Polylines: [][]int64{{10, 20, 0, 30}, {0, 40, 50}}},
			{ID: "002", Polylines: [][]int64{{7, 8}}},
		}}
	if !reflect.DeepEqual(cu, want) {
		t.Errorf("got %+v\nwant %+v", cu, want)
	}
	if _, err := ParseSkyculture([]byte(`{"constellations":[]}`)); err == nil {
		t.Error("a culture without id was accepted")
	}
}

func TestReadNamesUK(t *testing.T) {
	t.Parallel()
	got, err := ReadNamesUK(strings.NewReader("hip,name_uk\n32349,Сіріус\n11767,Полярна зоря\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[32349] != "Сіріус" || got[11767] != "Полярна зоря" || len(got) != 2 {
		t.Errorf("got %v", got)
	}
	if _, err := ReadNamesUK(strings.NewReader("a,b\n1,2\n")); err == nil {
		t.Error("a wrong header was accepted")
	}
}

// TestRepositoryCultures loads the committed cultures and checks what the
// platform relies on: 10–20 cultures, each with figures and the IAU one with 88.
func TestRepositoryCultures(t *testing.T) {
	t.Parallel()
	src := FileSources{CulturesDir: filepath.Join("..", "..", "..", "..", "testdata", "cultures")}
	cultures, err := src.loadCultures()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cultures); n < 10 || n > 20 {
		t.Errorf("%d cultures, want 10..20", n)
	}
	var iau *catalog.Culture
	for i, cu := range cultures {
		if len(cu.Constellations) == 0 {
			t.Errorf("culture %s has no constellations", cu.ID)
		}
		if cu.ID == catalog.IAUCultureID {
			iau = &cultures[i]
		}
	}
	if iau == nil || len(iau.Constellations) != catalog.IAUConstellations {
		t.Fatalf("modern_iau missing or not 88 constellations: %+v", iau)
	}
}

func TestStoreLifecycleAndOutbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlitex.Open(ctx, ":memory:", Schema...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := Store{DB: db}

	started := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	b := app.Build{ID: "b1", Status: app.StatusRunning, StartedAt: started}
	ev, _ := events.New(events.BuildStarted, "dataprep", events.BuildStartedData{BuildID: "b1"})
	if err := st.Create(ctx, b, ev); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, app.Build{ID: "b2", Status: app.StatusRunning, StartedAt: started}, ev); err != app.ErrBusy {
		t.Errorf("second running build: err = %v, want ErrBusy", err)
	}

	end := started.Add(3 * time.Second)
	b.Status, b.FinishedAt, b.Artifact, b.Stars = app.StatusCompleted, &end, "cosmos-b1.db", 119626
	done, _ := events.New(events.BuildCompleted, "dataprep", events.BuildCompletedData{BuildID: "b1"})
	if err := st.Finish(ctx, b, done); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != app.StatusCompleted || got.Stars != 119626 || got.FinishedAt == nil || !got.FinishedAt.Equal(end) {
		t.Errorf("stored build: %+v", got)
	}
	if _, err := st.Get(ctx, "nope"); err != app.ErrNotFound {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	pending, _, err := outbox.Stats(ctx, db)
	if err != nil || pending != 2 {
		t.Errorf("outbox pending = %d (err %v), want 2 (started + completed)", pending, err)
	}
}

// TestPipelineOnRealData runs the whole pipeline on the real HYG file and
// checks the result against the documented tier counts. It needs the file that
// is never committed: set CONSTELLA_HYG, or keep it at ../constella/data.
func TestPipelineOnRealData(t *testing.T) {
	hyg := os.Getenv("CONSTELLA_HYG")
	if hyg == "" {
		hyg = filepath.Join("..", "..", "..", "..", "..", "data", "hyg_v42.csv")
	}
	if _, err := os.Stat(hyg); err != nil {
		t.Skipf("HYG data not available: %v", err)
	}
	testdata := filepath.Join("..", "..", "..", "..", "testdata")
	src := FileSources{HYGPath: hyg, CulturesDir: filepath.Join(testdata, "cultures"),
		NamesUKPath: filepath.Join(testdata, "names_uk.csv")}

	ctx := context.Background()
	in, err := src.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ds := catalog.NewDataset(in.Stars, in.Cultures, in.NamesUK)
	dir := t.TempDir()
	art, err := ArtifactWriter{Dir: dir}.Write(ctx, "cosmos-test.db", ds)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := cosmosdb.Inspect(ctx, filepath.Join(dir, art.File), art.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	report := catalog.Validate(snap)
	for _, c := range report.Checks {
		t.Logf("%-16s %-5s %v", c.Code, c.Status, c.Params)
	}
	if !report.OK() {
		t.Errorf("real data failed validation: %v", report.Failed())
	}
	t.Logf("stars=%d constellations=%d cultures=%d edges=%d dropped=%d size=%d",
		len(ds.Stars), len(ds.Constellations), len(ds.Cultures), len(ds.Edges), ds.DroppedEdges, art.SizeBytes)
}

// TestResearchStrategiesOnRealData runs every generation strategy on the real
// HYG stars and compares it with the real cultures. It is a sanity check of
// the research core (not part of the service): the numbers must be sensible,
// and equal runs must be identical. Skipped without the HYG file.
func TestResearchStrategiesOnRealData(t *testing.T) {
	hyg := os.Getenv("CONSTELLA_HYG")
	if hyg == "" {
		hyg = filepath.Join("..", "..", "..", "..", "..", "data", "hyg_v42.csv")
	}
	if _, err := os.Stat(hyg); err != nil {
		t.Skipf("HYG data not available: %v", err)
	}
	testdata := filepath.Join("..", "..", "..", "..", "testdata")
	in, err := FileSources{HYGPath: hyg, CulturesDir: filepath.Join(testdata, "cultures")}.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ds := catalog.NewDataset(in.Stars, in.Cultures, nil)
	var stars []genconst.Star
	for _, s := range ds.Stars {
		if s.HIP != 0 {
			stars = append(stars, genconst.Star{HIP: s.HIP, RA: s.RA * 15, Dec: s.Dec, Mag: s.Mag})
		}
	}
	refs := map[string][]genconst.Edge{}
	for _, e := range ds.Edges {
		refs[e.Culture] = append(refs[e.Culture], genconst.Edge{A: e.A, B: e.B})
	}

	for _, culture := range []string{"modern_iau", "tibetan", "indian"} {
		best := 0.0
		for _, strategy := range genconst.Strategies {
			p := genconst.Params{Strategy: strategy, MagMax: 5, EpsDeg: 10}
			res, err := genconst.Generate(stars, p)
			if err != nil {
				t.Fatal(err)
			}
			m := compare.Edges(res.Edges, refs[culture], res.Used)
			t.Logf("%-10s %-10s stars=%d edges=%4d clusters=%3d ref=%4d J=%.3f P=%.2f R=%.2f",
				culture, strategy, len(res.Used), len(res.Edges), res.Clusters, m.Reference, m.Jaccard, m.Precision, m.Recall)
			if len(res.Edges) == 0 || res.Clusters == 0 {
				t.Errorf("%s/%s produced nothing", culture, strategy)
			}
			best = max(best, m.Jaccard)
			again, _ := genconst.Generate(stars, p)
			if genconst.Hash(p, again) != genconst.Hash(p, res) {
				t.Errorf("%s: not deterministic on real data", strategy)
			}
		}
		if culture == "modern_iau" && best < 0.05 {
			t.Errorf("no strategy comes near the IAU figures: best Jaccard %.3f", best)
		}
	}
}
