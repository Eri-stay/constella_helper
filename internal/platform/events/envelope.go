// Package events defines the envelope every integration event travels in.
//
// The event type doubles as the RabbitMQ routing key (for example
// "release.published"), so a consumer binds to the same names it switches on.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SchemaVersion is the envelope format version written by this code.
const SchemaVersion = 1

// ErrInvalidEnvelope is returned when an envelope misses a required field.
var ErrInvalidEnvelope = errors.New("invalid event envelope")

// Envelope is the transport form of an integration event.
type Envelope struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Producer      string          `json:"producer"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	SchemaVer     int             `json:"schema_ver"`
	Data          json.RawMessage `json:"data"`
}

// Option changes an envelope while it is being built by New.
type Option func(*Envelope)

// WithCorrelation sets the id that groups all events of one business process
// (for example a release id).
func WithCorrelation(id string) Option {
	return func(e *Envelope) { e.CorrelationID = id }
}

// CausedBy marks the envelope as a reaction to another event and inherits its
// correlation id when none was set.
func CausedBy(parent Envelope) Option {
	return func(e *Envelope) {
		e.CausationID = parent.ID
		if e.CorrelationID == "" {
			e.CorrelationID = parent.CorrelationID
		}
	}
}

// WithClock replaces the time source; used by tests.
func WithClock(now func() time.Time) Option {
	return func(e *Envelope) { e.OccurredAt = now().UTC() }
}

// New builds an envelope with a fresh random id and data marshalled to JSON.
func New(eventType, producer string, data any, opts ...Option) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal %s data: %w", eventType, err)
	}
	env := Envelope{
		ID:         NewID(),
		Type:       eventType,
		OccurredAt: time.Now().UTC(),
		Producer:   producer,
		SchemaVer:  SchemaVersion,
		Data:       raw,
	}
	for _, opt := range opts {
		opt(&env)
	}
	if err := env.Validate(); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// Validate checks the fields every consumer relies on.
func (e Envelope) Validate() error {
	switch {
	case e.ID == "":
		return fmt.Errorf("%w: empty id", ErrInvalidEnvelope)
	case e.Type == "":
		return fmt.Errorf("%w: empty type", ErrInvalidEnvelope)
	case e.Producer == "":
		return fmt.Errorf("%w: empty producer", ErrInvalidEnvelope)
	}
	return nil
}

// Decode unmarshals the event data into v.
func (e Envelope) Decode(v any) error {
	if err := json.Unmarshal(e.Data, v); err != nil {
		return fmt.Errorf("decode %s data: %w", e.Type, err)
	}
	return nil
}

// NewID returns a random 128-bit id in hex (UUID-sized, no dependency).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; a failure means the
		// process cannot produce safe ids at all.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b[:])
}
