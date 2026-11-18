// Package genconst generates constellations from star positions alone and is
// the research core of the platform: which rule for drawing lines between
// stars comes closest to a human sky culture?
//
// Every strategy works in three steps: (1) take the stars brighter than a
// limit, (2) connect them with a minimum spanning forest under a strategy
// specific distance, with clusters of bounded size, (3) optionally reshape
// the graph (extra cycles, rings around the pole). The result is a list of
// edges between Hipparcos numbers, comparable with a culture's edge list.
//
// The package is pure: no I/O, deterministic for equal input and Params.
package genconst

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
)

// Strategies.
const (
	// Base: haversine distance, MST forest. The reference method.
	Base = "base"
	// Sectors: stars on different sides of automatically found sector
	// boundaries cost an extra penalty ("soft borders").
	Sectors = "sectors"
	// Imperial: an emperor star with vassals at the pole, wall arcs around
	// it, ordinary clustering elsewhere (Purple Forbidden Enclosure style).
	Imperial = "imperial"
	// StarPaths: anisotropic metric around the pole; movement along the
	// radius is cheap, sideways is Gamma times dearer (Polynesian star paths).
	StarPaths = "star_paths"
	// Cycles: the MST plus the shortest Gabriel chords, so figures get closed
	// polygons (Western style).
	Cycles = "cycles"
)

// Strategies lists all strategy names.
var Strategies = []string{Base, Sectors, Imperial, StarPaths, Cycles}

// Star is a candidate star. RA and Dec are in degrees.
type Star struct {
	HIP     int64
	RA, Dec float64
	Mag     float64
}

// Edge joins two Hipparcos numbers, A < B.
type Edge struct{ A, B int64 }

// Params configure a generation. Zero fields take the defaults of WithDefaults.
type Params struct {
	Strategy   string  `json:"strategy"`
	MagMax     float64 `json:"mag_max"`     // candidates are stars with mag <= MagMax
	EpsDeg     float64 `json:"eps_deg"`     // longest edge (in the strategy's distance)
	MaxCluster int     `json:"max_cluster"` // largest cluster, stars
	MinCluster int     `json:"min_cluster"` // smaller clusters are dropped

	Seed      uint64  `json:"seed"`       // drives the jitter only
	JitterDeg float64 `json:"jitter_deg"` // random shift of every position, ± degrees

	SectorK          int     `json:"sector_k"`           // sectors: number of sectors
	SectorMag        float64 `json:"sector_mag"`         // sectors: stars this bright define the borders
	SectorPenaltyDeg float64 `json:"sector_penalty_deg"` // sectors: extra distance across a border

	Gamma float64 `json:"gamma"` // star_paths: sideways cost factor, >= 1

	ChordFrac float64 `json:"chord_frac"` // cycles: extra edges as a share of the MST

	CoreDeg    float64 `json:"core_deg"`    // imperial: radius of the court around the pole
	RingDeg    float64 `json:"ring_deg"`    // imperial: outer radius of the wall zone
	CoreStars  int     `json:"core_stars"`  // imperial: emperor plus vassals
	RingBreaks int     `json:"ring_breaks"` // imperial: gaps that cut the ring into walls
}

// WithDefaults fills zero fields.
func (p Params) WithDefaults() Params {
	def := func(v *float64, d float64) {
		if *v == 0 {
			*v = d
		}
	}
	defi := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	if p.Strategy == "" {
		p.Strategy = Base
	}
	def(&p.MagMax, 5)
	def(&p.EpsDeg, 10)
	defi(&p.MaxCluster, 20)
	defi(&p.MinCluster, 3)
	defi(&p.SectorK, 6)
	def(&p.SectorMag, 3.5)
	def(&p.SectorPenaltyDeg, 10)
	def(&p.Gamma, 3)
	def(&p.ChordFrac, 0.2)
	def(&p.CoreDeg, 8)
	def(&p.RingDeg, 25)
	defi(&p.CoreStars, 7)
	defi(&p.RingBreaks, 3)
	return p
}

// ErrParams is returned for parameters outside the supported ranges.
var ErrParams = errors.New("invalid generation parameters")

// Validate checks p (after defaults).
func (p Params) Validate() error {
	p = p.WithDefaults()
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrParams, msg) }
	switch {
	case !slices.Contains(Strategies, p.Strategy):
		return bad("unknown strategy " + p.Strategy)
	case p.MagMax <= 0 || p.MagMax > 6.5:
		return bad("mag_max must be in (0, 6.5]")
	case p.EpsDeg <= 0 || p.EpsDeg > 60:
		return bad("eps_deg must be in (0, 60]")
	case p.MaxCluster < 2 || p.MaxCluster > 200 || p.MinCluster < 2 || p.MinCluster > p.MaxCluster:
		return bad("cluster sizes must satisfy 2 <= min <= max <= 200")
	case p.JitterDeg < 0 || p.JitterDeg > 5:
		return bad("jitter_deg must be in [0, 5]")
	case p.SectorK < 1 || p.SectorK > 24:
		return bad("sector_k must be in [1, 24]")
	case p.Gamma < 1 || p.Gamma > 20:
		return bad("gamma must be in [1, 20]")
	case p.ChordFrac < 0 || p.ChordFrac > 1:
		return bad("chord_frac must be in [0, 1]")
	case p.CoreDeg <= 0 || p.RingDeg <= p.CoreDeg || p.RingDeg > 60 || p.CoreStars < 2 || p.RingBreaks < 0:
		return bad("imperial zones must satisfy 0 < core < ring <= 60, core_stars >= 2")
	}
	return nil
}

// Result is the generated graph.
type Result struct {
	Edges    []Edge  // sorted
	Used     []int64 // HIPs of the candidate stars, sorted
	Clusters int     // connected groups of at least two stars
}

// Generate builds constellations from stars.
func Generate(stars []Star, p Params) (Result, error) {
	p = p.WithDefaults()
	if err := p.Validate(); err != nil {
		return Result{}, err
	}
	g := newGraph(candidates(stars, p), p)

	var edges []Edge
	switch p.Strategy {
	case Imperial:
		edges = g.imperial()
	default:
		edges = g.forest(g.all())
		if p.Strategy == Cycles {
			edges = append(edges, g.chords(edges)...)
		}
	}
	return g.result(edges), nil
}

// candidates selects the stars brighter than MagMax, in HIP order, and
// shifts them by the seeded jitter.
func candidates(stars []Star, p Params) []Star {
	var out []Star
	for _, s := range stars {
		if s.HIP != 0 && s.Mag <= p.MagMax {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b Star) int { return cmp.Compare(a.HIP, b.HIP) })
	if p.JitterDeg > 0 {
		rng := rand.New(rand.NewPCG(p.Seed, p.Seed^0x9e3779b97f4a7c15))
		for i := range out {
			dd := (rng.Float64()*2 - 1) * p.JitterDeg
			da := (rng.Float64()*2 - 1) * p.JitterDeg
			out[i].Dec = math.Max(-90, math.Min(90, out[i].Dec+dd))
			out[i].RA = math.Mod(out[i].RA+da/math.Max(0.05, math.Cos(rad(out[i].Dec)))+360, 360)
		}
	}
	return out
}

// Hash is the SHA-256 of the parameters (with defaults) and the edges. Equal
// input and parameters, including the seed, always give the same hash.
func Hash(p Params, r Result) string {
	p = p.WithDefaults()
	h := sha256.New()
	fmt.Fprintf(h, "%+v\n", p)
	for _, e := range r.Edges {
		fmt.Fprintf(h, "%d-%d\n", e.A, e.B)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// hav is the angular distance in degrees between two points given by RA and Dec in degrees.
func hav(ra1, dec1, ra2, dec2 float64) float64 {
	p1, p2 := rad(dec1), rad(dec2)
	dp, dl := p2-p1, rad(ra2-ra1)
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return deg(2 * math.Asin(math.Min(1, math.Sqrt(a))))
}

// wrap180 maps an angle difference to (-180, 180].
func wrap180(d float64) float64 {
	x := math.Mod(d+180, 360)
	if x <= 0 {
		x += 360
	}
	return x - 180
}

func norm(a, b int64) Edge {
	if a > b {
		a, b = b, a
	}
	return Edge{a, b}
}
