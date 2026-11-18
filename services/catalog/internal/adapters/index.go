package adapters

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"constella/internal/cosmosdb"
	"constella/internal/domain/catalog"
)

// ErrNoActive is returned by queries while no validated catalog is loaded.
var ErrNoActive = errors.New("no active catalog")

// ErrNotFound is returned when a queried star, culture or figure is unknown.
var ErrNotFound = errors.New("not found")

// Inspector measures artifacts with the shared cosmos.db reader.
type Inspector struct{}

// Inspect implements app.Inspector.
func (Inspector) Inspect(ctx context.Context, path, expectedSHA string) (catalog.Snapshot, error) {
	return cosmosdb.Inspect(ctx, path, expectedSHA)
}

// Index answers read queries from the active cosmos.db, or from the build
// named by WithBuild. The active handle is swapped atomically when a newer
// build is validated; handles of other builds are opened on demand and kept
// in a small cache.
type Index struct {
	mu sync.RWMutex
	db *sql.DB

	// Resolve maps a build id to its artifact path; ErrNotFound if unknown.
	Resolve func(ctx context.Context, buildID string) (string, error)

	cacheMu sync.Mutex
	cache   map[string]*sql.DB
	order   []string // cache keys, oldest first
}

type buildKey struct{}

// WithBuild makes queries on ctx read the given build instead of the active one.
func WithBuild(ctx context.Context, buildID string) context.Context {
	if buildID == "" {
		return ctx
	}
	return context.WithValue(ctx, buildKey{}, buildID)
}

const maxCached = 4

func (ix *Index) forBuild(ctx context.Context, id string) (*sql.DB, error) {
	ix.cacheMu.Lock()
	defer ix.cacheMu.Unlock()
	if db, ok := ix.cache[id]; ok {
		return db, nil
	}
	if ix.Resolve == nil {
		return nil, ErrNoActive
	}
	path, err := ix.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	db, err := cosmosdb.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open %s: %w", path, err), db.Close())
	}
	if ix.cache == nil {
		ix.cache = map[string]*sql.DB{}
	}
	if len(ix.order) >= maxCached {
		oldest := ix.order[0]
		ix.order = ix.order[1:]
		_ = ix.cache[oldest].Close() // waits for running queries; nothing to report
		delete(ix.cache, oldest)
	}
	ix.cache[id] = db
	ix.order = append(ix.order, id)
	return db, nil
}

// Activate implements app.Index: it opens the file read-only and makes it the
// source of all queries.
func (ix *Index) Activate(path string) error {
	db, err := cosmosdb.OpenReadOnly(path)
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		return errors.Join(fmt.Errorf("open %s: %w", path, err), db.Close())
	}
	ix.mu.Lock()
	old := ix.db
	ix.db = db
	ix.mu.Unlock()
	if old != nil {
		// sql.DB.Close waits for queries that are still running on the old file.
		return old.Close()
	}
	return nil
}

// Close releases the active file.
func (ix *Index) Close() error {
	ix.cacheMu.Lock()
	for _, db := range ix.cache {
		_ = db.Close() // read-only handles; nothing to report
	}
	ix.cache, ix.order = nil, nil
	ix.cacheMu.Unlock()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.db == nil {
		return nil
	}
	err := ix.db.Close()
	ix.db = nil
	return err
}

func (ix *Index) handle(ctx context.Context) (*sql.DB, error) {
	if id, _ := ctx.Value(buildKey{}).(string); id != "" {
		return ix.forBuild(ctx, id)
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.db == nil {
		return nil, ErrNoActive
	}
	return ix.db, nil
}

// Summary counts the active catalog.
type Summary struct {
	Stars          int `json:"stars"`
	Constellations int `json:"constellations"`
	Cultures       int `json:"cultures"`
	Edges          int `json:"edges"`
}

// Summary returns counts of the active catalog.
func (ix *Index) Summary(ctx context.Context) (Summary, error) {
	db, err := ix.handle(ctx)
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	err = db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM stars), (SELECT COUNT(*) FROM constellations),
		(SELECT COUNT(*) FROM cultures), (SELECT COUNT(*) FROM culture_edges)`).
		Scan(&s.Stars, &s.Constellations, &s.Cultures, &s.Edges)
	if err != nil {
		return Summary{}, fmt.Errorf("summary: %w", err)
	}
	return s, nil
}

// Star is one star as the UI shows it.
type Star struct {
	ID     int64   `json:"id"`
	HIP    int64   `json:"hip,omitempty"`
	Name   string  `json:"name,omitempty"`
	NameUK string  `json:"name_uk,omitempty"`
	Bayer  string  `json:"bayer,omitempty"`
	Con    string  `json:"con,omitempty"`
	RA     float64 `json:"ra"`  // hours
	Dec    float64 `json:"dec"` // degrees
	Mag    float64 `json:"mag"`
	Spect  string  `json:"spect,omitempty"`
}

const starColumns = `id, hip, name, name_uk, bayer, con, ra, dec, mag, spect`

func scanStar(r scanner) (Star, error) {
	var s Star
	if err := r.Scan(&s.ID, &s.HIP, &s.Name, &s.NameUK, &s.Bayer, &s.Con, &s.RA, &s.Dec, &s.Mag, &s.Spect); err != nil {
		return Star{}, fmt.Errorf("scan star: %w", err)
	}
	return s, nil
}

// SearchStars finds stars by name (English or Ukrainian), Bayer designation,
// constellation, or Hipparcos number ("32349" or "HIP 32349"), brightest
// first. An empty query lists the brightest stars.
func (ix *Index) SearchStars(ctx context.Context, q string, limit int) ([]Star, error) {
	db, err := ix.handle(ctx)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	query, args := starQuery(q, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search stars %q: %w", q, err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out := []Star{}
	for rows.Next() {
		s, err := scanStar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stars: %w", err)
	}
	return out, nil
}

// starQuery builds the search SQL. A number (optionally prefixed "hip") is an
// exact Hipparcos lookup; anything else is a substring match on the search key.
func starQuery(q string, limit int) (string, []any) {
	base := `SELECT ` + starColumns + ` FROM stars WHERE `
	digits := strings.TrimSpace(strings.TrimPrefix(q, "hip"))
	if n, err := strconv.ParseInt(digits, 10, 64); err == nil && n > 0 {
		return base + `hip = ? ORDER BY mag LIMIT ?`, []any{n, limit}
	}
	if q == "" {
		return base + `1 ORDER BY mag LIMIT ?`, []any{limit}
	}
	return base + `search LIKE ? ESCAPE '\' ORDER BY mag LIMIT ?`, []any{"%" + escapeLike(q) + "%", limit}
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Constellation is an IAU constellation with its brightest star.
type Constellation struct {
	Abbr      string `json:"abbr"`
	NameLatin string `json:"name_latin"`
	NameEN    string `json:"name_en"`
	Brightest Star   `json:"brightest"`
}

// Constellations lists the IAU constellations.
func (ix *Index) Constellations(ctx context.Context) ([]Constellation, error) {
	db, err := ix.handle(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT c.abbr, c.name_latin, c.name_en,
		s.id, s.hip, s.name, s.name_uk, s.bayer, s.con, s.ra, s.dec, s.mag, s.spect
		FROM constellations c JOIN stars s ON s.id = c.brightest_star_id ORDER BY c.abbr`)
	if err != nil {
		return nil, fmt.Errorf("query constellations: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out := []Constellation{}
	for rows.Next() {
		var c Constellation
		s := &c.Brightest
		if err := rows.Scan(&c.Abbr, &c.NameLatin, &c.NameEN,
			&s.ID, &s.HIP, &s.Name, &s.NameUK, &s.Bayer, &s.Con, &s.RA, &s.Dec, &s.Mag, &s.Spect); err != nil {
			return nil, fmt.Errorf("scan constellation: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate constellations: %w", err)
	}
	return out, nil
}

// Culture is a sky culture with counts.
type Culture struct {
	ID             string `json:"id"`
	Region         string `json:"region"`
	Classification string `json:"classification"`
	Constellations int    `json:"constellations"`
	Edges          int    `json:"edges"`
}

// Cultures lists the sky cultures.
func (ix *Index) Cultures(ctx context.Context) ([]Culture, error) {
	db, err := ix.handle(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id, c.region, c.classification,
		(SELECT COUNT(*) FROM culture_constellations x WHERE x.culture_id = c.id),
		(SELECT COUNT(*) FROM culture_edges e WHERE e.culture_id = c.id)
		FROM cultures c ORDER BY c.id`)
	if err != nil {
		return nil, fmt.Errorf("query cultures: %w", err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out := []Culture{}
	for rows.Next() {
		var c Culture
		if err := rows.Scan(&c.ID, &c.Region, &c.Classification, &c.Constellations, &c.Edges); err != nil {
			return nil, fmt.Errorf("scan culture: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cultures: %w", err)
	}
	return out, nil
}

// Figure is one constellation of a culture.
type Figure struct {
	ID         string `json:"id"`
	NameEN     string `json:"name_en"`
	NameNative string `json:"name_native"`
	Edges      int    `json:"edges"`
}

// CultureFigures lists the figures of a culture.
func (ix *Index) CultureFigures(ctx context.Context, culture string) ([]Figure, error) {
	db, err := ix.handle(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT k.id, k.name_en, k.name_native,
		(SELECT COUNT(*) FROM culture_edges e WHERE e.culture_id = k.culture_id AND e.constellation_id = k.id)
		FROM culture_constellations k WHERE k.culture_id = ? ORDER BY k.id`, culture)
	if err != nil {
		return nil, fmt.Errorf("query figures of %s: %w", culture, err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out := []Figure{}
	for rows.Next() {
		var f Figure
		if err := rows.Scan(&f.ID, &f.NameEN, &f.NameNative, &f.Edges); err != nil {
			return nil, fmt.Errorf("scan figure: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate figures: %w", err)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// FigureLines is a figure ready to draw: its stars and the segments between
// them (pairs of Hipparcos numbers; Line is the polyline each belongs to).
type FigureLines struct {
	Figure
	Stars []Star       `json:"stars"`
	Lines []FigureEdge `json:"lines"`
}

// FigureEdge is one segment of a figure.
type FigureEdge struct {
	Line int   `json:"line"`
	A    int64 `json:"a"`
	B    int64 `json:"b"`
}

// FigureLines returns the stars and segments of one figure.
func (ix *Index) FigureLines(ctx context.Context, culture, figure string) (FigureLines, error) {
	db, err := ix.handle(ctx)
	if err != nil {
		return FigureLines{}, err
	}
	var out FigureLines
	err = db.QueryRowContext(ctx, `SELECT id, name_en, name_native FROM culture_constellations
		WHERE culture_id = ? AND id = ?`, culture, figure).Scan(&out.ID, &out.NameEN, &out.NameNative)
	if errors.Is(err, sql.ErrNoRows) {
		return FigureLines{}, ErrNotFound
	}
	if err != nil {
		return FigureLines{}, fmt.Errorf("query figure %s/%s: %w", culture, figure, err)
	}

	rows, err := db.QueryContext(ctx, `SELECT line, hip_a, hip_b FROM culture_edges
		WHERE culture_id = ? AND constellation_id = ? ORDER BY line, seq`, culture, figure)
	if err != nil {
		return FigureLines{}, fmt.Errorf("query edges %s/%s: %w", culture, figure, err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	out.Lines = []FigureEdge{}
	for rows.Next() {
		var e FigureEdge
		if err := rows.Scan(&e.Line, &e.A, &e.B); err != nil {
			return FigureLines{}, fmt.Errorf("scan edge: %w", err)
		}
		out.Lines = append(out.Lines, e)
	}
	if err := rows.Err(); err != nil {
		return FigureLines{}, fmt.Errorf("iterate edges: %w", err)
	}
	out.Edges = len(out.Lines)

	out.Stars = []Star{}
	rows2, err := db.QueryContext(ctx, `SELECT `+starColumns+` FROM stars WHERE hip IN (
		SELECT hip_a FROM culture_edges WHERE culture_id = ?1 AND constellation_id = ?2
		UNION SELECT hip_b FROM culture_edges WHERE culture_id = ?1 AND constellation_id = ?2) ORDER BY hip`,
		culture, figure)
	if err != nil {
		return FigureLines{}, fmt.Errorf("query stars of %s/%s: %w", culture, figure, err)
	}
	defer rows2.Close() //nolint:errcheck // Err() below reports failures
	for rows2.Next() {
		s, err := scanStar(rows2)
		if err != nil {
			return FigureLines{}, err
		}
		out.Stars = append(out.Stars, s)
	}
	if err := rows2.Err(); err != nil {
		return FigureLines{}, fmt.Errorf("iterate figure stars: %w", err)
	}
	return out, nil
}
