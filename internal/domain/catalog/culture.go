package catalog

// NoStar marks a polyline vertex that is not a catalog star (a bare sky
// position, a deep-sky object). Such vertices split a polyline.
const NoStar int64 = 0

// IAUCultureID is the sky culture whose constellations are the IAU ones; their
// names fill the Constellation table.
const IAUCultureID = "modern_iau"

// CultureConstellation is one figure of a sky culture: polylines of Hipparcos
// numbers (NoStar for vertices that are not stars).
type CultureConstellation struct {
	ID         string // unique inside the culture, e.g. "And" or "001"
	NameEN     string
	NameNative string
	Polylines  [][]int64
}

// Culture is one sky culture (a set of figures drawn on the same stars).
type Culture struct {
	ID             string
	Region         string
	Classification []string
	Constellations []CultureConstellation
}

// Edge is one line segment between two stars, by Hipparcos number.
type Edge struct {
	Culture       string
	Constellation string
	Line          int // polyline index inside the constellation
	Seq           int // segment index inside the polyline
	A, B          int64
}

// SplitPolyline returns the runs of consecutive stars of a polyline; vertices
// that are not stars cut it into separate runs.
func SplitPolyline(p []int64) [][]int64 {
	var runs [][]int64
	var cur []int64
	for _, v := range p {
		if v == NoStar {
			if len(cur) > 1 {
				runs = append(runs, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, v)
	}
	if len(cur) > 1 {
		runs = append(runs, cur)
	}
	return runs
}
