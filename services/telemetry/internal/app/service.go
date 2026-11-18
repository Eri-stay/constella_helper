// Package app holds the telemetry use cases: the audit trail of platform
// events and the intake of client logs.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"constella/internal/domain/telemetry"
	"constella/internal/platform/events"
)

// Via says how an audit entry arrived.
const (
	ViaSync  = "sync"  // POST /v1/audit, a mandatory step of a classic release
	ViaEvent = "event" // consumed from the broker
)

// ErrInvalid is returned for input that misses required fields.
var ErrInvalid = errors.New("invalid input")

// AuditEntry is one line of the audit trail.
type AuditEntry struct {
	ID            int64           `json:"id"`
	At            time.Time       `json:"at"`
	Source        string          `json:"source"`
	Type          string          `json:"type"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Via           string          `json:"via"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// ClientLog is a diagnostic entry sent by an installed client.
type ClientLog struct {
	ID           int64     `json:"id"`
	At           time.Time `json:"at"`
	Installation string    `json:"installation,omitempty"`
	AppVersion   string    `json:"app_version,omitempty"`
	Level        string    `json:"level"`
	Message      string    `json:"message"`
	Stack        string    `json:"stack,omitempty"`
}

// Store persists audit entries and client logs.
type Store interface {
	// AddAudit stores e; added=false when dedupeKey was already used.
	AddAudit(ctx context.Context, e AuditEntry, dedupeKey string) (id int64, added bool, err error)
	// AddEventAudit stores e once per (consumer, event id); duplicate=true when seen.
	AddEventAudit(ctx context.Context, eventID string, e AuditEntry) (duplicate bool, err error)
	Audit(ctx context.Context, limit int, typePrefix string) ([]AuditEntry, error)
	AddClientLog(ctx context.Context, l ClientLog) (ClientLog, error)
	ClientLogs(ctx context.Context, limit int) ([]ClientLog, error)
	Counts(ctx context.Context) (audit, clientLogs int, err error)
}

// ErrorSink forwards errors and crashes to an external service. The default is
// a no-op; Sentry is enabled only by SENTRY_DSN.
type ErrorSink interface {
	Name() string
	Capture(ctx context.Context, l ClientLog) error
}

// Service is the telemetry use cases.
type Service struct {
	store Store
	sink  ErrorSink
	log   *slog.Logger
	now   func() time.Time
}

// New wires the service.
func New(store Store, sink ErrorSink, log *slog.Logger) *Service {
	return &Service{store: store, sink: sink, log: log, now: time.Now}
}

// AuditInput is a request to record an audit entry.
type AuditInput struct {
	Source, Type, CorrelationID string
	Data                        json.RawMessage
}

// RecordAudit stores an entry. A repeated idempotency key is accepted and
// stores nothing, so callers can retry safely. It returns the entry id.
func (s *Service) RecordAudit(ctx context.Context, in AuditInput, key string) (id int64, added bool, err error) {
	if in.Source == "" || in.Type == "" {
		return 0, false, fmt.Errorf("%w: source and type are required", ErrInvalid)
	}
	return s.store.AddAudit(ctx, AuditEntry{
		At: s.now().UTC(), Source: in.Source, Type: in.Type, CorrelationID: in.CorrelationID,
		Via: ViaSync, Data: in.Data,
	}, key)
}

// HandleEvent audits any broker event (the telemetry queue binds to "#").
func (s *Service) HandleEvent(ctx context.Context, env events.Envelope) error {
	dup, err := s.store.AddEventAudit(ctx, env.ID, AuditEntry{
		At: env.OccurredAt, Source: env.Producer, Type: env.Type, CorrelationID: env.CorrelationID,
		Via: ViaEvent, Data: env.Data,
	})
	if err != nil {
		return fmt.Errorf("audit event %s: %w", env.ID, err)
	}
	if dup {
		s.log.InfoContext(ctx, "duplicate event skipped", "event_id", env.ID)
	}
	return nil
}

// ClientLogInput is what a client sends.
type ClientLogInput struct {
	Installation, AppVersion, Level, Message, Stack string
}

// AddClientLog scrubs the entry of user names in paths, stores it, and forwards
// errors and crashes to the sink. A failing sink never fails the request:
// the entry is already stored locally.
func (s *Service) AddClientLog(ctx context.Context, in ClientLogInput) (ClientLog, error) {
	if !telemetry.ValidLevel(in.Level) || in.Message == "" {
		return ClientLog{}, fmt.Errorf("%w: level and message are required", ErrInvalid)
	}
	l, err := s.store.AddClientLog(ctx, ClientLog{
		At: s.now().UTC(), Level: in.Level,
		Installation: telemetry.Scrub(in.Installation), AppVersion: in.AppVersion,
		Message: telemetry.Scrub(in.Message), Stack: telemetry.Scrub(in.Stack),
	})
	if err != nil {
		return ClientLog{}, err
	}
	if telemetry.Reportable(l.Level) {
		if err := s.sink.Capture(ctx, l); err != nil {
			s.log.WarnContext(ctx, "error sink failed", "sink", s.sink.Name(), "err", err)
		}
	}
	return l, nil
}

// Audit lists entries, newest first.
func (s *Service) Audit(ctx context.Context, limit int, typePrefix string) ([]AuditEntry, error) {
	return s.store.Audit(ctx, limit, typePrefix)
}

// ClientLogs lists client logs, newest first.
func (s *Service) ClientLogs(ctx context.Context, limit int) ([]ClientLog, error) {
	return s.store.ClientLogs(ctx, limit)
}

// Summary is what the gateway overview shows.
type Summary struct {
	Audit      int    `json:"audit"`
	ClientLogs int    `json:"client_logs"`
	Sink       string `json:"sink"`
}

// Summary counts stored rows.
func (s *Service) Summary(ctx context.Context) (Summary, error) {
	a, c, err := s.store.Counts(ctx)
	return Summary{Audit: a, ClientLogs: c, Sink: s.sink.Name()}, err
}
