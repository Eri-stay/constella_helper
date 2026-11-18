// Package app holds the research use cases: a sweep generates constellations
// with several strategies, parameters and seeds from the catalog's stars and
// compares every result with a sky culture.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"constella/internal/domain/compare"
	"constella/internal/domain/genconst"
	"constella/internal/platform/events"
)

// Producer is the name research puts on its events.
const Producer = "research"

// Sweep statuses.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Failure codes.
const (
	CodeCatalogDown = "CATALOG_UNAVAILABLE"
	CodeNoCulture   = "CULTURE_NOT_FOUND"
	CodeInterrupted = "INTERRUPTED"
	CodeCancelled   = "CANCELLED"
	CodeInternal    = "INTERNAL"
)

// MaxRuns limits one sweep.
const MaxRuns = 200

var (
	// ErrInvalid is returned for a sweep that cannot be run.
	ErrInvalid = errors.New("invalid sweep")
	// ErrNotFound is returned for an unknown sweep.
	ErrNotFound = errors.New("sweep not found")
	// ErrNoCulture is returned by Catalog when the culture does not exist.
	ErrNoCulture = errors.New("culture not found")
)

var tracer = otel.Tracer("constella/research")

// Spec describes a sweep: the cross product of strategies, eps values and seeds.
type Spec struct {
	Culture    string          `json:"culture"`
	BuildID    string          `json:"build_id,omitempty"` // empty: the active build, fixed at the start
	Strategies []string        `json:"strategies,omitempty"`
	EpsList    []float64       `json:"eps_list,omitempty"`
	Seeds      []uint64        `json:"seeds,omitempty"`
	Params     genconst.Params `json:"params"` // shared settings; strategy, eps and seed are overwritten per run
}

// Sweep is a stored sweep.
type Sweep struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	BuildID     string     `json:"build_id"`
	Culture     string     `json:"culture"`
	Total       int        `json:"total"`
	Done        int        `json:"done"`
	ErrorCode   string     `json:"error_code,omitempty"`
	ErrorDetail string     `json:"error_detail,omitempty"`
	Spec        Spec       `json:"spec"`
}

// Run is the result of one generation compared with the culture.
type Run struct {
	Seq        int             `json:"seq"`
	Params     genconst.Params `json:"params"`
	Stars      int             `json:"stars"`
	Edges      int             `json:"edges"`
	Clusters   int             `json:"clusters"`
	Metrics    compare.Metrics `json:"metrics"`
	Hash       string          `json:"hash"`
	DurationMS float64         `json:"duration_ms"`
}

// Catalog supplies the stars and the culture's edges (gRPC streams).
type Catalog interface {
	// ActiveBuild returns the id of the active catalog build.
	ActiveBuild(ctx context.Context) (string, error)
	Stars(ctx context.Context, buildID string, magMax float64) ([]genconst.Star, error)
	// Edges returns ErrNoCulture for an unknown culture.
	Edges(ctx context.Context, buildID, culture string) ([]genconst.Edge, error)
}

// Store persists sweeps and runs. FinishSweep stores the closing event in the
// outbox in the same transaction.
type Store interface {
	CreateSweep(ctx context.Context, s Sweep) error
	AddRun(ctx context.Context, sweepID string, r Run) error
	FinishSweep(ctx context.Context, s Sweep, ev events.Envelope) error
	Get(ctx context.Context, id string) (Sweep, []Run, error)
	List(ctx context.Context, limit int) ([]Sweep, error)
	Running(ctx context.Context) ([]Sweep, error)
}

// Notifier wakes the outbox relay after a commit.
type Notifier interface{ Notify() }

// Service runs sweeps.
type Service struct {
	store   Store
	catalog Catalog
	notify  Notifier
	log     *slog.Logger
	now     func() time.Time
	wg      sync.WaitGroup
}

// New wires the service.
func New(st Store, c Catalog, n Notifier, log *slog.Logger) *Service {
	return &Service{store: st, catalog: c, notify: n, log: log, now: time.Now}
}

// Normalize fills defaults and validates the spec; it returns the list of
// runs (one parameter set each) the sweep will perform.
func (s Spec) Normalize() (Spec, []genconst.Params, error) {
	if s.Culture == "" {
		return s, nil, fmt.Errorf("%w: culture is required", ErrInvalid)
	}
	if len(s.Strategies) == 0 {
		s.Strategies = genconst.Strategies
	}
	if len(s.EpsList) == 0 {
		s.EpsList = []float64{s.Params.WithDefaults().EpsDeg}
	}
	if len(s.Seeds) == 0 {
		s.Seeds = []uint64{42}
	}
	if n := len(s.Strategies) * len(s.EpsList) * len(s.Seeds); n > MaxRuns {
		return s, nil, fmt.Errorf("%w: %d runs exceed the limit of %d", ErrInvalid, n, MaxRuns)
	}
	var runs []genconst.Params
	for _, st := range s.Strategies {
		for _, eps := range s.EpsList {
			for _, seed := range s.Seeds {
				p := s.Params
				p.Strategy, p.EpsDeg, p.Seed = st, eps, seed
				p = p.WithDefaults()
				if err := p.Validate(); err != nil {
					return s, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
				}
				runs = append(runs, p)
			}
		}
	}
	return s, runs, nil
}

// Start validates the spec, stores a running sweep and runs it in the background.
func (s *Service) Start(ctx context.Context, spec Spec) (Sweep, error) {
	spec, runs, err := spec.Normalize()
	if err != nil {
		return Sweep{}, err
	}
	// The build is fixed now, so a build activated during the sweep cannot mix results.
	build := spec.BuildID
	if build == "" {
		if build, err = s.catalog.ActiveBuild(ctx); err != nil {
			return Sweep{}, fmt.Errorf("%w: find active build: %v", errCatalog, err)
		}
	}
	spec.BuildID = build
	sw := Sweep{ID: newID(s.now()), Status: StatusRunning, CreatedAt: s.now().UTC(), BuildID: build,
		Culture: spec.Culture, Total: len(runs), Spec: spec}
	if err := s.store.CreateSweep(ctx, sw); err != nil {
		return Sweep{}, fmt.Errorf("create sweep: %w", err)
	}
	bg := context.WithoutCancel(ctx) // outlives the request, stays in its trace
	s.wg.Go(func() { s.run(bg, sw, runs) })
	return sw, nil
}

func newID(t time.Time) string {
	return "s" + t.UTC().Format("20060102-150405") + "-" + events.NewID()[:4]
}

// Get returns a sweep with its runs.
func (s *Service) Get(ctx context.Context, id string) (Sweep, []Run, error) {
	return s.store.Get(ctx, id)
}

// List returns the newest sweeps.
func (s *Service) List(ctx context.Context, limit int) ([]Sweep, error) {
	return s.store.List(ctx, limit)
}

// Wait blocks until running sweeps end or ctx is done (graceful shutdown).
func (s *Service) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Recover fails sweeps that a restart interrupted.
func (s *Service) Recover(ctx context.Context) error {
	running, err := s.store.Running(ctx)
	if err != nil {
		return fmt.Errorf("list running sweeps: %w", err)
	}
	for _, sw := range running {
		if err := s.fail(ctx, sw, CodeInterrupted, "process stopped while the sweep was running"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) run(ctx context.Context, sw Sweep, runs []genconst.Params) {
	ctx, span := tracer.Start(ctx, "research sweep", trace.WithAttributes(
		attribute.String("sweep.id", sw.ID), attribute.String("build.id", sw.BuildID),
		attribute.String("culture", sw.Culture), attribute.Int("runs", len(runs))))
	defer span.End()

	err := s.execute(ctx, &sw, runs)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		code := CodeInternal
		switch {
		case errors.Is(err, ErrNoCulture):
			code = CodeNoCulture
		case errors.Is(err, context.Canceled):
			code = CodeCancelled
		case errors.Is(err, errCatalog):
			code = CodeCatalogDown
		}
		s.log.ErrorContext(ctx, "sweep failed", "sweep_id", sw.ID, "code", code, "err", err)
		if fErr := s.fail(ctx, sw, code, err.Error()); fErr != nil {
			s.log.ErrorContext(ctx, "could not record sweep failure", "sweep_id", sw.ID, "err", fErr)
		}
	}
}

var errCatalog = errors.New("catalog unavailable")

func (s *Service) execute(ctx context.Context, sw *Sweep, runs []genconst.Params) error {
	stars, err := s.stars(ctx, sw)
	if err != nil {
		return err
	}
	ref, err := s.catalog.Edges(ctx, sw.BuildID, sw.Culture)
	if err != nil {
		if errors.Is(err, ErrNoCulture) {
			return err
		}
		return fmt.Errorf("%w: read edges of %s: %v", errCatalog, sw.Culture, err)
	}

	var best Run
	for i, p := range runs {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := s.one(ctx, stars, ref, p, i)
		if err != nil {
			return err
		}
		if err := s.store.AddRun(ctx, sw.ID, r); err != nil {
			return fmt.Errorf("store run %d: %w", i, err)
		}
		sw.Done++
		if r.Metrics.Jaccard > best.Metrics.Jaccard {
			best = r
		}
	}

	end := s.now().UTC()
	sw.Status, sw.FinishedAt = StatusCompleted, &end
	ev, err := events.New(events.ResearchRunCompleted, Producer, events.ResearchCompletedData{
		SweepID: sw.ID, BuildID: sw.BuildID, Culture: sw.Culture, Runs: len(runs),
		BestStrategy: best.Params.Strategy, BestJaccard: best.Metrics.Jaccard,
	}, events.WithCorrelation(sw.ID))
	if err != nil {
		return err
	}
	if err := s.store.FinishSweep(ctx, *sw, ev); err != nil {
		return fmt.Errorf("finish sweep: %w", err)
	}
	s.notify.Notify()
	s.log.InfoContext(ctx, "sweep completed", "sweep_id", sw.ID, "runs", len(runs), "best", best.Params.Strategy, "jaccard", best.Metrics.Jaccard)
	return nil
}

// stars loads the candidate stars once for the largest magnitude limit of the sweep.
func (s *Service) stars(ctx context.Context, sw *Sweep) ([]genconst.Star, error) {
	ctx, span := tracer.Start(ctx, "load stars")
	defer span.End()
	magMax := sw.Spec.Params.WithDefaults().MagMax
	stars, err := s.catalog.Stars(ctx, sw.BuildID, magMax)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("%w: read stars: %v", errCatalog, err)
	}
	span.SetAttributes(attribute.Int("stars", len(stars)))
	return stars, nil
}

// one generates, compares and measures a single parameter set.
func (s *Service) one(ctx context.Context, stars []genconst.Star, ref []genconst.Edge, p genconst.Params, seq int) (Run, error) {
	_, span := tracer.Start(ctx, "generate "+p.Strategy, trace.WithAttributes(
		attribute.String("strategy", p.Strategy), attribute.Float64("eps_deg", p.EpsDeg), attribute.Int64("seed", int64(p.Seed))))
	defer span.End()
	start := time.Now()
	res, err := genconst.Generate(stars, p)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return Run{}, fmt.Errorf("generate run %d: %w", seq, err)
	}
	m := compare.Edges(res.Edges, ref, res.Used)
	span.SetAttributes(attribute.Float64("jaccard", m.Jaccard))
	return Run{Seq: seq, Params: p, Stars: len(res.Used), Edges: len(res.Edges), Clusters: res.Clusters,
		Metrics: m, Hash: genconst.Hash(p, res), DurationMS: float64(time.Since(start).Microseconds()) / 1000}, nil
}

func (s *Service) fail(ctx context.Context, sw Sweep, code, detail string) error {
	end := s.now().UTC()
	sw.Status, sw.FinishedAt, sw.ErrorCode, sw.ErrorDetail = StatusFailed, &end, code, detail
	ev, err := events.New(events.ResearchRunFailed, Producer,
		events.ResearchFailedData{SweepID: sw.ID, Code: code, Detail: detail}, events.WithCorrelation(sw.ID))
	if err != nil {
		return err
	}
	if err := s.store.FinishSweep(ctx, sw, ev); err != nil {
		return fmt.Errorf("finish failed sweep %s: %w", sw.ID, err)
	}
	s.notify.Notify()
	return nil
}

// Verification is the result of generating the same thing twice.
type Verification struct {
	Params genconst.Params `json:"params"`
	Hash1  string          `json:"hash1"`
	Hash2  string          `json:"hash2"`
	Equal  bool            `json:"equal"`
	Edges  int             `json:"edges"`
}

// Verify checks determinism: the first parameter set of the spec is generated
// twice from independently loaded stars; the hashes must be equal.
func (s *Service) Verify(ctx context.Context, spec Spec) (Verification, error) {
	spec, runs, err := spec.Normalize()
	if err != nil {
		return Verification{}, err
	}
	build := spec.BuildID
	if build == "" {
		if build, err = s.catalog.ActiveBuild(ctx); err != nil {
			return Verification{}, fmt.Errorf("%w: %v", errCatalog, err)
		}
	}
	p := runs[0]
	var hashes [2]string
	edges := 0
	for i := range hashes {
		stars, err := s.catalog.Stars(ctx, build, p.MagMax)
		if err != nil {
			return Verification{}, fmt.Errorf("%w: read stars: %v", errCatalog, err)
		}
		res, err := genconst.Generate(stars, p)
		if err != nil {
			return Verification{}, err
		}
		hashes[i], edges = genconst.Hash(p, res), len(res.Edges)
	}
	return Verification{Params: p, Hash1: hashes[0], Hash2: hashes[1], Equal: hashes[0] == hashes[1], Edges: edges}, nil
}

// IsCatalogError reports failures of reaching the catalog.
func IsCatalogError(err error) bool { return errors.Is(err, errCatalog) }
