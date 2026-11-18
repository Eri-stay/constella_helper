// Package compare measures how close a generated constellation graph is to a
// human sky culture: the Jaccard index of their edge sets.
package compare

import "constella/internal/domain/genconst"

// Metrics of one comparison.
type Metrics struct {
	Jaccard   float64 `json:"jaccard"`   // |gen ∩ ref| / |gen ∪ ref|
	Precision float64 `json:"precision"` // share of generated edges that the culture also draws
	Recall    float64 `json:"recall"`    // share of the culture's edges that were generated
	Common    int     `json:"common"`
	Generated int     `json:"generated"`
	Reference int     `json:"reference"` // culture edges between candidate stars only
}

// Edges compares the generated edges with the culture's. Only culture edges
// whose both ends were candidate stars (the Used list of the result) take
// part: a method cannot be blamed for stars it was not given.
func Edges(gen, ref []genconst.Edge, used []int64) Metrics {
	have := make(map[int64]bool, len(used))
	for _, h := range used {
		have[h] = true
	}
	refSet := map[genconst.Edge]bool{}
	for _, e := range ref {
		e = order(e)
		if e.A != e.B && have[e.A] && have[e.B] {
			refSet[e] = true
		}
	}
	genSet := map[genconst.Edge]bool{}
	for _, e := range gen {
		genSet[order(e)] = true
	}
	common := 0
	for e := range genSet {
		if refSet[e] {
			common++
		}
	}
	m := Metrics{Common: common, Generated: len(genSet), Reference: len(refSet)}
	if union := len(genSet) + len(refSet) - common; union > 0 {
		m.Jaccard = float64(common) / float64(union)
	}
	if len(genSet) > 0 {
		m.Precision = float64(common) / float64(len(genSet))
	}
	if len(refSet) > 0 {
		m.Recall = float64(common) / float64(len(refSet))
	}
	return m
}

func order(e genconst.Edge) genconst.Edge {
	if e.A > e.B {
		e.A, e.B = e.B, e.A
	}
	return e
}
