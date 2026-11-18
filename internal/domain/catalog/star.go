// Package catalog is the pure domain of the sky catalog: stars, constellations,
// sky cultures, the rules that turn raw source rows into one consistent
// dataset (the content of cosmos.db), and the rules that validate it.
//
// It has no I/O. Readers and writers of files and databases live in the
// services; the path mirrors constella/server/internal/domain/catalog so the
// package can be lifted into the product unchanged.
package catalog

// Star is one catalog row (HYG v4.2). Positions are J2000: RA in hours, Dec in
// degrees, distance in parsecs, proper motion in mas/yr.
type Star struct {
	ID     int64 // HYG id; 0 is the Sun
	HIP    int64 // Hipparcos number, 0 when the star has none
	Name   string
	NameUK string // Ukrainian proper name (curated, optional)
	Bayer  string
	Flam   int
	Con    string // IAU constellation abbreviation, empty for the Sun
	RA     float64
	Dec    float64
	Dist   float64
	PMRA   float64
	PMDec  float64
	Mag    float64
	AbsMag float64
	Spect  string
	CI     *float64 // colour index B−V, nil when unknown
}

// Constellation is one of the 88 IAU constellations.
type Constellation struct {
	Abbr            string
	NameLatin       string
	NameEN          string
	BrightestStarID int64 // pre-computed in the pipeline, never at runtime
}

// BrightestPerConstellation returns, for every constellation abbreviation, the
// id of its brightest star (lowest magnitude; the lowest id wins a tie, so the
// result is deterministic). Stars without a constellation are ignored.
func BrightestPerConstellation(stars []Star) map[string]int64 {
	best := map[string]Star{}
	for _, s := range stars {
		if s.Con == "" {
			continue
		}
		cur, ok := best[s.Con]
		if !ok || s.Mag < cur.Mag || (s.Mag == cur.Mag && s.ID < cur.ID) {
			best[s.Con] = s
		}
	}
	out := make(map[string]int64, len(best))
	for con, s := range best {
		out[con] = s.ID
	}
	return out
}
