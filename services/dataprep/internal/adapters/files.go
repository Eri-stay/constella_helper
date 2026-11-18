package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"constella/internal/cosmosdb"
	"constella/internal/domain/catalog"
	"constella/services/dataprep/internal/app"
)

// FileSources reads the pipeline input from files.
type FileSources struct {
	HYGPath     string // hyg_v42.csv
	CulturesDir string // one Stellarium index.json per culture: <dir>/*.json
	NamesUKPath string // curated Ukrainian names; optional
}

// Load reads HYG, every culture file and the names file.
func (f FileSources) Load(ctx context.Context) (app.Input, error) {
	var in app.Input
	hyg, err := os.Open(f.HYGPath)
	if err != nil {
		return in, fmt.Errorf("open hyg: %w", err)
	}
	defer hyg.Close() //nolint:errcheck // read-only file
	if in.Stars, err = ReadHYG(hyg); err != nil {
		return in, err
	}
	if err := ctx.Err(); err != nil {
		return in, err
	}

	if in.Cultures, err = f.loadCultures(); err != nil {
		return in, err
	}
	if f.NamesUKPath != "" {
		names, err := os.Open(f.NamesUKPath)
		if err != nil {
			return in, fmt.Errorf("open ukrainian names: %w", err)
		}
		defer names.Close() //nolint:errcheck // read-only file
		if in.NamesUK, err = ReadNamesUK(names); err != nil {
			return in, err
		}
	}
	return in, nil
}

func (f FileSources) loadCultures() ([]catalog.Culture, error) {
	paths, err := filepath.Glob(filepath.Join(f.CulturesDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("list cultures in %s: %w", f.CulturesDir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no skyculture files in %s", f.CulturesDir)
	}
	slices.Sort(paths)
	cultures := make([]catalog.Culture, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		cu, err := ParseSkyculture(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		cultures = append(cultures, cu)
	}
	return cultures, nil
}

// ArtifactWriter writes cosmos.db files into a directory. A file appears under
// its final name only when it is complete (written to a temp name, then
// renamed), so a reader never sees a half-written artifact.
type ArtifactWriter struct{ Dir string }

// Write implements app.Writer.
func (w ArtifactWriter) Write(ctx context.Context, name string, ds catalog.Dataset) (app.Artifact, error) {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return app.Artifact{}, fmt.Errorf("create artifacts dir: %w", err)
	}
	final := filepath.Join(w.Dir, name)
	tmp := final + ".tmp"
	res, err := cosmosdb.Write(ctx, tmp, ds)
	if err != nil {
		_ = os.Remove(tmp) // best effort: the write error is what matters
		return app.Artifact{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return app.Artifact{}, fmt.Errorf("publish artifact %s: %w", name, err)
	}
	return app.Artifact{File: name, SHA256: res.SHA256, SizeBytes: res.Size}, nil
}
