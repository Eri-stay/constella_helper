package cosmosdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"constella/internal/domain/catalog"
)

// ErrNotCosmos is returned for a file that is not a readable cosmos.db.
var ErrNotCosmos = errors.New("not a cosmos database")

// Inspect measures the file at path for catalog.Validate. expectedSHA is the
// hash announced by the producer; Validate compares it with the real hash.
func Inspect(ctx context.Context, path, expectedSHA string) (catalog.Snapshot, error) {
	snap := catalog.Snapshot{ExpectedSHA256: expectedSHA}
	sum, _, err := FileSHA256(path)
	if err != nil {
		return snap, err
	}
	snap.ActualSHA256 = sum

	db, err := OpenReadOnly(path)
	if err != nil {
		return snap, err
	}
	defer db.Close() //nolint:errcheck // read-only handle

	if err := readMeta(ctx, db, &snap); err != nil {
		return snap, err
	}
	if err := readCounts(ctx, db, &snap); err != nil {
		return snap, err
	}
	if snap.ComputedContentHash, err = ContentHash(ctx, db); err != nil {
		return snap, err
	}
	return snap, nil
}

func readMeta(ctx context.Context, db *sql.DB, snap *catalog.Snapshot) error {
	return each(ctx, db, "meta", `SELECT key, value FROM meta`, func(r *sql.Rows) error {
		var k, v string
		if err := r.Scan(&k, &v); err != nil {
			return err
		}
		switch k {
		case MetaSchemaVersion:
			snap.SchemaVersion, _ = strconv.Atoi(v) // a bad value stays 0 and fails validation
		case MetaContentHash:
			snap.StoredContentHash = v
		case MetaDroppedEdges:
			snap.DroppedEdges, _ = strconv.Atoi(v) // informational; 0 when unreadable
		}
		return nil
	})
}

func readCounts(ctx context.Context, db *sql.DB, snap *catalog.Snapshot) error {
	snap.TierCounts = make([]int, len(catalog.Tiers))
	for i, t := range catalog.Tiers {
		if err := count(ctx, db, &snap.TierCounts[i], `SELECT COUNT(*) FROM stars WHERE mag < ?`, t.MagLimit); err != nil {
			return err
		}
	}
	queries := []struct {
		dst *int
		sql string
	}{
		{&snap.Constellations, `SELECT COUNT(*) FROM constellations`},
		{&snap.NoBrightest, `SELECT COUNT(*) FROM constellations c WHERE NOT EXISTS
			(SELECT 1 FROM stars s WHERE s.id = c.brightest_star_id AND s.con = c.abbr)`},
		{&snap.Cultures, `SELECT COUNT(*) FROM cultures`},
		{&snap.Edges, `SELECT COUNT(*) FROM culture_edges`},
		{&snap.EdgesNoStar, `SELECT
			(SELECT COUNT(*) FROM culture_edges e WHERE NOT EXISTS (SELECT 1 FROM stars s WHERE s.hip = e.hip_a AND s.hip <> 0)) +
			(SELECT COUNT(*) FROM culture_edges e WHERE NOT EXISTS (SELECT 1 FROM stars s WHERE s.hip = e.hip_b AND s.hip <> 0))`},
	}
	for _, q := range queries {
		if err := count(ctx, db, q.dst, q.sql); err != nil {
			return err
		}
	}
	return nil
}

func count(ctx context.Context, db *sql.DB, dst *int, query string, args ...any) error {
	if err := db.QueryRowContext(ctx, query, args...).Scan(dst); err != nil {
		return fmt.Errorf("%w: %v", ErrNotCosmos, err)
	}
	return nil
}

// ContentHash recomputes the content hash from the tables, in the canonical
// order of catalog.Dataset.
func ContentHash(ctx context.Context, db *sql.DB) (string, error) {
	h := catalog.NewContentHasher()
	steps := []func() error{
		func() error { return hashStars(ctx, db, h) },
		func() error { return hashConstellations(ctx, db, h) },
		func() error { return hashCultures(ctx, db, h) },
		func() error { return hashCultureConstellations(ctx, db, h) },
		func() error { return hashEdges(ctx, db, h) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return "", err
		}
	}
	return h.Sum(), nil
}

// each runs query and calls fn for every row, wrapping errors with what.
func each(ctx context.Context, db *sql.DB, what, query string, fn func(rows *sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("%w: query %s: %v", ErrNotCosmos, what, err)
	}
	defer rows.Close() //nolint:errcheck // Err() below reports failures
	for rows.Next() {
		if err := fn(rows); err != nil {
			return fmt.Errorf("scan %s: %w", what, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s: %w", what, err)
	}
	return nil
}

func hashStars(ctx context.Context, db *sql.DB, h *catalog.ContentHasher) error {
	return each(ctx, db, "stars",
		`SELECT id, hip, con, name, name_uk, bayer, ra, dec, mag FROM stars ORDER BY id`,
		func(r *sql.Rows) error {
			var s catalog.Star
			if err := r.Scan(&s.ID, &s.HIP, &s.Con, &s.Name, &s.NameUK, &s.Bayer, &s.RA, &s.Dec, &s.Mag); err != nil {
				return err
			}
			h.Star(s)
			return nil
		})
}

func hashConstellations(ctx context.Context, db *sql.DB, h *catalog.ContentHasher) error {
	return each(ctx, db, "constellations",
		`SELECT abbr, name_latin, name_en, brightest_star_id FROM constellations ORDER BY abbr`,
		func(r *sql.Rows) error {
			var c catalog.Constellation
			if err := r.Scan(&c.Abbr, &c.NameLatin, &c.NameEN, &c.BrightestStarID); err != nil {
				return err
			}
			h.Constellation(c)
			return nil
		})
}

func hashCultures(ctx context.Context, db *sql.DB, h *catalog.ContentHasher) error {
	return each(ctx, db, "cultures", `SELECT id, region, classification FROM cultures ORDER BY id`,
		func(r *sql.Rows) error {
			var id, region, class string
			if err := r.Scan(&id, &region, &class); err != nil {
				return err
			}
			h.Culture(id, region, class)
			return nil
		})
}

func hashCultureConstellations(ctx context.Context, db *sql.DB, h *catalog.ContentHasher) error {
	return each(ctx, db, "culture constellations",
		`SELECT culture_id, id, name_en, name_native FROM culture_constellations ORDER BY culture_id, id`,
		func(r *sql.Rows) error {
			var culture string
			var k catalog.CultureConstellation
			if err := r.Scan(&culture, &k.ID, &k.NameEN, &k.NameNative); err != nil {
				return err
			}
			h.CultureConstellation(culture, k)
			return nil
		})
}

func hashEdges(ctx context.Context, db *sql.DB, h *catalog.ContentHasher) error {
	return each(ctx, db, "edges",
		`SELECT culture_id, constellation_id, line, seq, hip_a, hip_b FROM culture_edges
		 ORDER BY culture_id, constellation_id, line, seq`,
		func(r *sql.Rows) error {
			var e catalog.Edge
			if err := r.Scan(&e.Culture, &e.Constellation, &e.Line, &e.Seq, &e.A, &e.B); err != nil {
				return err
			}
			h.Edge(e)
			return nil
		})
}
