// Package cosmosdb is the file format of cosmos.db, the content database of
// the Constella client: its schema, the writer used by dataprep and the
// inspector used by catalog. Both sides share this package so they cannot
// drift apart.
package cosmosdb

import (
	"database/sql"
	"fmt"
	"path/filepath"

	// Pure-Go SQLite driver (no CGO), registered as "sqlite".
	_ "modernc.org/sqlite"
)

// Schema is the DDL of cosmos.db (schema version catalog.SchemaVersion).
// Strings are NOT NULL and empty for "none", so the content hash is simple.
var Schema = []string{
	`CREATE TABLE meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	) WITHOUT ROWID`,
	`CREATE TABLE stars (
		id      INTEGER PRIMARY KEY,
		hip     INTEGER NOT NULL,
		name    TEXT    NOT NULL,
		name_uk TEXT    NOT NULL,
		bayer   TEXT    NOT NULL,
		flam    INTEGER NOT NULL,
		con     TEXT    NOT NULL,
		ra      REAL    NOT NULL,
		dec     REAL    NOT NULL,
		dist    REAL    NOT NULL,
		pmra    REAL    NOT NULL,
		pmdec   REAL    NOT NULL,
		mag     REAL    NOT NULL,
		absmag  REAL    NOT NULL,
		spect   TEXT    NOT NULL,
		ci      REAL,
		search  TEXT    NOT NULL
	)`,
	`CREATE INDEX stars_hip ON stars(hip) WHERE hip <> 0`,
	`CREATE INDEX stars_mag ON stars(mag)`,
	`CREATE INDEX stars_con ON stars(con)`,
	`CREATE TABLE constellations (
		abbr              TEXT PRIMARY KEY,
		name_latin        TEXT    NOT NULL,
		name_en           TEXT    NOT NULL,
		brightest_star_id INTEGER NOT NULL
	) WITHOUT ROWID`,
	`CREATE TABLE cultures (
		id             TEXT PRIMARY KEY,
		region         TEXT NOT NULL,
		classification TEXT NOT NULL
	) WITHOUT ROWID`,
	`CREATE TABLE culture_constellations (
		culture_id  TEXT NOT NULL,
		id          TEXT NOT NULL,
		name_en     TEXT NOT NULL,
		name_native TEXT NOT NULL,
		PRIMARY KEY (culture_id, id)
	) WITHOUT ROWID`,
	`CREATE TABLE culture_edges (
		culture_id       TEXT    NOT NULL,
		constellation_id TEXT    NOT NULL,
		line             INTEGER NOT NULL,
		seq              INTEGER NOT NULL,
		hip_a            INTEGER NOT NULL,
		hip_b            INTEGER NOT NULL,
		PRIMARY KEY (culture_id, constellation_id, line, seq)
	) WITHOUT ROWID`,
}

// Meta keys.
const (
	MetaSchemaVersion = "schema_version"
	MetaContentHash   = "content_hash"
	MetaDroppedEdges  = "dropped_edges"
)

// uri returns the SQLite URI of a file with the given mode ("rwc" or "ro").
func uri(path, mode string) string {
	return "file:" + filepath.ToSlash(path) + "?mode=" + mode
}

// OpenReadOnly opens an existing cosmos.db for reading. The artifacts volume
// is mounted read-only in consumers, so nothing may try to write to it.
func OpenReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", uri(path, "ro"))
	if err != nil {
		return nil, fmt.Errorf("open cosmos db %s: %w", path, err)
	}
	return db, nil
}
