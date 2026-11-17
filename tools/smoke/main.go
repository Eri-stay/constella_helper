// Command smoke checks the M0 platform end to end against the running
// infrastructure: an event is committed to an outbox, relayed to RabbitMQ,
// consumed through the inbox (a duplicate is skipped), and the whole chain is
// exported as one trace to Jaeger.
//
//	docker compose -f deploy/docker-compose.yml up -d
//	OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 go run ./tools/smoke
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"constella/internal/platform/envx"
	"constella/internal/platform/events"
	"constella/internal/platform/inbox"
	"constella/internal/platform/logx"
	"constella/internal/platform/mq"
	"constella/internal/platform/otelx"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
)

const (
	service   = "smoke"
	eventType = "smoke.ping"
)

func main() {
	log := logx.New(service)
	if err := run(log); err != nil {
		log.Error("smoke failed", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	shutdown, err := otelx.Init(ctx, service)
	if err != nil {
		return err
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			log.Warn("otel shutdown", "err", err)
		}
	}()

	dir, err := os.MkdirTemp("", "constella-smoke-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // temp files only
	db, err := sqlitex.Open(ctx, filepath.Join(dir, "smoke.db"), outbox.Schema, inbox.Schema,
		`CREATE TABLE seen (event_id TEXT)`)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // process exits next

	client := mq.New(envx.String("AMQP_URL", "amqp://guest:guest@localhost:5672/"), service, log)
	defer client.Close() //nolint:errcheck // process exits next

	// Stop the consumer before the connection closes, so it exits quietly.
	consumeCtx, stopConsumer := context.WithCancel(ctx)
	defer stopConsumer()

	received := make(chan string, 4)
	go client.Consume(consumeCtx, mq.Binding{Queue: "smoke.q", Keys: []string{eventType}, Transient: true},
		func(ctx context.Context, env events.Envelope) error {
			dup, err := inbox.Process(ctx, db, service, env.ID, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO seen VALUES (?)`, env.ID)
				return err
			})
			if err != nil {
				return err
			}
			if dup {
				received <- "duplicate:" + env.ID
			} else {
				received <- env.ID
			}
			return nil
		})
	time.Sleep(time.Second) // let the consumer declare its queue

	ctx, span := otel.Tracer(service).Start(ctx, "smoke run")
	defer span.End()
	traceID := trace.SpanContextFromContext(ctx).TraceID().String()

	env, err := events.New(eventType, service, map[string]string{"hello": "constella"})
	if err != nil {
		return err
	}
	if err := sqlitex.InTx(ctx, db, func(tx *sql.Tx) error { return outbox.Add(ctx, tx, env) }); err != nil {
		return err
	}
	relay := outbox.NewRelay(db, client, log, time.Hour)
	if n := relay.Flush(ctx); n != 1 {
		return fmt.Errorf("relay published %d events, want 1", n)
	}
	if err := client.Publish(ctx, env); err != nil { // simulate a redelivery
		return err
	}

	want := []string{env.ID, "duplicate:" + env.ID}
	for _, w := range want {
		select {
		case got := <-received:
			if got != w {
				return fmt.Errorf("received %q, want %q", got, w)
			}
		case <-ctx.Done():
			return errors.New("timed out waiting for the event")
		}
	}
	log.InfoContext(ctx, "smoke ok: outbox → rabbitmq → inbox (duplicate skipped)",
		"event_id", env.ID,
		"jaeger", "http://localhost:16686/trace/"+traceID)
	return nil
}
