package adapters

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"constella/internal/domain/catalog"
)

// indexFile is the part of a Stellarium skyculture index.json that dataprep
// reads. Vertices are Hipparcos numbers, but a file may also hold sky
// coordinates or deep-sky ids; those are kept as raw JSON and become NoStar.
type indexFile struct {
	ID             string   `json:"id"`
	Region         string   `json:"region"`
	Classification []string `json:"classification"`
	Constellations []struct {
		ID         string              `json:"id"`
		Lines      [][]json.RawMessage `json:"lines"`
		CommonName struct {
			English string `json:"english"`
			Native  string `json:"native"`
		} `json:"common_name"`
	} `json:"constellations"`
}

// ParseSkyculture reads one skyculture index.json.
func ParseSkyculture(data []byte) (catalog.Culture, error) {
	var f indexFile
	if err := json.Unmarshal(data, &f); err != nil {
		return catalog.Culture{}, fmt.Errorf("parse skyculture: %w", err)
	}
	if f.ID == "" {
		return catalog.Culture{}, errors.New("skyculture has no id")
	}
	cu := catalog.Culture{ID: f.ID, Region: f.Region, Classification: f.Classification}
	prefix := "CON " + f.ID + " "
	for _, c := range f.Constellations {
		cc := catalog.CultureConstellation{
			ID:         strings.TrimPrefix(c.ID, prefix),
			NameEN:     c.CommonName.English,
			NameNative: c.CommonName.Native,
		}
		for _, line := range c.Lines {
			poly := make([]int64, len(line))
			for i, raw := range line {
				poly[i] = vertex(raw)
			}
			cc.Polylines = append(cc.Polylines, poly)
		}
		cu.Constellations = append(cu.Constellations, cc)
	}
	return cu, nil
}

// vertex returns the Hipparcos number, or NoStar for anything else.
func vertex(raw json.RawMessage) int64 {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return catalog.NoStar
	}
	n, ok := v.(json.Number)
	if !ok {
		return catalog.NoStar
	}
	hip, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || hip <= 0 {
		return catalog.NoStar
	}
	return hip
}

// ReadNamesUK parses the curated Ukrainian star names: CSV "hip,name_uk" with
// a header row.
func ReadNamesUK(r io.Reader) (map[int64]string, error) {
	cr := csv.NewReader(r)
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read names header: %w", err)
	}
	if len(head) < 2 || head[0] != "hip" || head[1] != "name_uk" {
		return nil, fmt.Errorf("names header must be hip,name_uk; got %v", head)
	}
	out := map[int64]string{}
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read names line %d: %w", line, err)
		}
		hip, err := strconv.ParseInt(rec[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("names line %d: %w", line, err)
		}
		out[hip] = rec[1]
	}
}
