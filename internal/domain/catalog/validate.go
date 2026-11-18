package catalog

import "math"

// SchemaVersion is the cosmos.db schema this code reads and writes.
const SchemaVersion = 1

// IAUConstellations is the number of IAU constellations HYG assigns stars to.
const IAUConstellations = 88

// Tier is a magnitude limit and the star count the project documents for it
// (counts include the Sun, which HYG lists with magnitude −26.7).
type Tier struct {
	MagLimit float64 // stars with mag < MagLimit
	Expected int
}

// Tiers are the LOD tiers from docs/PLAN.md §10.
var Tiers = []Tier{
	{5, 1616},
	{6, 5020},
	{6.5, 8835},
	{7, 15459},
	{8, 41149},
	{9, 83124},
	{10, 107938},
}

// Status is the outcome of one check.
type Status string

// Check outcomes. Only StatusFail rejects a build.
const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Check codes. The UI translates them; the server never sends text.
const (
	CheckSchema         = "SCHEMA_VERSION"
	CheckArtifactSHA    = "ARTIFACT_SHA256"
	CheckContentHash    = "CONTENT_HASH"
	CheckStarTier       = "STAR_TIER"
	CheckConstellations = "CONSTELLATIONS"
	CheckBrightest      = "BRIGHTEST_STAR"
	CheckCultures       = "CULTURES"
	CheckEdgeStars      = "EDGE_STARS"
	CheckDroppedEdges   = "DROPPED_EDGES"
)

// Snapshot is what an inspector measured in a cosmos.db file.
type Snapshot struct {
	SchemaVersion int
	// File integrity: hash of the file as announced by dataprep and as read.
	ExpectedSHA256 string
	ActualSHA256   string
	// Content integrity: hash stored in meta and recomputed from the tables.
	StoredContentHash   string
	ComputedContentHash string

	TierCounts     []int // parallel to Tiers
	Constellations int
	NoBrightest    int // constellations whose brightest_star_id is missing or not a star
	Cultures       int
	Edges          int
	EdgesNoStar    int // edge ends that are not in the star table
	DroppedEdges   int // recorded by dataprep in meta
}

// Check is one validation result.
type Check struct {
	Code   string         `json:"code"`
	Status Status         `json:"status"`
	Params map[string]any `json:"params,omitempty"`
}

// Report is the result of validating one build.
type Report struct {
	Checks []Check `json:"checks"`
}

// OK reports whether no check failed (warnings are allowed).
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return false
		}
	}
	return true
}

// Warnings counts checks with StatusWarn.
func (r Report) Warnings() int { return r.count(StatusWarn) }

// Failed returns the codes of failed checks.
func (r Report) Failed() []string {
	var out []string
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			out = append(out, c.Code)
		}
	}
	return out
}

func (r Report) count(s Status) int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == s {
			n++
		}
	}
	return n
}

// Validate applies the M1 rules to a snapshot:
//   - schema version is known;
//   - the file SHA-256 and the content hash match;
//   - star counts per tier match the documented ones (within 1 % is a warning);
//   - 88 constellations, each with a brightest star that exists;
//   - cultures are present and every edge end is a star.
func Validate(s Snapshot) Report {
	var r Report
	add := func(code string, st Status, params map[string]any) {
		r.Checks = append(r.Checks, Check{Code: code, Status: st, Params: params})
	}
	pass := func(ok bool) Status {
		if ok {
			return StatusOK
		}
		return StatusFail
	}

	add(CheckSchema, pass(s.SchemaVersion == SchemaVersion),
		map[string]any{"expected": SchemaVersion, "actual": s.SchemaVersion})
	add(CheckArtifactSHA, pass(s.ExpectedSHA256 != "" && s.ExpectedSHA256 == s.ActualSHA256),
		map[string]any{"expected": s.ExpectedSHA256, "actual": s.ActualSHA256})
	add(CheckContentHash, pass(s.StoredContentHash != "" && s.StoredContentHash == s.ComputedContentHash),
		map[string]any{"stored": s.StoredContentHash, "computed": s.ComputedContentHash})

	for i, t := range Tiers {
		got := 0
		if i < len(s.TierCounts) {
			got = s.TierCounts[i]
		}
		add(CheckStarTier, tierStatus(t.Expected, got),
			map[string]any{"mag_limit": t.MagLimit, "expected": t.Expected, "actual": got})
	}

	conSt := StatusOK
	if s.Constellations != IAUConstellations {
		conSt = StatusWarn
	}
	add(CheckConstellations, conSt, map[string]any{"expected": IAUConstellations, "actual": s.Constellations})
	add(CheckBrightest, pass(s.NoBrightest == 0), map[string]any{"missing": s.NoBrightest})
	add(CheckCultures, pass(s.Cultures > 0 && s.Edges > 0),
		map[string]any{"cultures": s.Cultures, "edges": s.Edges})
	add(CheckEdgeStars, pass(s.EdgesNoStar == 0), map[string]any{"without_star": s.EdgesNoStar})
	dropSt := StatusOK
	if s.DroppedEdges > 0 {
		dropSt = StatusWarn
	}
	add(CheckDroppedEdges, dropSt, map[string]any{"dropped": s.DroppedEdges})
	return r
}

// tierStatus: exact is ok, within 1 % is a warning, anything else fails.
func tierStatus(expected, got int) Status {
	switch {
	case got == expected:
		return StatusOK
	case math.Abs(float64(got-expected)) <= 0.01*float64(expected):
		return StatusWarn
	}
	return StatusFail
}
