// Package app holds the catalog use cases: validating a finished build and
// switching the active catalog to a validated one.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"constella/internal/cosmosdb"
	"constella/internal/domain/catalog"
	"constella/internal/platform/events"
)

// Producer is the name catalog puts on its events.
const Producer = "catalog"

// Verdict values of a stored report.
const (
	VerdictValidated = "validated"
	VerdictRejected  = "rejected"
)

// CheckArtifactMissing is reported when the announced artifact cannot be read
// as a cosmos database at all; the build is rejected, not retried.
const CheckArtifactMissing = "ARTIFACT_UNREADABLE"

// ErrNotFound is returned for a build without a report.
var ErrNotFound = errors.New("report not found")

var tracer = otel.Tracer("constella/catalog")

// Record is the stored result of validating one build.
type Record struct {
	BuildID   string          `json:"build_id"`
	Verdict   string          `json:"verdict"`
	CheckedAt time.Time       `json:"checked_at"`
	Artifact  string          `json:"artifact"`
	SHA256    string          `json:"-"`
	SizeBytes int64           `json:"-"`
	Checks    []catalog.Check `json:"checks"`
}

// Inspector measures an artifact file.
type Inspector interface {
	Inspect(ctx context.Context, path, expectedSHA string) (catalog.Snapshot, error)
}

// Repository stores reports. SaveReport also stores the outgoing event in the
// outbox and records the incoming one in the inbox, in one transaction; when
// the verdict is validated it also makes the build the active one.
type Repository interface {
	// SaveReport returns duplicate=true, writing nothing, when inEventID was
	// already processed.
	SaveReport(ctx context.Context, inEventID string, r Record, out events.Envelope) (duplicate bool, err error)
	Report(ctx context.Context, buildID string) (Record, error)
	Reports(ctx context.Context, limit int) ([]Record, error)
	Reservations
	// Active returns the newest validated build, if any.
	Active(ctx context.Context) (Record, bool, error)
}

// Index serves queries from the active cosmos.db.
type Index interface {
	Activate(path string) error
}

// Notifier wakes the outbox relay after a commit.
type Notifier interface{ Notify() }

// Service validates builds.
type Service struct {
	repo         Repository
	inspector    Inspector
	index        Index
	notify       Notifier
	artifactsDir string
	log          *slog.Logger
	now          func() time.Time
}

// New wires the service. artifactsDir is the shared artifacts volume.
func New(repo Repository, ins Inspector, idx Index, n Notifier, artifactsDir string, log *slog.Logger) *Service {
	return &Service{repo: repo, inspector: ins, index: idx, notify: n, artifactsDir: artifactsDir, log: log, now: time.Now}
}

// HandleBuildCompleted validates the artifact announced by dataprep and
// publishes the verdict. It is idempotent: a redelivered event changes nothing.
func (s *Service) HandleBuildCompleted(ctx context.Context, env events.Envelope) error {
	var data events.BuildCompletedData
	if err := env.Decode(&data); err != nil {
		return fmt.Errorf("decode build completed: %w", err)
	}
	ctx, span := tracer.Start(ctx, "catalog validate build",
		trace.WithAttributes(attribute.String("build.id", data.BuildID)))
	defer span.End()

	path := filepath.Join(s.artifactsDir, filepath.Base(data.Artifact)) // never trust a path from an event
	snap, err := s.inspector.Inspect(ctx, path, data.SHA256)
	var report catalog.Report
	if err != nil {
		if !isPermanent(err) {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("inspect %s: %w", path, err) // transient: let the broker retry
		}
		s.log.WarnContext(ctx, "artifact unreadable; rejecting build", "build_id", data.BuildID, "err", err)
		report = catalog.Report{Checks: []catalog.Check{{Code: CheckArtifactMissing, Status: catalog.StatusFail}}}
	} else {
		report = catalog.Validate(snap)
	}

	rec := Record{BuildID: data.BuildID, CheckedAt: s.now().UTC(), Artifact: data.Artifact,
		SHA256: data.SHA256, SizeBytes: data.SizeBytes, Checks: report.Checks}
	var out events.Envelope
	if report.OK() {
		rec.Verdict = VerdictValidated
		out, err = events.New(events.BuildValidated, Producer,
			events.BuildValidatedData{BuildID: data.BuildID, Warnings: report.Warnings()}, events.CausedBy(env))
	} else {
		rec.Verdict = VerdictRejected
		out, err = events.New(events.BuildRejected, Producer,
			events.BuildRejectedData{BuildID: data.BuildID, FailedChecks: report.Failed()}, events.CausedBy(env))
	}
	if err != nil {
		return err
	}
	span.SetAttributes(attribute.String("verdict", rec.Verdict))

	dup, err := s.repo.SaveReport(ctx, env.ID, rec, out)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("save report for %s: %w", data.BuildID, err)
	}
	if dup {
		s.log.InfoContext(ctx, "duplicate event skipped", "event_id", env.ID, "build_id", data.BuildID)
		return nil
	}
	s.notify.Notify()
	s.log.InfoContext(ctx, "build checked", "build_id", data.BuildID, "verdict", rec.Verdict,
		"failed", report.Failed(), "warnings", report.Warnings())

	if rec.Verdict == VerdictValidated {
		if err := s.index.Activate(path); err != nil {
			// The verdict is already stored; Restore picks it up on next start.
			return fmt.Errorf("activate %s: %w", path, err)
		}
	}
	return nil
}

// isPermanent reports errors a retry cannot fix: a missing or foreign file.
func isPermanent(err error) bool {
	return errors.Is(err, cosmosdb.ErrNotCosmos) || errors.Is(err, os.ErrNotExist)
}

// Restore re-opens the active catalog after a restart.
func (s *Service) Restore(ctx context.Context) error {
	rec, ok, err := s.repo.Active(ctx)
	if err != nil {
		return fmt.Errorf("find active build: %w", err)
	}
	if !ok {
		return nil
	}
	path := filepath.Join(s.artifactsDir, filepath.Base(rec.Artifact))
	if err := s.index.Activate(path); err != nil {
		s.log.WarnContext(ctx, "active catalog could not be opened", "build_id", rec.BuildID, "err", err)
		return nil // the service still runs; queries answer "no active catalog"
	}
	s.log.InfoContext(ctx, "active catalog restored", "build_id", rec.BuildID)
	return nil
}

// Report returns the stored report of one build.
func (s *Service) Report(ctx context.Context, buildID string) (Record, error) {
	return s.repo.Report(ctx, buildID)
}

// Reports lists reports, newest first.
func (s *Service) Reports(ctx context.Context, limit int) ([]Record, error) {
	return s.repo.Reports(ctx, limit)
}

// Active returns the report of the active build.
func (s *Service) Active(ctx context.Context) (Record, bool, error) { return s.repo.Active(ctx) }

// Errors of the reservation protocol (the release saga's first step).
var (
	// ErrNotValidated: the build has no validated report.
	ErrNotValidated = errors.New("build is not validated")
	// ErrReserved: another release holds the build.
	ErrReserved = errors.New("build is reserved by another release")
	// ErrReleased: the build was already released.
	ErrReleased = errors.New("build is already released")
	// ErrNotReserved: the release holds no reservation on the build.
	ErrNotReserved = errors.New("build is not reserved by this release")
)

// Reserved describes a reserved artifact.
type Reserved struct {
	BuildID   string `json:"build_id"`
	Artifact  string `json:"artifact"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// Reservations is the storage of reservations. All three are idempotent for
// the same release id, so callers may retry.
type Reservations interface {
	// Reserve gives a validated build to a release.
	Reserve(ctx context.Context, buildID, releaseID string) (Reserved, error)
	// MarkReleased turns the reservation into a final release.
	MarkReleased(ctx context.Context, buildID, releaseID string) error
	// Cancel drops the reservation or the release mark of that release (compensation).
	Cancel(ctx context.Context, buildID, releaseID string) error
}

// Reserve reserves a validated build for a release.
func (s *Service) Reserve(ctx context.Context, buildID, releaseID string) (Reserved, error) {
	return s.repo.Reserve(ctx, buildID, releaseID)
}

// MarkReleased marks the build released.
func (s *Service) MarkReleased(ctx context.Context, buildID, releaseID string) error {
	return s.repo.MarkReleased(ctx, buildID, releaseID)
}

// Cancel undoes Reserve or MarkReleased.
func (s *Service) Cancel(ctx context.Context, buildID, releaseID string) error {
	return s.repo.Cancel(ctx, buildID, releaseID)
}
