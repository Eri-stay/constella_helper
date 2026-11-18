package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
	"strings"
)

// ContentHasher computes the content hash stored in cosmos.db (meta
// content_hash). The writer feeds it the dataset; the validator feeds it what
// it reads back from the file. Equal hashes mean the file holds exactly the
// dataset that was built.
//
// Records must be added in the dataset order (see Dataset) and in the order
// stars, constellations, cultures, culture constellations, edges. Only the
// fields written below take part, so a reader needs to fill only those.
type ContentHasher struct{ h hash.Hash }

// NewContentHasher returns an empty hasher.
func NewContentHasher() *ContentHasher { return &ContentHasher{h: sha256.New()} }

func (c *ContentHasher) line(fields ...string) {
	for _, f := range fields {
		// Fields never contain NUL; it separates them unambiguously.
		_, _ = c.h.Write([]byte(f))
		_, _ = c.h.Write([]byte{0})
	}
	_, _ = c.h.Write([]byte{'\n'})
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
func itoa(v int64) string  { return strconv.FormatInt(v, 10) }

// Star adds a star record (id, hip, con, name, name_uk, bayer, ra, dec, mag).
func (c *ContentHasher) Star(s Star) {
	c.line("S", itoa(s.ID), itoa(s.HIP), s.Con, s.Name, s.NameUK, s.Bayer, num(s.RA), num(s.Dec), num(s.Mag))
}

// Constellation adds an IAU constellation record.
func (c *ContentHasher) Constellation(k Constellation) {
	c.line("C", k.Abbr, k.NameLatin, k.NameEN, itoa(k.BrightestStarID))
}

// Culture adds a culture record; classification is joined with commas.
func (c *ContentHasher) Culture(id, region, classification string) {
	c.line("U", id, region, classification)
}

// CultureConstellation adds one figure of a culture.
func (c *ContentHasher) CultureConstellation(culture string, k CultureConstellation) {
	c.line("N", culture, k.ID, k.NameEN, k.NameNative)
}

// Edge adds one line segment.
func (c *ContentHasher) Edge(e Edge) {
	c.line("E", e.Culture, e.Constellation, strconv.Itoa(e.Line), strconv.Itoa(e.Seq), itoa(e.A), itoa(e.B))
}

// Sum returns the lowercase hex SHA-256.
func (c *ContentHasher) Sum() string { return hex.EncodeToString(c.h.Sum(nil)) }

// HashDataset hashes a whole dataset in canonical order.
func HashDataset(ds Dataset) string {
	h := NewContentHasher()
	for _, s := range ds.Stars {
		h.Star(s)
	}
	for _, k := range ds.Constellations {
		h.Constellation(k)
	}
	for _, cu := range ds.Cultures {
		h.Culture(cu.ID, cu.Region, JoinClassification(cu.Classification))
	}
	for _, cu := range ds.Cultures {
		for _, k := range cu.Constellations {
			h.CultureConstellation(cu.ID, k)
		}
	}
	for _, e := range ds.Edges {
		h.Edge(e)
	}
	return h.Sum()
}

// JoinClassification renders a classification list as stored in the database.
func JoinClassification(c []string) string { return strings.Join(c, ",") }
