package genconst

import (
	"cmp"
	"math"
	"slices"
)

// graph holds the candidate stars and the strategy's distance.
type graph struct {
	st     []Star
	p      Params
	sector []int // sector of every star (Sectors strategy), else nil
}

func newGraph(st []Star, p Params) *graph {
	g := &graph{st: st, p: p}
	if p.Strategy == Sectors {
		g.sector = sectorsOf(st, p)
	}
	return g
}

// dist is the strategy's distance between stars i and j, in degrees.
func (g *graph) dist(i, j int) float64 {
	a, b := g.st[i], g.st[j]
	d := hav(a.RA, a.Dec, b.RA, b.Dec)
	switch g.p.Strategy {
	case Sectors:
		if g.sector[i] != g.sector[j] {
			d += g.p.SectorPenaltyDeg
		}
	case StarPaths:
		// Radius from the pole is 90°−Dec; sideways is the arc along the circle.
		dTheta := a.Dec - b.Dec
		meanTheta := rad(90 - (a.Dec+b.Dec)/2)
		side := wrap180(a.RA-b.RA) * math.Sin(meanTheta)
		d = math.Hypot(dTheta, g.p.Gamma*side)
	}
	return d
}

type cand struct {
	i, j int
	d    float64
}

// all returns every pair of stars within EpsDeg. Every distance is at least
// the difference of declinations, which bounds the search.
func (g *graph) all() []cand {
	return g.pairs(func(int) bool { return true })
}

func (g *graph) pairs(keep func(int) bool) []cand {
	order := make([]int, 0, len(g.st))
	for i := range g.st {
		if keep(i) {
			order = append(order, i)
		}
	}
	slices.SortFunc(order, func(a, b int) int { return cmp.Compare(g.st[a].Dec, g.st[b].Dec) })
	var out []cand
	for x, i := range order {
		for _, j := range order[x+1:] {
			if g.st[j].Dec-g.st[i].Dec > g.p.EpsDeg {
				break
			}
			if d := g.dist(i, j); d <= g.p.EpsDeg {
				out = append(out, cand{i, j, d})
			}
		}
	}
	return out
}

// forest is Kruskal with bounded cluster size: the minimum spanning forest of
// the pairs, where a union that would make a cluster larger than MaxCluster is
// skipped. Clusters smaller than MinCluster are dropped.
func (g *graph) forest(cs []cand) []Edge {
	slices.SortFunc(cs, func(a, b cand) int {
		if c := cmp.Compare(a.d, b.d); c != 0 {
			return c
		}
		if c := cmp.Compare(g.st[a.i].HIP, g.st[b.i].HIP); c != 0 {
			return c
		}
		return cmp.Compare(g.st[a.j].HIP, g.st[b.j].HIP)
	})
	uf := newUF(len(g.st))
	var kept []cand
	for _, c := range cs {
		if uf.union(c.i, c.j, g.p.MaxCluster) {
			kept = append(kept, c)
		}
	}
	var out []Edge
	for _, c := range kept {
		if uf.size(c.i) >= g.p.MinCluster {
			out = append(out, norm(g.st[c.i].HIP, g.st[c.j].HIP))
		}
	}
	return out
}

// chords adds, per cluster, the shortest Gabriel edges that are not in the
// forest: a pair is Gabriel when no third star of the cluster lies inside the
// circle on the pair as diameter (checked with the small-angle approximation).
func (g *graph) chords(forest []Edge) []Edge {
	idx := make(map[int64]int, len(g.st))
	for i, s := range g.st {
		idx[s.HIP] = i
	}
	uf := newUF(len(g.st))
	in := map[Edge]bool{}
	for _, e := range forest {
		in[e] = true
		uf.union(idx[e.A], idx[e.B], 1<<30)
	}
	members := map[int][]int{}
	for _, e := range forest {
		for _, h := range [2]int64{e.A, e.B} {
			r := uf.find(idx[h])
			if !slices.Contains(members[r], idx[h]) {
				members[r] = append(members[r], idx[h])
			}
		}
	}
	var out []Edge
	for _, m := range members {
		var extra []cand
		for x, i := range m {
			for _, j := range m[x+1:] {
				d := g.dist(i, j)
				if d > g.p.EpsDeg || in[norm(g.st[i].HIP, g.st[j].HIP)] || !g.gabriel(m, i, j) {
					continue
				}
				extra = append(extra, cand{i, j, d})
			}
		}
		slices.SortFunc(extra, func(a, b cand) int {
			if c := cmp.Compare(a.d, b.d); c != 0 {
				return c
			}
			return cmp.Compare(g.st[a.i].HIP*1_000_000+g.st[a.j].HIP, g.st[b.i].HIP*1_000_000+g.st[b.j].HIP)
		})
		limit := int(math.Ceil(g.p.ChordFrac * float64(len(m)-1)))
		for _, c := range extra[:min(limit, len(extra))] {
			out = append(out, norm(g.st[c.i].HIP, g.st[c.j].HIP))
		}
	}
	return out
}

func (g *graph) gabriel(members []int, i, j int) bool {
	dij := g.dist(i, j)
	for _, k := range members {
		if k == i || k == j {
			continue
		}
		dik, djk := g.dist(i, k), g.dist(j, k)
		if dik*dik+djk*djk < dij*dij {
			return false
		}
	}
	return true
}

// imperial builds the court, the walls and the outer provinces.
func (g *graph) imperial() []Edge {
	theta := func(i int) float64 { return 90 - g.st[i].Dec }
	var court, ring []int
	for i := range g.st {
		switch t := theta(i); {
		case t <= g.p.CoreDeg:
			court = append(court, i)
		case t <= g.p.RingDeg:
			ring = append(ring, i)
		}
	}
	var edges []Edge

	// Court: the brightest star is the emperor, the next ones are his vassals.
	byMag := func(a, b int) int {
		if c := cmp.Compare(g.st[a].Mag, g.st[b].Mag); c != 0 {
			return c
		}
		return cmp.Compare(g.st[a].HIP, g.st[b].HIP)
	}
	slices.SortFunc(court, byMag)
	court = court[:min(len(court), g.p.CoreStars)]
	for _, v := range court[min(1, len(court)):] {
		edges = append(edges, norm(g.st[court[0]].HIP, g.st[v].HIP))
	}

	// Walls: stars of the ring joined in order of RA, the largest gaps cut.
	slices.SortFunc(ring, func(a, b int) int {
		if c := cmp.Compare(g.st[a].RA, g.st[b].RA); c != 0 {
			return c
		}
		return cmp.Compare(g.st[a].HIP, g.st[b].HIP)
	})
	if n := len(ring); n >= 2 {
		type gap struct {
			from int // index in ring; the gap leads to ring[(from+1)%n]
			size float64
		}
		gaps := make([]gap, n)
		for k := range ring {
			next := ring[(k+1)%n]
			gaps[k] = gap{k, math.Mod(g.st[next].RA-g.st[ring[k]].RA+360, 360)}
		}
		cut := map[int]bool{}
		sorted := slices.Clone(gaps)
		slices.SortStableFunc(sorted, func(a, b gap) int { return cmp.Compare(b.size, a.size) })
		for _, gp := range sorted[:min(g.p.RingBreaks, len(sorted))] {
			cut[gp.from] = true
		}
		for k := range ring {
			next := ring[(k+1)%n]
			if n == 2 && k == 1 {
				break // two stars are one edge, not a closed ring
			}
			if !cut[k] && hav(g.st[ring[k]].RA, g.st[ring[k]].Dec, g.st[next].RA, g.st[next].Dec) <= 2*g.p.EpsDeg {
				edges = append(edges, norm(g.st[ring[k]].HIP, g.st[next].HIP))
			}
		}
	}

	// Provinces: ordinary clustering of everything outside the ring.
	outer := g.pairs(func(i int) bool { return theta(i) > g.p.RingDeg })
	return append(edges, g.forest(outer)...)
}

// sectorsOf finds the sector borders from the gaps between the bright stars
// (as seen around the pole) and assigns a sector to every star.
func sectorsOf(st []Star, p Params) []int {
	var angles []float64
	for _, s := range st {
		if s.Mag <= p.SectorMag {
			angles = append(angles, s.RA)
		}
	}
	borders := sectorBorders(angles, p.SectorK)
	out := make([]int, len(st))
	for i, s := range st {
		out[i] = sectorOf(s.RA, borders)
	}
	return out
}

// sectorBorders returns up to k borders at the middle of the k largest gaps
// between the angles (degrees) around the circle, sorted.
func sectorBorders(angles []float64, k int) []float64 {
	if len(angles) < 2 || k < 2 {
		return nil
	}
	a := slices.Clone(angles)
	slices.Sort(a)
	type gap struct {
		mid, size float64
	}
	gaps := make([]gap, len(a))
	for i := range a {
		next := a[(i+1)%len(a)]
		size := math.Mod(next-a[i]+360, 360)
		if len(a) > 0 && i == len(a)-1 && size == 0 {
			size = 360
		}
		gaps[i] = gap{math.Mod(a[i]+size/2, 360), size}
	}
	slices.SortStableFunc(gaps, func(x, y gap) int { return cmp.Compare(y.size, x.size) })
	var out []float64
	for _, gp := range gaps[:min(k, len(gaps))] {
		out = append(out, gp.mid)
	}
	slices.Sort(out)
	return out
}

// sectorOf numbers the sector of an angle: borders split the circle into
// len(borders) arcs; the arc that wraps through 0° shares sector 0.
func sectorOf(angle float64, borders []float64) int {
	if len(borders) == 0 {
		return 0
	}
	n := 0
	for _, b := range borders {
		if b <= angle {
			n++
		}
	}
	return n % len(borders)
}

// result sorts the edges and counts the clusters.
func (g *graph) result(edges []Edge) Result {
	slices.SortFunc(edges, func(a, b Edge) int {
		if c := cmp.Compare(a.A, b.A); c != 0 {
			return c
		}
		return cmp.Compare(a.B, b.B)
	})
	edges = slices.Compact(edges)

	idx := make(map[int64]int, len(g.st))
	used := make([]int64, len(g.st))
	for i, s := range g.st {
		idx[s.HIP] = i
		used[i] = s.HIP
	}
	uf := newUF(len(g.st))
	for _, e := range edges {
		uf.union(idx[e.A], idx[e.B], 1<<30)
	}
	roots := map[int]bool{}
	for _, e := range edges {
		roots[uf.find(idx[e.A])] = true
	}
	return Result{Edges: edges, Used: used, Clusters: len(roots)}
}

// uf is a union-find with set sizes.
type uf struct{ parent, sz []int }

func newUF(n int) *uf {
	u := &uf{parent: make([]int, n), sz: make([]int, n)}
	for i := range u.parent {
		u.parent[i], u.sz[i] = i, 1
	}
	return u
}

func (u *uf) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

func (u *uf) size(x int) int { return u.sz[u.find(x)] }

// union joins the sets of a and b unless they are already one or the result
// would exceed limit stars; it reports whether it joined them.
func (u *uf) union(a, b, limit int) bool {
	ra, rb := u.find(a), u.find(b)
	if ra == rb || u.sz[ra]+u.sz[rb] > limit {
		return false
	}
	if u.sz[ra] < u.sz[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	u.sz[ra] += u.sz[rb]
	return true
}
