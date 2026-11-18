package compare

import (
	"math"
	"testing"

	"constella/internal/domain/genconst"
)

func e(a, b int64) genconst.Edge { return genconst.Edge{A: a, B: b} }

func TestEdges(t *testing.T) {
	t.Parallel()
	gen := []genconst.Edge{e(1, 2), e(2, 3), e(3, 4)}
	ref := []genconst.Edge{e(2, 1), e(3, 4), e(4, 9), e(7, 8), e(5, 5)} // {4,9}: 9 is not a candidate; {7,8}: 7,8 are; {5,5} degenerate
	m := Edges(gen, ref, []int64{1, 2, 3, 4, 7, 8})

	// ref inside the candidates: {1,2} {3,4} {7,8}; common: {1,2} {3,4}
	if m.Common != 2 || m.Generated != 3 || m.Reference != 3 {
		t.Fatalf("counts: %+v", m)
	}
	want := 2.0 / (3 + 3 - 2)
	if math.Abs(m.Jaccard-want) > 1e-12 || math.Abs(m.Precision-2.0/3) > 1e-12 || math.Abs(m.Recall-2.0/3) > 1e-12 {
		t.Errorf("metrics: %+v, want jaccard %v", m, want)
	}
}

func TestEdgesEmpty(t *testing.T) {
	t.Parallel()
	if m := Edges(nil, nil, nil); m.Jaccard != 0 || m.Generated != 0 {
		t.Errorf("empty: %+v", m)
	}
	if m := Edges([]genconst.Edge{e(1, 2)}, nil, []int64{1, 2}); m.Jaccard != 0 || m.Precision != 0 {
		t.Errorf("no reference: %+v", m)
	}
}
