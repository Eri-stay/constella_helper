// Package mq is the RabbitMQ adapter shared by all services.
//
// It grew out of the Lab 2 client (lab2/internal/rabbitmq): one topic
// exchange, durable queues, manual ack. Added here: publisher confirms (needed
// by the outbox), a dead-letter exchange, reconnects after a broker restart,
// and OpenTelemetry context in the message headers.
package mq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"constella/internal/platform/events"
)

// Broker names, declared by every program on start (as in Lab 2), so start-up
// order does not matter.
const (
	Exchange           = "constella.platform"
	DeadLetterExchange = "constella.dlx"
)

var tracer = otel.Tracer("constella/mq")

// ErrNotConfirmed is returned when the broker refused (nacked) a message.
var ErrNotConfirmed = errors.New("broker did not confirm the message")

// Client holds one AMQP connection per process and reopens it on demand.
type Client struct {
	url  string
	name string
	log  *slog.Logger

	mu   sync.Mutex
	conn *amqp.Connection
	pub  *amqp.Channel
}

// New returns a client; it connects lazily on the first Publish or Consume.
// name is shown as the connection name in the RabbitMQ UI.
func New(url, name string, log *slog.Logger) *Client {
	return &Client{url: url, name: name, log: log}
}

// connection returns a live connection, dialling if needed. Caller holds mu.
func (c *Client) connection() (*amqp.Connection, error) {
	if c.conn != nil && !c.conn.IsClosed() {
		return c.conn, nil
	}
	conn, err := amqp.DialConfig(c.url, amqp.Config{
		Properties: amqp.Table{"connection_name": c.name},
	})
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	c.conn = conn
	c.pub = nil
	return conn, nil
}

// channel opens a new channel with the exchanges declared.
func (c *Client) channel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, err := c.connection()
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}
	if err := declareExchanges(ch); err != nil {
		return nil, errors.Join(err, ch.Close())
	}
	return ch, nil
}

func declareExchanges(ch *amqp.Channel) error {
	for _, name := range []string{Exchange, DeadLetterExchange} {
		if err := ch.ExchangeDeclare(name, "topic", true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %s: %w", name, err)
		}
	}
	return nil
}

// publishChannel returns the shared confirm-mode channel. Caller holds mu.
func (c *Client) publishChannel() (*amqp.Channel, error) {
	if c.pub != nil && !c.pub.IsClosed() {
		return c.pub, nil
	}
	conn, err := c.connection()
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open publish channel: %w", err)
	}
	if err := declareExchanges(ch); err != nil {
		return nil, errors.Join(err, ch.Close())
	}
	if err := ch.Confirm(false); err != nil {
		return nil, errors.Join(fmt.Errorf("enable publisher confirms: %w", err), ch.Close())
	}
	c.pub = ch
	return ch, nil
}

// Publish sends env with its type as the routing key and waits for the broker
// confirm. It satisfies outbox.Publisher.
func (c *Client) Publish(ctx context.Context, env events.Envelope) error {
	ctx, span := tracer.Start(ctx, "publish "+env.Type,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(messageAttrs(env)...))
	defer span.End()

	err := c.publish(ctx, env)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (c *Client) publish(ctx context.Context, env events.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope %s: %w", env.ID, err)
	}
	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, tableCarrier(headers))

	c.mu.Lock()
	defer c.mu.Unlock()
	ch, err := c.publishChannel()
	if err != nil {
		return err
	}
	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, Exchange, env.Type, false, false, amqp.Publishing{
		ContentType:   "application/json",
		DeliveryMode:  amqp.Persistent,
		MessageId:     env.ID,
		CorrelationId: env.CorrelationID,
		Type:          env.Type,
		AppId:         env.Producer,
		Timestamp:     env.OccurredAt,
		Headers:       headers,
		Body:          body,
	})
	if err != nil {
		c.dropPublishChannel()
		return fmt.Errorf("publish %s: %w", env.Type, err)
	}
	ok, err := confirm.WaitContext(ctx)
	if err != nil {
		c.dropPublishChannel()
		return fmt.Errorf("wait confirm for %s: %w", env.Type, err)
	}
	if !ok {
		return fmt.Errorf("publish %s: %w", env.Type, ErrNotConfirmed)
	}
	return nil
}

// dropPublishChannel forgets a broken channel so the next publish reopens it.
// Caller holds mu.
func (c *Client) dropPublishChannel() {
	if c.pub != nil {
		// The channel is already broken; its close error carries no information.
		_ = c.pub.Close()
		c.pub = nil
	}
}

// Close closes the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	return c.conn.Close()
}

func messageAttrs(env events.Envelope) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("messaging.system", "rabbitmq"),
		attribute.String("messaging.destination.name", Exchange),
		attribute.String("messaging.rabbitmq.destination.routing_key", env.Type),
		attribute.String("messaging.message.id", env.ID),
		attribute.String("messaging.message.conversation_id", env.CorrelationID),
	}
}

// tableCarrier adapts AMQP headers to the OpenTelemetry propagator.
type tableCarrier amqp.Table

func (t tableCarrier) Get(key string) string {
	if v, ok := t[key].(string); ok {
		return v
	}
	return ""
}

func (t tableCarrier) Set(key, value string) { t[key] = value }

func (t tableCarrier) Keys() []string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	return keys
}
