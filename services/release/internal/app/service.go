// Package app holds the release use cases. PublishRelease is an orchestrated
// saga with two execution modes over the same steps:
//
//   - classic: the HTTP request runs every step and answers at the end; the
//     audit call to telemetry is a mandatory last step; a failure rolls back.
//   - eda: the request only stores "requested" and answers 202; a worker runs
//     the steps with retry and backoff, and the outcome travels as events
//     (outbox → broker) to telemetry, the gateway and others.
//
// In both modes a failed step compensates the finished ones.
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
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"constella/internal/domain/release"
	"constella/internal/platform/events"
	"constella/internal/platform/httpx"
)

// Producer is the name release puts on its events.
const Producer = "release"

// Failure codes of the saga (besides the ones services return).
const (
	CodeCatalogDown   = "CATALOG_UNAVAILABLE"
	CodeCatalogRefuse = "CATALOG_REFUSED"
	CodeSwitchFailed  = "SWITCH_FAILED"
	CodeAuditDown     = "AUDIT_UNAVAILABLE"
	CodeCircuitOpen   = "CIRCUIT_OPEN"
)

// Errors of the use cases.
var (
	// ErrNotFound is returned for an unknown release.
	ErrNotFound = errors.New("release not found")
	// ErrModeUnsupported is returned for a mode that does not exist.
	ErrModeUnsupported = errors.New("release mode is not supported")
)

var tracer = otel.Tracer("constella/release")

// StepError is a failed saga step: which one, and a code for the UI. Publish
// returns it after the release was stored as failed.
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
// example the build is not validated). Code is its reason. Retrying is useless.
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
	// Append stores events and, in the same transaction, the integration
	// events for the outbox; revision is the number of events the stream had
	// when it was loaded (optimistic concurrency).
	Append(ctx context.Context, releaseID string, revision int, evs []release.Event, out []events.Envelope) error
	Load(ctx context.Context, releaseID string) ([]release.Event, error)
	// Recent returns the newest release ids.
	Recent(ctx context.Context, limit int) ([]string, error)
	// Open returns the ids of event-mode releases that are neither published nor failed.
	Open(ctx context.Context) ([]string, error)
}

// Service runs releases.
type Service struct {
	store     Store
	catalog   Catalog
	publisher Publisher
	audit     Audit
	log       *slog.Logger
	now       func() time.Time

	attempts int           // per step in event mode
	backoff  time.Duration // first wait between attempts, doubled each time
	wake     chan struct{}
	notify   interface{ Notify() }
}

// New wires the service. Event mode retries each step 6 times (0.5 s, 1 s, 2 s,
// …), enough for the circuit breaker (10 s) to half-open again.
func New(st Store, c Catalog, p Publisher, a Audit, log *slog.Logger) *Service {
	return &Service{store: st, catalog: c, publisher: p, audit: a, log: log, now: time.Now,
		attempts: 6, backoff: 500 * time.Millisecond, wake: make(chan struct{}, 1), notify: noNotify{}}
}

type noNotify struct{}

func (noNotify) Notify() {}

// WithRelay sets the outbox relay that is woken after a commit that added events.
func (s *Service) WithRelay(n interface{ Notify() }) *Service { s.notify = n; return s }

// WithRetry changes the event-mode retry policy (used by tests).
func (s *Service) WithRetry(attempts int, backoff time.Duration) *Service {
	s.attempts, s.backoff = attempts, backoff
	return s
}

func newReleaseID(now time.Time) string {
	return "r" + now.UTC().Format("20060102-150405") + "-" + events.NewID()[:4]
}

// PublishClassic runs the synchronous saga: reserve → switch → mark released →
// audit. On a failed step the finished steps are undone, the release is
// stored as failed, and a *StepError is returned together with the release.
func (s *Service) PublishClassic(ctx context.Context, buildID string) (*release.Release, error) {
	r, first, err := release.Request(newReleaseID(s.now()), buildID, release.ModeClassic, s.now())
	if err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "release publish", s.attrs(r))
	defer span.End()
	first.Traceparent = traceparent(ctx)
	r.TraceParent = first.Traceparent
	g := &saga{s: s, r: r, pending: []release.Event{first}}
	if err := g.run(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return r, err
	}
	return r, nil
}

// RequestEDA stores the request and its release.requested event, wakes the
// worker and returns at once; the saga runs in the background.
func (s *Service) RequestEDA(ctx context.Context, buildID string) (*release.Release, error) {
	r, first, err := release.Request(newReleaseID(s.now()), buildID, release.ModeEDA, s.now())
	if err != nil {
		return nil, err
	}
	ctx, span := tracer.Start(ctx, "release request", s.attrs(r))
	defer span.End()
	first.Traceparent = traceparent(ctx)
	r.TraceParent = first.Traceparent
	g := &saga{s: s, r: r, pending: []release.Event{first}}
	if err := g.flush(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	s.Wake()
	return r, nil
}

// traceparent returns the W3C trace context of ctx ("" when untraced).
func traceparent(ctx context.Context) string {
	c := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, c)
	return c.Get("traceparent")
}

func (s *Service) attrs(r *release.Release) trace.SpanStartEventOption {
	return trace.WithAttributes(attribute.String("release.id", r.ID), attribute.String("build.id", r.BuildID),
		attribute.String("mode", r.Mode))
}

// Wake asks the worker to look for work now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default: // a pass is already requested
	}
}

// RunWorker runs event-mode releases until ctx is cancelled. It also resumes
// releases that a restart interrupted: their state is in the event store.
func (s *Service) RunWorker(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		s.resumeAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Service) resumeAll(ctx context.Context) {
	ids, err := s.store.Open(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "list open releases failed", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := s.Advance(ctx, id); err != nil {
			var se *StepError
			if !errors.As(err, &se) { // a failed saga is already stored and logged
				s.log.ErrorContext(ctx, "advance release failed", "release_id", id, "err", err)
			}
		}
	}
}

// Advance runs the remaining steps of one release from its stored state.
func (s *Service) Advance(ctx context.Context, id string) error {
	r, evs, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == release.Published || r.Status == release.Failed {
		return nil
	}
	// The worker runs later and elsewhere, but its spans belong to the trace of
	// the request that created the release.
	if r.TraceParent != "" {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": r.TraceParent})
	}
	ctx, span := tracer.Start(ctx, "release saga", s.attrs(r))
	defer span.End()
	g := &saga{s: s, r: r, revision: len(evs)}
	for _, e := range evs {
		if e.Type == release.EvSwitched && e.Previous != "" {
			g.previous = []byte(e.Previous)
		}
	}
	if err := g.run(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

// saga holds the state of one run.
type saga struct {
	s        *Service
	r        *release.Release
	pending  []release.Event // events not stored yet
	revision int             // events already stored
	previous []byte          // manifest before the switch
}

// errAborted means the process is shutting down: the release is left as it is
// and the next start resumes it.
var errAborted = errors.New("saga aborted by shutdown")

func (g *saga) eda() bool { return g.r.Mode == release.ModeEDA }

func (g *saga) flush(ctx context.Context) error {
	if len(g.pending) == 0 {
		return nil
	}
	out, err := g.envelopes()
	if err != nil {
		return err
	}
	if err := g.s.store.Append(ctx, g.r.ID, g.revision, g.pending, out); err != nil {
		return fmt.Errorf("store release events: %w", err)
	}
	g.revision += len(g.pending)
	g.pending = nil
	if len(out) > 0 {
		g.s.notify.Notify()
	}
	return nil
}

// envelopes turns the pending domain events into integration events. Only the
// event mode publishes them; classic mode audits synchronously instead.
func (g *saga) envelopes() ([]events.Envelope, error) {
	if !g.eda() {
		return nil, nil
	}
	var out []events.Envelope
	for _, e := range g.pending {
		data := events.ReleaseData{ReleaseID: g.r.ID, BuildID: g.r.BuildID, Mode: g.r.Mode}
		var typ string
		switch e.Type {
		case release.EvRequested:
			typ = events.ReleaseRequested
		case release.EvPublished:
			typ = events.ReleasePublished
			data.Version, data.SHA256 = g.r.Version, g.r.SHA256
		case release.EvFailed:
			typ = events.ReleaseFailed
			data.Step, data.Code, data.Compensated = e.Step, e.Code, e.Compensated
		default:
			continue
		}
		env, err := events.New(typ, Producer, data, events.WithCorrelation(g.r.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}

func (g *saga) record(ev release.Event, err error) error {
	if err != nil {
		return err
	}
	g.pending = append(g.pending, ev)
	return nil
}

// run executes the steps the release has not done yet, so it serves both a
// fresh classic release and an event-mode release resumed after a restart.
func (g *saga) run(ctx context.Context) error {
	var failed string
	var cause error
	do := func(name string, run func(context.Context) error) bool {
		if err := g.attempt(ctx, name, run); err != nil {
			failed, cause = name, err
			return false
		}
		return true
	}
	ok := true
	if g.r.Status == release.Requested {
		ok = do(release.StepReserve, g.reserve)
	}
	if ok && g.r.Status == release.Reserved {
		ok = do(release.StepSwitch, g.switchManifest)
	}
	if ok {
		ok = do(release.StepMarkReleased, g.markReleased)
	}
	if ok && !g.eda() {
		ok = do(release.StepAudit, g.auditStep)
	}
	if !ok {
		if errors.Is(cause, errAborted) {
			return cause
		}
		return g.fail(ctx, failed, cause)
	}
	if err := g.record(g.r.Publish(g.s.now())); err != nil {
		return err
	}
	if err := g.flush(ctx); err != nil {
		return err
	}
	g.s.log.InfoContext(ctx, "release published", "release_id", g.r.ID, "mode", g.r.Mode, "build_id", g.r.BuildID)
	return nil
}

// attempt runs a step. In event mode a transient failure is retried with
// exponential backoff; a refusal or a local failure is not.
func (g *saga) attempt(ctx context.Context, name string, run func(context.Context) error) error {
	tries := 1
	if g.eda() {
		tries = g.s.attempts
	}
	delay := g.s.backoff
	for i := 1; ; i++ {
		err := g.step(ctx, name, i, run)
		if err == nil {
			return nil
		}
		if g.eda() && ctx.Err() != nil {
			return errAborted
		}
		if i >= tries || !retryable(err) {
			return err
		}
		g.s.log.WarnContext(ctx, "release step failed; retrying", "release_id", g.r.ID, "step", name,
			"attempt", i, "in", delay, "err", err)
		select {
		case <-ctx.Done():
			return errAborted
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func (g *saga) step(ctx context.Context, name string, attempt int, run func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, "saga "+name, trace.WithAttributes(attribute.Int("attempt", attempt)))
	defer span.End()
	if err := run(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

// retryable: only failures of reaching another service can go away by waiting.
func retryable(err error) bool {
	var ref *RefusedError
	var ce *codeError
	switch {
	case errors.As(err, &ref):
		return false
	case errors.As(err, &ce):
		return ce.code != CodeSwitchFailed
	case errors.Is(err, release.ErrTransition):
		return false
	}
	return true
}

func (g *saga) reserve(ctx context.Context) error {
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
	g.previous = prev
	if err := g.record(g.r.Switch(g.r.BuildID, string(prev), g.s.now())); err != nil {
		return err
	}
	return g.flush(ctx) // the previous manifest is stored: rollback survives a restart
}

func (g *saga) markReleased(ctx context.Context) error {
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
	if errors.Is(err, httpx.ErrCircuitOpen) {
		return CodeCircuitOpen
	}
	return CodeCatalogDown
}

// fail undoes the finished steps in reverse order and stores the failure.
func (g *saga) fail(ctx context.Context, step string, cause error) error {
	// The request may be gone (client left or timed out); compensation must still run.
	ctx = context.WithoutCancel(ctx)
	compensated := true
	ctx, span := tracer.Start(ctx, "saga compensate", trace.WithAttributes(attribute.String("failed.step", step)))
	defer span.End()
	if g.r.Status == release.Switched {
		if err := g.compensate(ctx, "restore manifest", func(ctx context.Context) error {
			return g.s.publisher.Restore(ctx, g.previous)
		}); err != nil {
			compensated = false
			g.s.log.ErrorContext(ctx, "compensation failed: restore manifest", "release_id", g.r.ID, "err", err)
		}
	}
	if g.r.Status != release.Requested || step == release.StepReserve {
		// Cancel is idempotent; it also frees a reservation whose answer was lost.
		if err := g.compensate(ctx, "cancel reservation", func(ctx context.Context) error {
			return g.s.catalog.Cancel(ctx, g.r.BuildID, g.r.ID)
		}); err != nil {
			compensated = false
			g.s.log.ErrorContext(ctx, "compensation failed: cancel reservation", "release_id", g.r.ID, "err", err)
		}
	}
	code := codeOf(cause)
	ev, err := g.r.Fail(step, code, compensated, g.s.now())
	if err != nil {
		return err
	}
	ev.Detail = cause.Error()
	g.r.Failure.Detail = ev.Detail
	g.pending = append(g.pending, ev)
	if err := g.flush(ctx); err != nil {
		return err
	}
	g.s.log.WarnContext(ctx, "release failed", "release_id", g.r.ID, "step", step, "code", code, "compensated", compensated)
	return &StepError{Step: step, Code: code, Compensated: compensated, Err: cause}
}

// compensate runs one undo action in its own span, so a rollback is visible in the trace.
func (g *saga) compensate(ctx context.Context, name string, run func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, "compensate "+name)
	defer span.End()
	if err := run(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
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
