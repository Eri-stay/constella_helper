package genconst

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
)

// sky returns a reproducible random sky of n stars, brighter ones first.
func sky(n int, seed uint64) []Star {
	rng := rand.New(rand.NewPCG(seed, 1))
	out := make([]Star, n)
	for i := range out {
		out[i] = Star{HIP: int64(i + 1), RA: rng.Float64() * 360, Dec: rng.Float64()*160 - 80, Mag: rng.Float64() * 5}
	}
	return out
}

func has(r Result, a, b int64) bool { return slices.Contains(r.Edges, norm(a, b)) }

// Two tight groups far apart: base joins within a group and never across.
func TestBaseClustersAndMST(t *testing.T) {
	t.Parallel()
	stars := []Star{
		{1, 10, 10, 1}, {2, 12, 10, 1}, {3, 14, 11, 1}, {4, 12, 13, 1}, // group A
		{5, 100, -20, 1}, {6, 102, -20, 1}, {7, 104, -19, 1}, // group B
		{8, 200, 40, 1}, // alone
	}
	r, err := Generate(stars, Params{Strategy: Base, EpsDeg: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Edges) != 3+2 {
		t.Errorf("a tree of 4 and a tree of 3 have 5 edges, got %v", r.Edges)
	}
	if r.Clusters != 2 || has(r, 4, 5) || len(r.Used) != 8 {
		t.Errorf("clusters %d, edges %v", r.Clusters, r.Edges)
	}
}

func TestSmallClustersAreDropped(t *testing.T) {
	t.Parallel()
	stars := []Star{{1, 10, 10, 1}, {2, 12, 10, 1}, {3, 100, 0, 1}, {4, 101, 0, 1}, {5, 102, 0, 1}}
	r, _ := Generate(stars, Params{EpsDeg: 5, MinCluster: 3})
	if has(r, 1, 2) || !has(r, 3, 4) || !has(r, 4, 5) {
		t.Errorf("a pair must be dropped when min_cluster is 3: %v", r.Edges)
	}
}

func TestMaxClusterBoundsSize(t *testing.T) {
	t.Parallel()
	var stars []Star // a chain of 10 stars, 2° apart
	for i := range 10 {
		stars = append(stars, Star{int64(i + 1), float64(i) * 2, 0, 1})
	}
	r, _ := Generate(stars, Params{EpsDeg: 3, MaxCluster: 4, MinCluster: 2})
	if r.Clusters < 3 {
		t.Errorf("10 stars in clusters of at most 4 need 3+ clusters, got %d: %v", r.Clusters, r.Edges)
	}
}

func TestDeterminismAndSeed(t *testing.T) {
	t.Parallel()
	stars := sky(300, 7)
	for _, strategy := range Strategies {
		p := Params{Strategy: strategy, EpsDeg: 12, Seed: 42, JitterDeg: 0.5}
		a, _ := Generate(stars, p)
		b, _ := Generate(stars, p)
		if Hash(p, a) != Hash(p, b) {
			t.Errorf("%s: the same seed gave different hashes", strategy)
		}
		p2 := p
		p2.Seed = 43
		c, _ := Generate(stars, p2)
		if Hash(p2, c) == Hash(p, a) {
			t.Errorf("%s: another seed with jitter should change the result", strategy)
		}
		// Without jitter the seed has nothing to act on, but it is part of the hash.
		p0 := Params{Strategy: strategy, EpsDeg: 12, Seed: 1}
		p1 := Params{Strategy: strategy, EpsDeg: 12, Seed: 2}
		r0, _ := Generate(stars, p0)
		r1, _ := Generate(stars, p1)
		if !slices.Equal(r0.Edges, r1.Edges) {
			t.Errorf("%s: without jitter the seed must not change the edges", strategy)
		}
	}
}

func TestInputOrderDoesNotMatter(t *testing.T) {
	t.Parallel()
	stars := sky(200, 3)
	shuffled := slices.Clone(stars)
	rand.New(rand.NewPCG(9, 9)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	p := Params{Strategy: Cycles, EpsDeg: 12}
	a, _ := Generate(stars, p)
	b, _ := Generate(shuffled, p)
	if Hash(p, a) != Hash(p, b) {
		t.Error("the result depends on the order of the input stars")
	}
}

// Cycles closes polygons: a square has an MST of 3 edges, a Gabriel chord adds one.
func TestCyclesAddChords(t *testing.T) {
	t.Parallel()
	square := []Star{{1, 0, 0, 1}, {2, 4, 0, 1}, {3, 4, 4, 1}, {4, 0, 4, 1}}
	base, _ := Generate(square, Params{Strategy: Base, EpsDeg: 6, MinCluster: 2})
	cyc, _ := Generate(square, Params{Strategy: Cycles, EpsDeg: 6, MinCluster: 2, ChordFrac: 0.4})
	if len(base.Edges) != 3 || len(cyc.Edges) != 4 {
		t.Errorf("base %v, cycles %v", base.Edges, cyc.Edges)
	}
	// The diagonals are not Gabriel edges (the other corners lie inside their circle).
	if has(cyc, 1, 3) || has(cyc, 2, 4) {
		t.Errorf("a diagonal was added: %v", cyc.Edges)
	}
}

// Along the radius is cheap, sideways is dear.
func TestStarPathsAnisotropy(t *testing.T) {
	t.Parallel()
	stars := []Star{
		{1, 0, 60, 1}, {2, 0, 64, 1}, // 4° apart along a meridian
		{3, 8, 60, 1}, // same latitude, 8° of RA = ~4° of arc sideways
	}
	p := Params{EpsDeg: 5, MinCluster: 2, Gamma: 3}
	p.Strategy = StarPaths
	paths, _ := Generate(stars, p)
	p.Strategy = Base
	base, _ := Generate(stars, p)
	if !has(paths, 1, 2) || has(paths, 1, 3) {
		t.Errorf("star_paths: %v", paths.Edges)
	}
	if !has(base, 1, 3) {
		t.Errorf("base should join the sideways pair: %v", base.Edges)
	}
}

func TestSectorBordersAndPenalty(t *testing.T) {
	t.Parallel()
	// Bright stars in two groups, 10..30 and 190..210: borders in the two big gaps.
	b := sectorBorders([]float64{10, 20, 30, 190, 200, 210}, 2)
	if len(b) != 2 || b[0] != 110 || b[1] != 290 {
		t.Fatalf("borders %v, want [110 290]", b)
	}
	if sectorOf(20, b) != sectorOf(300, b) && sectorOf(20, b) == sectorOf(200, b) {
		t.Errorf("sectors: 20→%d 200→%d 300→%d", sectorOf(20, b), sectorOf(200, b), sectorOf(300, b))
	}
	if sectorOf(10, b) == sectorOf(200, b) {
		t.Error("the two groups must be in different sectors")
	}

	// Two stars 4° apart on both sides of the border at RA 110° do not connect;
	// two stars 1° apart do (1° + 10° penalty is still within eps 12°).
	bright := []Star{{1, 10, 0, 1}, {2, 20, 0, 1}, {3, 30, 0, 1}, {4, 190, 0, 1}, {5, 200, 0, 1}, {6, 210, 0, 1}}
	apart := append(slices.Clone(bright), Star{7, 108, 0, 4.9}, Star{8, 112, 0, 4.9})
	p := Params{Strategy: Sectors, SectorK: 2, SectorMag: 3.5, SectorPenaltyDeg: 10, EpsDeg: 12, MinCluster: 2}
	r, _ := Generate(apart, p)
	if has(r, 7, 8) {
		t.Errorf("4° across a border cost 14° and must not connect: %v", r.Edges)
	}
	near := append(slices.Clone(bright), Star{7, 109.5, 0, 4.9}, Star{8, 110.5, 0, 4.9})
	r, _ = Generate(near, p)
	if !has(r, 7, 8) {
		t.Errorf("1° across a border costs 11° and may connect: %v", r.Edges)
	}
}

func TestImperialCourtAndWalls(t *testing.T) {
	t.Parallel()
	stars := []Star{
		{1, 0, 89, 2},                                    // emperor (brightest in the court)
		{2, 90, 87, 3}, {3, 180, 86, 3}, {4, 270, 85, 4}, // vassals, θ <= 8
	}
	// A ring at θ = 15°: eight stars every 45° of RA.
	for i := range 8 {
		stars = append(stars, Star{int64(10 + i), float64(i) * 45, 75, 4})
	}
	r, err := Generate(stars, Params{Strategy: Imperial, EpsDeg: 12, RingBreaks: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []int64{2, 3, 4} {
		if !has(r, 1, v) {
			t.Errorf("vassal %d is not joined to the emperor: %v", v, r.Edges)
		}
	}
	ring := 0
	for _, e := range r.Edges {
		if e.A >= 10 && e.B >= 10 {
			ring++
		}
	}
	if ring != 8-2 { // 8 ring links minus 2 cut gaps
		t.Errorf("the ring should have 6 wall links, got %d: %v", ring, r.Edges)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	bad := []Params{
		{Strategy: "voronoi"}, {MagMax: 9}, {EpsDeg: 100}, {JitterDeg: 10}, {Gamma: 0.5},
		{Strategy: Imperial, CoreDeg: 30, RingDeg: 20}, {ChordFrac: 2},
	}
	for _, p := range bad {
		if err := p.Validate(); !errors.Is(err, ErrParams) {
			t.Errorf("%+v accepted: %v", p, err)
		}
	}
	if err := (Params{}).Validate(); err != nil {
		t.Errorf("defaults rejected: %v", err)
	}
	if _, err := Generate(nil, Params{Strategy: "nope"}); !errors.Is(err, ErrParams) {
		t.Errorf("Generate accepted a bad strategy: %v", err)
	}
}

func BenchmarkBaseMag5(b *testing.B) {
	stars := make([]Star, 0, 1600)
	for _, s := range sky(1600, 5) {
		s.Mag = 4
		stars = append(stars, s)
	}
	for b.Loop() {
		if _, err := Generate(stars, Params{Strategy: Base}); err != nil {
			b.Fatal(err)
		}
	}
}
