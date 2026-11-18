package catalog

import (
	"cmp"
	"slices"
	"strings"
)

// Dataset is the full, consistent content of one cosmos.db build. Every slice
// is sorted by its natural key, so writing and hashing are deterministic.
type Dataset struct {
	Stars          []Star          // by ID
	Constellations []Constellation // by Abbr
	Cultures       []Culture       // by ID; constellations by ID
	Edges          []Edge          // by culture, constellation, line, seq
	DroppedEdges   int             // segments dropped: an end is not in the star table
	SkippedVerts   int             // polyline vertices that are not stars
}

// NewDataset builds the dataset from raw sources.
//
//   - namesUK maps a Hipparcos number to a curated Ukrainian proper name.
//   - The brightest star of every constellation is computed here.
//   - Polylines become edges between consecutive stars; a segment whose end is
//     not in the star table is dropped and counted (real skyculture files do
//     contain a few such references).
func NewDataset(stars []Star, cultures []Culture, namesUK map[int64]string) Dataset {
	stars = slices.Clone(stars)
	for i := range stars {
		if n, ok := namesUK[stars[i].HIP]; ok && stars[i].HIP != 0 {
			stars[i].NameUK = n
		}
	}
	slices.SortFunc(stars, func(a, b Star) int { return cmp.Compare(a.ID, b.ID) })

	cultures = slices.Clone(cultures)
	for i := range cultures {
		cultures[i].Constellations = slices.Clone(cultures[i].Constellations)
		slices.SortFunc(cultures[i].Constellations, func(a, b CultureConstellation) int {
			return strings.Compare(a.ID, b.ID)
		})
	}
	slices.SortFunc(cultures, func(a, b Culture) int { return strings.Compare(a.ID, b.ID) })

	ds := Dataset{Stars: stars, Cultures: cultures}
	ds.Constellations = buildConstellations(stars, cultures)
	ds.Edges, ds.DroppedEdges, ds.SkippedVerts = buildEdges(stars, cultures)
	return ds
}

func buildConstellations(stars []Star, cultures []Culture) []Constellation {
	brightest := BrightestPerConstellation(stars)
	names := map[string]CultureConstellation{}
	for _, c := range cultures {
		if c.ID != IAUCultureID {
			continue
		}
		for _, cc := range c.Constellations {
			names[cc.ID] = cc
		}
	}
	out := make([]Constellation, 0, len(brightest))
	for abbr, id := range brightest {
		n := names[abbr]
		out = append(out, Constellation{Abbr: abbr, NameLatin: n.NameNative, NameEN: n.NameEN, BrightestStarID: id})
	}
	slices.SortFunc(out, func(a, b Constellation) int { return strings.Compare(a.Abbr, b.Abbr) })
	return out
}

func buildEdges(stars []Star, cultures []Culture) (edges []Edge, dropped, skipped int) {
	hasHIP := make(map[int64]bool, len(stars))
	for _, s := range stars {
		if s.HIP != 0 {
			hasHIP[s.HIP] = true
		}
	}
	for _, cu := range cultures {
		for _, cc := range cu.Constellations {
			for line, poly := range cc.Polylines {
				for _, v := range poly {
					if v == NoStar {
						skipped++
					}
				}
				seq := 0
				for _, run := range SplitPolyline(poly) {
					for i := 0; i+1 < len(run); i++ {
						a, b := run[i], run[i+1]
						if a == b {
							continue
						}
						if !hasHIP[a] || !hasHIP[b] {
							dropped++
							continue
						}
						edges = append(edges, Edge{Culture: cu.ID, Constellation: cc.ID, Line: line, Seq: seq, A: a, B: b})
						seq++
					}
				}
			}
		}
	}
	return edges, dropped, skipped
}
