// Package adapters connects dataprep to files, SQLite and HTTP.
package adapters

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"

	"constella/internal/domain/catalog"
)

// ReadHYG parses the HYG v4.2 CSV (header row required). Missing numbers read
// as zero; a missing colour index stays nil.
func ReadHYG(r io.Reader) ([]catalog.Star, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read hyg header: %w", err)
	}
	col := map[string]int{}
	for i, name := range head {
		col[name] = i
	}
	for _, need := range []string{"id", "hip", "proper", "ra", "dec", "mag", "con", "bayer", "flam", "spect", "ci"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("hyg header misses column %q", need)
		}
	}

	var stars []catalog.Star
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return stars, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read hyg line %d: %w", line, err)
		}
		s, err := parseStar(rec, col)
		if err != nil {
			return nil, fmt.Errorf("hyg line %d: %w", line, err)
		}
		stars = append(stars, s)
	}
}

func parseStar(rec []string, col map[string]int) (catalog.Star, error) {
	p := fieldParser{rec: rec, col: col}
	s := catalog.Star{
		ID:     p.int("id"),
		HIP:    p.int("hip"),
		Name:   p.str("proper"),
		Bayer:  p.str("bayer"),
		Flam:   int(p.int("flam")),
		Con:    p.str("con"),
		RA:     p.float("ra"),
		Dec:    p.float("dec"),
		Dist:   p.float("dist"),
		PMRA:   p.float("pmra"),
		PMDec:  p.float("pmdec"),
		Mag:    p.float("mag"),
		AbsMag: p.float("absmag"),
		Spect:  p.str("spect"),
	}
	if p.str("ci") != "" {
		ci := p.float("ci")
		s.CI = &ci
	}
	return s, p.err
}

// fieldParser reads typed fields of one record and remembers the first error.
type fieldParser struct {
	rec []string
	col map[string]int
	err error
}

func (p *fieldParser) str(name string) string {
	i, ok := p.col[name]
	if !ok || i >= len(p.rec) {
		return ""
	}
	return p.rec[i]
}

func (p *fieldParser) int(name string) int64 {
	v := p.str(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("column %s: %w", name, err)
	}
	return n
}

func (p *fieldParser) float(name string) float64 {
	v := p.str(name)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("column %s: %w", name, err)
	}
	return f
}
