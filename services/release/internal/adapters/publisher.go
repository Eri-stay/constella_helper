package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"constella/services/release/internal/app"
)

// ManifestName is the file clients poll for updates.
const ManifestName = "version.json"

// Manifest is the published version.json (schema 1). Data and code update
// separately; only the data channel exists on the platform.
type Manifest struct {
	Schema      int          `json:"schema"`
	ReleaseID   string       `json:"release_id"`
	PublishedAt time.Time    `json:"published_at"`
	Data        ManifestData `json:"data"`
}

// ManifestData describes the cosmos.db of a release.
type ManifestData struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// FilePublisher publishes into a directory that the gateway serves at /updates/.
type FilePublisher struct {
	ArtifactsDir string // shared artifacts volume (read-only here)
	ReleasesDir  string // <dir>/version.json and <dir>/data/cosmos-<build>.db

	mu sync.Mutex // one switch at a time
}

// Switch implements app.Publisher.
func (p *FilePublisher) Switch(ctx context.Context, releaseID string, a app.Reserved, at time.Time) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	name := filepath.Base(a.Artifact) // never trust a path from another service
	dataDir := filepath.Join(p.ReleasesDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create releases dir: %w", err)
	}
	if err := copyVerified(ctx, filepath.Join(p.ArtifactsDir, name), filepath.Join(dataDir, name), a.SHA256); err != nil {
		return nil, err
	}
	previous, err := p.read()
	if err != nil {
		return nil, err
	}
	m := Manifest{Schema: 1, ReleaseID: releaseID, PublishedAt: at.UTC(), Data: ManifestData{
		Version: versionOf(name), URL: "data/" + name, SHA256: a.SHA256, Size: a.SizeBytes}}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeAtomic(filepath.Join(p.ReleasesDir, ManifestName), body); err != nil {
		return nil, err
	}
	return previous, nil
}

// versionOf turns "cosmos-<build>.db" into "<build>".
func versionOf(name string) string {
	base := name[:len(name)-len(filepath.Ext(name))]
	if len(base) > 7 && base[:7] == "cosmos-" {
		return base[7:]
	}
	return base
}

// Restore implements app.Publisher.
func (p *FilePublisher) Restore(_ context.Context, previous []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := filepath.Join(p.ReleasesDir, ManifestName)
	if previous == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove manifest: %w", err)
		}
		return nil
	}
	return writeAtomic(path, previous)
}

// Current implements app.Publisher.
func (p *FilePublisher) Current(context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.read()
}

func (p *FilePublisher) read() ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(p.ReleasesDir, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return b, nil
}

// writeAtomic writes next to the target and renames, so readers see the old
// file or the new one, never half of it.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// copyVerified copies src to dst and fails if the bytes do not hash to want.
// The copy is kept only when it is correct.
func copyVerified(ctx context.Context, src, dst, want string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer in.Close() //nolint:errcheck // read-only file
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), &ctxReader{ctx: ctx, r: in})
	if cErr := out.Close(); err == nil {
		err = cErr
	}
	if err != nil {
		_ = os.Remove(tmp) // best effort: the copy error is what matters
		return fmt.Errorf("copy artifact: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(tmp) // best effort: the mismatch is what matters
		return fmt.Errorf("artifact sha256 is %s, catalog announced %s", got, want)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
