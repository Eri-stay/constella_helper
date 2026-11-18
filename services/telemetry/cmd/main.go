// Command telemetry keeps the local audit trail and receives client logs.
// Sentry is used only when SENTRY_DSN is set. Wiring only.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"constella/internal/platform/envx"
	"constella/internal/platform/fault"
	"constella/internal/platform/httpx"
	"constella/internal/platform/logx"
	"constella/internal/platform/mq"
	"constella/internal/platform/otelx"
	"constella/internal/platform/sqlitex"
	"constella/services/telemetry/internal/adapters"
	"constella/services/telemetry/internal/app"
)

const service = "telemetry"

func main() {
	log := logx.New(service)
	if err := run(log); err != nil {
		log.Error("telemetry stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownOtel, err := otelx.Init(ctx, service)
	if err != nil {
		return err
	}
	defer func() {
		if err := shutdownOtel(context.Background()); err != nil {
			log.Warn("otel shutdown", "err", err)
		}
	}()

	db, err := sqlitex.Open(ctx, envx.String("DB_PATH", "data/telemetry.db"), adapters.Schema...)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // process is exiting

	var sink app.ErrorSink = adapters.NoopSink{}
	if dsn := os.Getenv("SENTRY_DSN"); dsn != "" {
		if sink, err = adapters.NewSentrySink(dsn); err != nil {
			return err
		}
	}
	log.Info("error sink", "sink", sink.Name())
	svc := app.New(adapters.Store{DB: db}, sink, log)

	broker := mq.New(envx.String("AMQP_URL", "amqp://guest:guest@localhost:5672/"), service, log)
	defer broker.Close() //nolint:errcheck // process is exiting

	var inj fault.Injector
	mux := http.NewServeMux()
	adapters.Routes(mux, svc)
	inj.Register(mux)
	handler := httpx.Wrap(mux, httpx.ServerOptions{
		Service: service, Log: log, Token: os.Getenv("PLATFORM_TOKEN"),
		Inner: []httpx.Middleware{inj.Middleware},
	})

	var wg sync.WaitGroup
	wg.Go(func() {
		broker.Consume(ctx, mq.Binding{Queue: "telemetry.q", Keys: []string{"#"}}, svc.HandleEvent)
	})
	serveErr := httpx.Serve(ctx, envx.String("HTTP_ADDR", ":8084"), handler, log)

	stop()
	wg.Wait()
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	return nil
}
