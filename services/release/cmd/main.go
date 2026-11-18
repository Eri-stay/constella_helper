// Command release publishes cosmos.db builds: the PublishRelease saga, the
// version.json manifest and the downloadable artifacts. Wiring only.
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
	"time"

	"constella/internal/platform/envx"
	"constella/internal/platform/fault"
	"constella/internal/platform/httpx"
	"constella/internal/platform/logx"
	"constella/internal/platform/mq"
	"constella/internal/platform/otelx"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
	"constella/services/release/internal/adapters"
	"constella/services/release/internal/app"
)

const service = "release"

func main() {
	log := logx.New(service)
	if err := run(log); err != nil {
		log.Error("release stopped", "err", err)
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

	db, err := sqlitex.Open(ctx, envx.String("DB_PATH", "data/release.db"), adapters.Schema...)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // process is exiting

	token := os.Getenv("PLATFORM_TOKEN")
	client := httpx.NewClient(httpx.ClientOptions{Timeout: 3 * time.Second, Retries: 1, Token: token, Log: log})
	releases := envx.String("RELEASES_DIR", "releases")
	broker := mq.New(envx.String("AMQP_URL", "amqp://guest:guest@localhost:5672/"), service, log)
	defer broker.Close() //nolint:errcheck // process is exiting
	relay := outbox.NewRelay(db, broker, log, 2*time.Second)
	svc := app.New(
		adapters.Store{DB: db},
		adapters.CatalogClient{Client: client, BaseURL: envx.String("CATALOG_URL", "http://localhost:8082")},
		&adapters.FilePublisher{ArtifactsDir: envx.String("ARTIFACTS_DIR", "artifacts"), ReleasesDir: releases},
		adapters.AuditClient{Client: client, BaseURL: envx.String("TELEMETRY_URL", "http://localhost:8084")},
		log).WithRelay(relay)

	var inj fault.Injector
	mux := http.NewServeMux()
	adapters.Routes(mux, svc)
	inj.Register(mux)
	// Clients download version.json and the data files without a token: the
	// update channel is public, protected by HTTPS and SHA-256 in the product.
	mux.Handle("GET /updates/", http.StripPrefix("/updates/", http.FileServer(http.Dir(releases))))
	handler := httpx.Wrap(mux, httpx.ServerOptions{
		Service: service, Log: log, Token: token,
		Inner: []httpx.Middleware{inj.Middleware},
	})

	// The relay and the saga worker stop before the database closes.
	var wg sync.WaitGroup
	wg.Go(func() { relay.Run(ctx) })
	wg.Go(func() { svc.RunWorker(ctx) })
	serveErr := httpx.Serve(ctx, envx.String("HTTP_ADDR", ":8083"), handler, log)
	stop()
	wg.Wait()
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	return nil
}
