package catalog

import (
	"reflect"
	"testing"
)

func TestBrightestPerConstellation(t *testing.T) {
	t.Parallel()
	stars := []Star{
		{ID: 0, Name: "Sol", Mag: -26.7}, // no constellation: ignored
		{ID: 5, Con: "Ori", Mag: 0.45},
		{ID: 7, Con: "Ori", Mag: 0.18},
		{ID: 9, Con: "Ori", Mag: 0.18}, // tie: the lower id wins
		{ID: 3, Con: "CMa", Mag: -1.44},
	}
	want := map[string]int64{"Ori": 7, "CMa": 3}
	if got := BrightestPerConstellation(stars); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSplitPolyline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []int64
		want [][]int64
	}{
		{"plain", []int64{1, 2, 3}, [][]int64{{1, 2, 3}}},
		{"cut by a non-star vertex", []int64{1, 2, 0, 3, 4}, [][]int64{{1, 2}, {3, 4}}},
		{"single vertex runs vanish", []int64{1, 0, 2, 0, 3, 4}, [][]int64{{3, 4}}},
		{"empty", nil, nil},
	}
	for _, tc := range tests {
		if got := SplitPolyline(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func sampleSources() ([]Star, []Culture) {
	stars := []Star{
		{ID: 2, HIP: 200, Con: "Ori", Mag: 1},
		{ID: 1, HIP: 100, Con: "Ori", Mag: 0.5, Name: "Alpha"},
		{ID: 3, HIP: 300, Con: "CMa", Mag: 2},
		{ID: 4, Con: "CMa", Mag: 3}, // no HIP
	}
	cultures := []Culture{
		{ID: "z_culture", Constellations: []CultureConstellation{
			{ID: "b", Polylines: [][]int64{{100, 200, 999, 300}}}, // 999 is not in the table
		}},
		{ID: IAUCultureID, Region: "World", Classification: []string{"traditional"}, Constellations: []CultureConstellation{
			{ID: "Ori", NameEN: "Orion", NameNative: "Orion", Polylines: [][]int64{{100, 200, NoStar, 300, 100}}},
		}},
	}
	return stars, cultures
}

func TestNewDataset(t *testing.T) {
	t.Parallel()
	stars, cultures := sampleSources()
	ds := NewDataset(stars, cultures, map[int64]string{100: "Альфа"})

	if ds.Stars[0].ID != 1 || ds.Stars[0].NameUK != "Альфа" {
		t.Errorf("stars not sorted or Ukrainian name not applied: %+v", ds.Stars[0])
	}
	if stars[1].NameUK != "" {
		t.Error("NewDataset must not modify its input")
	}
	if ds.Cultures[0].ID != IAUCultureID {
		t.Errorf("cultures not sorted: first is %s", ds.Cultures[0].ID)
	}

	if len(ds.Constellations) != 2 || ds.Constellations[0].Abbr != "CMa" || ds.Constellations[1].Abbr != "Ori" {
		t.Fatalf("constellations: %+v", ds.Constellations)
	}
	ori := ds.Constellations[1]
	if ori.BrightestStarID != 1 || ori.NameEN != "Orion" {
		t.Errorf("Ori: %+v", ori)
	}
	if cma := ds.Constellations[0]; cma.BrightestStarID != 3 || cma.NameEN != "" {
		t.Errorf("CMa: %+v", cma)
	}

	// modern_iau: 100-200 and 300-100 (the non-star vertex cut 200-300);
	// z_culture: 100-200 ok, 200-999 and 999-300 dropped.
	wantEdges := []Edge{
		{IAUCultureID, "Ori", 0, 0, 100, 200},
		{IAUCultureID, "Ori", 0, 1, 300, 100},
		{"z_culture", "b", 0, 0, 100, 200},
	}
	if !reflect.DeepEqual(ds.Edges, wantEdges) {
		t.Errorf("edges:\n got %+v\nwant %+v", ds.Edges, wantEdges)
	}
	if ds.DroppedEdges != 2 || ds.SkippedVerts != 1 {
		t.Errorf("dropped=%d skipped=%d, want 2 and 1", ds.DroppedEdges, ds.SkippedVerts)
	}
}

func TestHashDatasetIsStable(t *testing.T) {
	t.Parallel()
	stars, cultures := sampleSources()
	a := HashDataset(NewDataset(stars, cultures, nil))
	// The same data in another input order gives the same hash.
	stars[0], stars[3] = stars[3], stars[0]
	b := HashDataset(NewDataset(stars, cultures, nil))
	if a != b || len(a) != 64 {
		t.Errorf("hash not stable: %s vs %s", a, b)
	}
	stars[1].Mag += 0.01
	if c := HashDataset(NewDataset(stars, cultures, nil)); c == a {
		t.Error("hash did not change after a magnitude changed")
	}
}

func goodSnapshot() Snapshot {
	counts := make([]int, len(Tiers))
	for i, t := range Tiers {
		counts[i] = t.Expected
	}
	return Snapshot{
		SchemaVersion:  SchemaVersion,
		ExpectedSHA256: "aa", ActualSHA256: "aa",
		StoredContentHash: "bb", ComputedContentHash: "bb",
		TierCounts: counts, Constellations: IAUConstellations,
		Cultures: 14, Edges: 3000,
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*Snapshot)
		ok       bool
		warnings int
		failed   []string
	}{
		{"good", func(*Snapshot) {}, true, 0, nil},
		{"wrong file hash", func(s *Snapshot) { s.ActualSHA256 = "zz" }, false, 0, []string{CheckArtifactSHA}},
		{"wrong content hash", func(s *Snapshot) { s.ComputedContentHash = "zz" }, false, 0, []string{CheckContentHash}},
		{"tier within 1 percent", func(s *Snapshot) { s.TierCounts[3] += 100 }, true, 1, nil},
		{"tier far off", func(s *Snapshot) { s.TierCounts[0] = 10 }, false, 0, []string{CheckStarTier}},
		{"87 constellations", func(s *Snapshot) { s.Constellations = 87 }, true, 1, nil},
		{"no brightest star", func(s *Snapshot) { s.NoBrightest = 1 }, false, 0, []string{CheckBrightest}},
		{"edge without star", func(s *Snapshot) { s.EdgesNoStar = 2 }, false, 0, []string{CheckEdgeStars}},
		{"dropped edges only warn", func(s *Snapshot) { s.DroppedEdges = 3 }, true, 1, nil},
		{"no cultures", func(s *Snapshot) { s.Cultures, s.Edges = 0, 0 }, false, 0, []string{CheckCultures}},
		{"old schema", func(s *Snapshot) { s.SchemaVersion = 0 }, false, 0, []string{CheckSchema}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := goodSnapshot()
			tc.mutate(&s)
			r := Validate(s)
			if r.OK() != tc.ok || r.Warnings() != tc.warnings || !reflect.DeepEqual(r.Failed(), tc.failed) {
				t.Errorf("ok=%v warnings=%d failed=%v; want ok=%v warnings=%d failed=%v",
					r.OK(), r.Warnings(), r.Failed(), tc.ok, tc.warnings, tc.failed)
			}
		})
	}
}
