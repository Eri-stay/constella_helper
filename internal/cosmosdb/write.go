package cosmosdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"constella/internal/domain/catalog"
)

// Written describes a finished file.
type Written struct {
	SHA256      string // of the file bytes
	Size        int64
	ContentHash string // of the dataset, also stored in meta
}

// Write creates a cosmos.db at path from ds. The file is written completely
// (rollback journal, no WAL, so it is a single file) before Write returns.
// An existing file is replaced.
func Write(ctx context.Context, path string, ds catalog.Dataset) (Written, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Written{}, fmt.Errorf("remove old %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", uri(path, "rwc")+"&_pragma=journal_mode(DELETE)&_pragma=synchronous(OFF)")
	if err != nil {
		return Written{}, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	hash := catalog.HashDataset(ds)
	fillErr := fill(ctx, db, ds, hash)
	if err := db.Close(); err != nil {
		fillErr = errors.Join(fillErr, fmt.Errorf("close %s: %w", path, err))
	}
	if fillErr != nil {
		return Written{}, fillErr
	}
	sum, size, err := FileSHA256(path)
	if err != nil {
		return Written{}, err
	}
	return Written{SHA256: sum, Size: size, ContentHash: hash}, nil
}

func fill(ctx context.Context, db *sql.DB, ds catalog.Dataset, contentHash string) error {
	for i, ddl := range Schema {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("apply schema statement %d: %w", i, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := insertAll(ctx, tx, ds, contentHash); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func insertAll(ctx context.Context, tx *sql.Tx, ds catalog.Dataset, contentHash string) error {
	steps := []func() error{
		func() error { return insertStars(ctx, tx, ds.Stars) },
		func() error { return insertConstellations(ctx, tx, ds.Constellations) },
		func() error { return insertCultures(ctx, tx, ds.Cultures) },
		func() error { return insertEdges(ctx, tx, ds.Edges) },
		func() error {
			for _, kv := range [][2]string{
				{MetaSchemaVersion, strconv.Itoa(catalog.SchemaVersion)},
				{MetaContentHash, contentHash},
				{MetaDroppedEdges, strconv.Itoa(ds.DroppedEdges)},
			} {
				if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
					return fmt.Errorf("insert meta %s: %w", kv[0], err)
				}
			}
			return nil
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func insertStars(ctx context.Context, tx *sql.Tx, stars []catalog.Star) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO stars
		(id, hip, name, name_uk, bayer, flam, con, ra, dec, dist, pmra, pmdec, mag, absmag, spect, ci, search)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare star insert: %w", err)
	}
	defer stmt.Close() //nolint:errcheck // statement of a finished tx
	for i, s := range stars {
		if i%10_000 == 0 {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("write stars: %w", err)
			}
		}
		var ci any
		if s.CI != nil {
			ci = *s.CI
		}
		if _, err := stmt.ExecContext(ctx, s.ID, s.HIP, s.Name, s.NameUK, s.Bayer, s.Flam, s.Con,
			s.RA, s.Dec, s.Dist, s.PMRA, s.PMDec, s.Mag, s.AbsMag, s.Spect, ci, SearchKey(s)); err != nil {
			return fmt.Errorf("insert star %d: %w", s.ID, err)
		}
	}
	return nil
}

// SearchKey is the lower-cased text a star is searched by: names, Bayer
// designation, constellation and the Hipparcos number as "hip<N>".
func SearchKey(s catalog.Star) string {
	parts := []string{s.Name, s.NameUK, s.Bayer, s.Con}
	if s.HIP != 0 {
		parts = append(parts, "hip"+strconv.FormatInt(s.HIP, 10))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func insertConstellations(ctx context.Context, tx *sql.Tx, cons []catalog.Constellation) error {
	for _, c := range cons {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO constellations (abbr, name_latin, name_en, brightest_star_id) VALUES (?, ?, ?, ?)`,
			c.Abbr, c.NameLatin, c.NameEN, c.BrightestStarID); err != nil {
			return fmt.Errorf("insert constellation %s: %w", c.Abbr, err)
		}
	}
	return nil
}

func insertCultures(ctx context.Context, tx *sql.Tx, cultures []catalog.Culture) error {
	for _, cu := range cultures {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO cultures (id, region, classification) VALUES (?, ?, ?)`,
			cu.ID, cu.Region, catalog.JoinClassification(cu.Classification)); err != nil {
			return fmt.Errorf("insert culture %s: %w", cu.ID, err)
		}
		for _, k := range cu.Constellations {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO culture_constellations (culture_id, id, name_en, name_native) VALUES (?, ?, ?, ?)`,
				cu.ID, k.ID, k.NameEN, k.NameNative); err != nil {
				return fmt.Errorf("insert %s/%s: %w", cu.ID, k.ID, err)
			}
		}
	}
	return nil
}

func insertEdges(ctx context.Context, tx *sql.Tx, edges []catalog.Edge) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO culture_edges
		(culture_id, constellation_id, line, seq, hip_a, hip_b) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare edge insert: %w", err)
	}
	defer stmt.Close() //nolint:errcheck // statement of a finished tx
	for _, e := range edges {
		if _, err := stmt.ExecContext(ctx, e.Culture, e.Constellation, e.Line, e.Seq, e.A, e.B); err != nil {
			return fmt.Errorf("insert edge %s/%s: %w", e.Culture, e.Constellation, err)
		}
	}
	return nil
}

// FileSHA256 returns the hex SHA-256 and the size of the file at path.
func FileSHA256(path string) (sum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only file
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
