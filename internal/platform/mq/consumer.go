package mq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"constella/internal/platform/events"
)

// Handler processes one event. Returning an error makes the message be retried
// once and then moved to the queue's dead-letter queue.
type Handler func(ctx context.Context, env events.Envelope) error

// Binding describes a consumer queue and the routing keys it listens to.
type Binding struct {
	Queue string
	Keys  []string
	// Transient makes a non-durable, exclusive, auto-deleted queue without a
	// DLQ (for live views such as the gateway SSE stream).
	Transient bool
}

const prefetch = 16

// Consume declares b and delivers its messages to h until ctx is cancelled.
// It reconnects with backoff when the broker goes away.
func (c *Client) Consume(ctx context.Context, b Binding, h Handler) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := c.consumeOnce(ctx, b, h)
		if ctx.Err() != nil {
			return
		}
		c.log.WarnContext(ctx, "consumer stopped; reconnecting", "queue", b.Queue, "in", backoff, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func (c *Client) consumeOnce(ctx context.Context, b Binding, h Handler) error {
	ch, err := c.channel()
	if err != nil {
		return err
	}
	// Closing an already closed channel only reports that it was closed.
	defer func() { _ = ch.Close() }()

	if err := declareQueue(ch, b); err != nil {
		return err
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}
	deliveries, err := ch.ConsumeWithContext(ctx, b.Queue, c.name, false, b.Transient, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", b.Queue, err)
	}
	c.log.InfoContext(ctx, "consuming", "queue", b.Queue, "keys", b.Keys)
	for d := range deliveries {
		c.handle(ctx, b.Queue, d, h)
	}
	return errors.New("delivery channel closed")
}

func declareQueue(ch *amqp.Channel, b Binding) error {
	var err error
	if b.Transient {
		_, err = ch.QueueDeclare(b.Queue, false, true, true, false, nil)
	} else {
		dlq := b.Queue + ".dlq"
		if _, err = ch.QueueDeclare(dlq, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare queue %s: %w", dlq, err)
		}
		if err = ch.QueueBind(dlq, b.Queue, DeadLetterExchange, false, nil); err != nil {
			return fmt.Errorf("bind %s to dlx: %w", dlq, err)
		}
		_, err = ch.QueueDeclare(b.Queue, true, false, false, false, amqp.Table{
			"x-dead-letter-exchange":    DeadLetterExchange,
			"x-dead-letter-routing-key": b.Queue,
		})
	}
	if err != nil {
		return fmt.Errorf("declare queue %s: %w", b.Queue, err)
	}
	for _, key := range b.Keys {
		if err := ch.QueueBind(b.Queue, key, Exchange, false, nil); err != nil {
			return fmt.Errorf("bind queue %s (key %s): %w", b.Queue, key, err)
		}
	}
	return nil
}

// handle runs h for one delivery inside a consumer span linked to the
// producer's trace, then acks, retries once, or dead-letters.
func (c *Client) handle(ctx context.Context, queue string, d amqp.Delivery, h Handler) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, tableCarrier(d.Headers))
	ctx, span := tracer.Start(ctx, "consume "+d.RoutingKey,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination.name", queue),
			attribute.String("messaging.message.id", d.MessageId),
			attribute.Bool("messaging.rabbitmq.redelivered", d.Redelivered),
		))
	defer span.End()

	var env events.Envelope
	err := json.Unmarshal(d.Body, &env)
	if err == nil {
		err = env.Validate()
	}
	if err != nil {
		c.fail(ctx, span, d, false, fmt.Errorf("decode message %s: %w", d.MessageId, err))
		return
	}
	if err := h(ctx, env); err != nil {
		c.fail(ctx, span, d, !d.Redelivered, fmt.Errorf("handle %s %s: %w", env.Type, env.ID, err))
		return
	}
	if err := d.Ack(false); err != nil {
		// The broker will redeliver; the inbox makes that harmless.
		c.log.WarnContext(ctx, "ack failed", "queue", queue, "event_id", env.ID, "err", err)
	}
}

func (c *Client) fail(ctx context.Context, span trace.Span, d amqp.Delivery, requeue bool, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	c.log.ErrorContext(ctx, "message failed", "requeue", requeue, "err", err)
	if nErr := d.Nack(false, requeue); nErr != nil {
		c.log.WarnContext(ctx, "nack failed", "err", nErr)
	}
}
