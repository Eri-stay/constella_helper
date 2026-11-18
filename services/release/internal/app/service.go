// Package app holds the release use cases. PublishRelease in classic mode is
// an orchestrated saga: every step is a synchronous call, and when a later
// step fails the earlier ones are compensated.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"constella/internal/domain/release"
	"constella/internal/platform/events"
)

// Failure codes of the saga (besides the ones services return).
const (
	CodeCatalogDown   = "CATALOG_UNAVAILABLE"
	CodeCatalogRefuse = "CATALOG_REFUSED"
	CodeSwitchFailed  = "SWITCH_FAILED"
	CodeAuditDown     = "AUDIT_UNAVAILABLE"
)

// Errors of the use cases.
var (
	// ErrNotFound is returned for an unknown release.
	ErrNotFound = errors.New("release not found")
	// ErrModeUnsupported is returned for a mode that is not implemented yet.
	ErrModeUnsupported = errors.New("release mode is not supported")
)

var tracer = otel.Tracer("constella/release")

// StepError is a failed saga step: which one, and a code for the UI. It is
// returned by Publish after the release was stored as failed.
type StepError struct {
	Step, Code  string
	Compensated bool
	Err         error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("release step %s failed (%s): %v", e.Step, e.Code, e.Err)
}

// Unwrap returns the cause.
func (e *StepError) Unwrap() error { return e.Err }

// Reserved is what the catalog hands out when it reserves a build.
type Reserved struct {
	Artifact  string
	SHA256    string
	SizeBytes int64
}

// RefusedError is returned by Catalog when it answers but says no (for
// example the build is not validated). Code is its reason.
type RefusedError struct{ Code string }

func (e *RefusedError) Error() string { return "catalog refused: " + e.Code }

// Catalog is the reservation protocol of the catalog service.
type Catalog interface {
	Reserve(ctx context.Context, buildID, releaseID string) (Reserved, error)
	MarkReleased(ctx context.Context, buildID, releaseID string) error
	Cancel(ctx context.Context, buildID, releaseID string) error
}

// Publisher switches the published version.json.
type Publisher interface {
	// Switch copies the artifact (verifying its SHA-256), then atomically
	// points version.json at it. It returns the previous manifest, nil if none.
	Switch(ctx context.Context, releaseID string, a Reserved, at time.Time) (previous []byte, err error)
	// Restore puts the previous manifest back (nil removes version.json).
	Restore(ctx context.Context, previous []byte) error
	// Current returns the published manifest, nil if none.
	Current(ctx context.Context) ([]byte, error)
}

// Audit records that a release happened. In classic mode it is a mandatory step.
type Audit interface {
	Record(ctx context.Context, releaseID, eventType string, data any) error
}

// Store is the event store of the Release aggregate.
type Store interface {
	// Append stores events; revision is the number of events the stream had
	// when it was loaded (optimistic concurrency).
	Append(ctx context.Context, releaseID string, revision int, evs []release.Event) error
	Load(ctx context.Context, releaseID string) ([]release.Event, error)
	// Recent returns the newest release ids.
	Recent(ctx context.Context, limit int) ([]string, error)
}

// Service runs releases.
type Service struct {
	store     Store
	catalog   Catalog
	publisher Publisher
	audit     Audit
	log       *slog.Logger
	now       func() time.Time
}

// New wires the service.
func New(st Store, c Catalog, p Publisher, a Audit, log *slog.Logger) *Service {
	return &Service{store: st, catalog: c, publisher: p, audit: a, log: log, now: time.Now}
}

// PublishClassic runs the synchronous saga: reserve → switch → mark released →
// audit. It returns the release. On a failed step the finished steps are
// undone, the release is stored as failed, and a *StepError is returned with
// the stored release.
func (s *Service) PublishClassic(ctx context.Context, buildID string) (*release.Release, error) {
	id := "r" + s.now().UTC().Format("20060102-150405") + "-" + events.NewID()[:4]
	ctx, span := tracer.Start(ctx, "release publish",
		trace.WithAttributes(attribute.String("release.id", id), attribute.String("build.id", buildID),
			attribute.String("mode", release.ModeClassic)))
	defer span.End()

	r, first, err := release.Request(id, buildID, release.ModeClassic, s.now())
	if err != nil {
		return nil, err
	}
	run := &saga{s: s, r: r, pending: []release.Event{first}}
	if err := run.execute(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return r, err
	}
	return r, nil
}

// saga holds the state of one run.
type saga struct {
	s        *Service
	r        *release.Release
	pending  []release.Event // events not stored yet
	revision int             // events already stored
	previous []byte          // manifest before the switch
	switched bool
	reserved bool
	released bool
}

func (g *saga) flush(ctx context.Context) error {
	if len(g.pending) == 0 {
		return nil
	}
	if err := g.s.store.Append(ctx, g.r.ID, g.revision, g.pending); err != nil {
		return fmt.Errorf("store release events: %w", err)
	}
	g.revision += len(g.pending)
	g.pending = nil
	return nil
}

func (g *saga) record(ev release.Event, err error) error {
	if err != nil {
		return err
	}
	g.pending = append(g.pending, ev)
	return nil
}

func (g *saga) execute(ctx context.Context) error {
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{release.StepReserve, g.reserve},
		{release.StepSwitch, g.switchManifest},
		{release.StepMarkReleased, g.markReleased},
		{release.StepAudit, g.auditStep},
	}
	for _, st := range steps {
		if err := g.step(ctx, st.name, st.run); err != nil {
			return g.fail(ctx, st.name, err)
		}
	}
	if err := g.record(g.r.Publish(g.s.now())); err != nil {
		return err
	}
	return g.flush(ctx)
}

func (g *saga) step(ctx context.Context, name string, run func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, "saga "+name)
	defer span.End()
	if err := run(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func (g *saga) reserve(ctx context.Context) error {
	g.reserved = true // set first: a timed-out call may still have reserved; Cancel is idempotent
	art, err := g.s.catalog.Reserve(ctx, g.r.BuildID, g.r.ID)
	if err != nil {
		return err
	}
	if err := g.record(g.r.Reserve(art.Artifact, art.SHA256, art.SizeBytes, g.s.now())); err != nil {
		return err
	}
	return g.flush(ctx)
}

func (g *saga) switchManifest(ctx context.Context) error {
	prev, err := g.s.publisher.Switch(ctx, g.r.ID,
		Reserved{Artifact: g.r.Artifact, SHA256: g.r.SHA256, SizeBytes: g.r.SizeBytes}, g.s.now())
	if err != nil {
		return &codeError{CodeSwitchFailed, err}
	}
	g.previous, g.switched = prev, true
	return g.record(g.r.Switch(g.r.BuildID, g.s.now()))
}

func (g *saga) markReleased(ctx context.Context) error {
	g.released = true // set first, for the same reason as in reserve
	return g.s.catalog.MarkReleased(ctx, g.r.BuildID, g.r.ID)
}

func (g *saga) auditStep(ctx context.Context) error {
	err := g.s.audit.Record(ctx, g.r.ID, "release.published", map[string]any{
		"release_id": g.r.ID, "build_id": g.r.BuildID, "mode": g.r.Mode, "sha256": g.r.SHA256,
	})
	if err != nil {
		return &codeError{CodeAuditDown, err}
	}
	return nil
}

// codeError attaches a saga failure code to an error.
type codeError struct {
	code string
	err  error
}

func (e *codeError) Error() string { return e.err.Error() }
func (e *codeError) Unwrap() error { return e.err }

// codeOf maps a step error to a code the UI can translate.
func codeOf(err error) string {
	var ce *codeError
	if errors.As(err, &ce) {
		return ce.code
	}
	var ref *RefusedError
	if errors.As(err, &ref) {
		return ref.Code
	}
	return CodeCatalogDown
}

// fail undoes the finished steps in reverse order and stores the failure.
func (g *saga) fail(ctx context.Context, step string, cause error) error {
	// The request may be gone (client left or timed out); compensation must still run.
	ctx = context.WithoutCancel(ctx)
	compensated := true
	if g.switched {
		if err := g.s.publisher.Restore(ctx, g.previous); err != nil {
			compensated = false
			g.s.log.ErrorContext(ctx, "compensation failed: restore manifest", "release_id", g.r.ID, "err", err)
		}
	}
	if g.reserved || g.released {
		if err := g.s.catalog.Cancel(ctx, g.r.BuildID, g.r.ID); err != nil {
			compensated = false
			g.s.log.ErrorContext(ctx, "compensation failed: cancel reservation", "release_id", g.r.ID, "err", err)
		}
	}
	code := codeOf(cause)
	if err := g.record(g.r.Fail(step, code, compensated, g.s.now())); err != nil {
		return err
	}
	if err := g.flush(ctx); err != nil {
		return err
	}
	g.s.log.WarnContext(ctx, "release failed", "release_id", g.r.ID, "step", step, "code", code, "compensated", compensated)
	return &StepError{Step: step, Code: code, Compensated: compensated, Err: cause}
}

// Get replays one release from its events.
func (s *Service) Get(ctx context.Context, id string) (*release.Release, []release.Event, error) {
	evs, err := s.store.Load(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(evs) == 0 {
		return nil, nil, ErrNotFound
	}
	r, err := release.Replay(id, evs)
	return r, evs, err
}

// List returns the newest releases.
func (s *Service) List(ctx context.Context, limit int) ([]*release.Release, error) {
	ids, err := s.store.Recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*release.Release, 0, len(ids))
	for _, id := range ids {
		r, _, err := s.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("replay %s: %w", id, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Manifest returns the published version.json, nil if nothing is published.
func (s *Service) Manifest(ctx context.Context) ([]byte, error) { return s.publisher.Current(ctx) }
