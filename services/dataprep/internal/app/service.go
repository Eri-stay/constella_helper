// Package app holds the dataprep use cases: starting a build, running it in
// the background, and recovering builds that a restart interrupted.
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

	"constella/internal/domain/catalog"
	"constella/internal/platform/events"
)

// Producer is the name dataprep puts on its events.
const Producer = "dataprep"

// Build status values.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Failure codes stored with a failed build and sent in BuildFailed.
const (
	CodeSourceUnavailable = "SOURCE_UNAVAILABLE"
	CodeWriteFailed       = "WRITE_FAILED"
	CodeInterrupted       = "INTERRUPTED"
	CodeCancelled         = "CANCELLED"
)

var (
	// ErrBusy is returned by Start while another build is running.
	ErrBusy = errors.New("a build is already running")
	// ErrNotFound is returned for an unknown build id.
	ErrNotFound = errors.New("build not found")
)

var tracer = otel.Tracer("constella/dataprep")

// Build is one run of the pipeline.
type Build struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	Artifact       string `json:"artifact,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	Stars          int    `json:"stars,omitempty"`
	Constellations int    `json:"constellations,omitempty"`
	Cultures       int    `json:"cultures,omitempty"`
	Edges          int    `json:"edges,omitempty"`
	DroppedEdges   int    `json:"dropped_edges,omitempty"`

	ErrorCode   string `json:"error_code,omitempty"`
	ErrorDetail string `json:"error_detail,omitempty"`
}

// Input is everything the pipeline reads before it builds the dataset.
type Input struct {
	Stars    []catalog.Star
	Cultures []catalog.Culture
	NamesUK  map[int64]string
}

// Artifact describes a written cosmos.db.
type Artifact struct {
	File      string // file name inside the artifacts directory
	SHA256    string
	SizeBytes int64
}

// Store persists builds. Create and Finish also store the event in the outbox
// in the same transaction.
type Store interface {
	// Create stores a new running build and its BuildStarted event; it
	// returns ErrBusy if a build is already running.
	Create(ctx context.Context, b Build, ev events.Envelope) error
	// Finish stores the final state of a build and its closing event.
	Finish(ctx context.Context, b Build, ev events.Envelope) error
	Get(ctx context.Context, id string) (Build, error)
	List(ctx context.Context, limit int) ([]Build, error)
	Running(ctx context.Context) ([]Build, error)
}

// Sources loads the pipeline input (HYG, cultures, curated names).
type Sources interface {
	Load(ctx context.Context) (Input, error)
}

// Writer writes the dataset as an artifact file named name.
type Writer interface {
	Write(ctx context.Context, name string, ds catalog.Dataset) (Artifact, error)
}

// Notifier wakes the outbox relay after a commit.
type Notifier interface{ Notify() }

// Service runs builds.
type Service struct {
	store   Store
	sources Sources
	writer  Writer
	notify  Notifier
	log     *slog.Logger
	now     func() time.Time
	wg      sync.WaitGroup
}

// New wires the service.
func New(store Store, sources Sources, writer Writer, notify Notifier, log *slog.Logger) *Service {
	return &Service{store: store, sources: sources, writer: writer, notify: notify, log: log, now: time.Now}
}

// Start registers a build and runs it in the background. It returns at once
// (the HTTP layer answers 202); progress arrives as events.
func (s *Service) Start(ctx context.Context) (Build, error) {
	started := s.now().UTC()
	b := Build{ID: newBuildID(started), Status: StatusRunning, StartedAt: started}
	ev, err := events.New(events.BuildStarted, Producer, events.BuildStartedData{BuildID: b.ID},
		events.WithCorrelation(b.ID))
	if err != nil {
		return Build{}, err
	}
	if err := s.store.Create(ctx, b, ev); err != nil {
		return Build{}, fmt.Errorf("create build: %w", err)
	}
	s.notify.Notify()

	// The build outlives the request but stays in its trace.
	bg := context.WithoutCancel(ctx)
	s.wg.Go(func() { s.run(bg, b) })
	return b, nil
}

// Get returns one build.
func (s *Service) Get(ctx context.Context, id string) (Build, error) { return s.store.Get(ctx, id) }

// List returns the newest builds first.
func (s *Service) List(ctx context.Context, limit int) ([]Build, error) {
	return s.store.List(ctx, limit)
}

// Wait blocks until running builds end or ctx is done (graceful shutdown).
func (s *Service) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Recover fails builds left running by a crashed process, so they do not block
// new builds forever.
func (s *Service) Recover(ctx context.Context) error {
	running, err := s.store.Running(ctx)
	if err != nil {
		return fmt.Errorf("list running builds: %w", err)
	}
	for _, b := range running {
		s.log.WarnContext(ctx, "failing interrupted build", "build_id", b.ID)
		if err := s.fail(ctx, b, CodeInterrupted, "process stopped while the build was running"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) run(ctx context.Context, b Build) {
	ctx, span := tracer.Start(ctx, "dataprep build",
		trace.WithAttributes(attribute.String("build.id", b.ID)))
	defer span.End()

	art, ds, code, err := s.pipeline(ctx, b.ID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		s.log.ErrorContext(ctx, "build failed", "build_id", b.ID, "code", code, "err", err)
		if fErr := s.fail(ctx, b, code, err.Error()); fErr != nil {
			s.log.ErrorContext(ctx, "could not record build failure", "build_id", b.ID, "err", fErr)
		}
		return
	}

	end := s.now().UTC()
	b.Status, b.FinishedAt = StatusCompleted, &end
	b.Artifact, b.SHA256, b.SizeBytes = art.File, art.SHA256, art.SizeBytes
	b.Stars, b.Constellations, b.Cultures = len(ds.Stars), len(ds.Constellations), len(ds.Cultures)
	b.Edges, b.DroppedEdges = len(ds.Edges), ds.DroppedEdges

	ev, err := events.New(events.BuildCompleted, Producer, events.BuildCompletedData{
		BuildID: b.ID, Artifact: b.Artifact, SHA256: b.SHA256, SizeBytes: b.SizeBytes,
		Stars: b.Stars, Constellations: b.Constellations, Cultures: b.Cultures,
		Edges: b.Edges, DroppedEdges: b.DroppedEdges,
	}, events.WithCorrelation(b.ID))
	if err == nil {
		err = s.store.Finish(ctx, b, ev)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		s.log.ErrorContext(ctx, "could not record completed build", "build_id", b.ID, "err", err)
		return
	}
	s.notify.Notify()
	s.log.InfoContext(ctx, "build completed", "build_id", b.ID, "stars", b.Stars, "edges", b.Edges,
		"dropped_edges", b.DroppedEdges, "sha256", b.SHA256)
}

// pipeline loads, assembles and writes. On error it returns the failure code.
func (s *Service) pipeline(ctx context.Context, id string) (Artifact, catalog.Dataset, string, error) {
	in, err := phase(ctx, "load sources", func(ctx context.Context) (Input, error) { return s.sources.Load(ctx) })
	if err != nil {
		return Artifact{}, catalog.Dataset{}, failCode(err, CodeSourceUnavailable), err
	}
	ds, _ := phase(ctx, "assemble dataset", func(context.Context) (catalog.Dataset, error) {
		return catalog.NewDataset(in.Stars, in.Cultures, in.NamesUK), nil
	})
	art, err := phase(ctx, "write cosmos.db", func(ctx context.Context) (Artifact, error) {
		return s.writer.Write(ctx, "cosmos-"+id+".db", ds)
	})
	if err != nil {
		return Artifact{}, catalog.Dataset{}, failCode(err, CodeWriteFailed), err
	}
	return art, ds, "", nil
}

func failCode(err error, def string) string {
	if errors.Is(err, context.Canceled) {
		return CodeCancelled
	}
	return def
}

// phase runs fn inside a child span so the Jaeger trace shows where time goes.
func phase[T any](ctx context.Context, name string, fn func(context.Context) (T, error)) (T, error) {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()
	v, err := fn(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return v, err
}

func (s *Service) fail(ctx context.Context, b Build, code, detail string) error {
	end := s.now().UTC()
	b.Status, b.FinishedAt, b.ErrorCode, b.ErrorDetail = StatusFailed, &end, code, detail
	ev, err := events.New(events.BuildFailed, Producer,
		events.BuildFailedData{BuildID: b.ID, Code: code, Detail: detail}, events.WithCorrelation(b.ID))
	if err != nil {
		return err
	}
	if err := s.store.Finish(ctx, b, ev); err != nil {
		return fmt.Errorf("finish failed build %s: %w", b.ID, err)
	}
	s.notify.Notify()
	return nil
}

func newBuildID(t time.Time) string {
	return "b" + t.Format("20060102-150405") + "-" + events.NewID()[:4]
}
