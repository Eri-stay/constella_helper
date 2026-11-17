package outbox

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"

	"constella/internal/platform/events"
)

// Publisher sends one envelope to the broker. It must return only after the
// broker confirmed the message, otherwise the outbox guarantee is lost.
type Publisher interface {
	Publish(ctx context.Context, env events.Envelope) error
}

// Relay moves committed outbox rows to the broker.
type Relay struct {
	db       *sql.DB
	pub      Publisher
	log      *slog.Logger
	interval time.Duration
	batch    int
	wake     chan struct{}
}

// NewRelay creates a relay that polls every interval and can be woken earlier
// with Notify.
func NewRelay(db *sql.DB, pub Publisher, log *slog.Logger, interval time.Duration) *Relay {
	return &Relay{db: db, pub: pub, log: log, interval: interval, batch: 50, wake: make(chan struct{}, 1)}
}

// Notify asks the relay to flush now (call it after a commit that added rows).
func (r *Relay) Notify() {
	select {
	case r.wake <- struct{}{}:
	default: // a flush is already requested
	}
}

// Run flushes until ctx is cancelled.
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		r.Flush(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
	}
}

// Flush publishes pending rows in order and stops at the first failure, so
// events are never published out of order. It returns the number published.
func (r *Relay) Flush(ctx context.Context) int {
	pending, err := Fetch(ctx, r.db, r.batch)
	if err != nil {
		r.log.ErrorContext(ctx, "outbox fetch failed", "err", err)
		return 0
	}
	published := 0
	for _, p := range pending {
		pctx := otel.GetTextMapPropagator().Extract(ctx, p.Trace)
		if err := r.pub.Publish(pctx, p.Envelope); err != nil {
			r.log.WarnContext(ctx, "outbox publish failed; will retry",
				"event_id", p.Envelope.ID, "type", p.Envelope.Type, "attempts", p.Attempts+1, "err", err)
			if mErr := MarkFailed(ctx, r.db, p.Seq, err); mErr != nil {
				r.log.ErrorContext(ctx, "outbox mark failed", "err", mErr)
			}
			return published
		}
		if err := MarkSent(ctx, r.db, p.Seq); err != nil {
			// The event is already on the broker; at worst it is published
			// again later, which idempotent consumers absorb.
			r.log.ErrorContext(ctx, "outbox mark sent failed", "event_id", p.Envelope.ID, "err", err)
			return published
		}
		published++
	}
	return published
}
