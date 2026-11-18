// Command research runs experiments on constellation generation: sweeps over
// strategies, parameters and seeds, compared with a sky culture. It reads the
// stars from catalog over gRPC streams. Wiring only.
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
	"constella/services/research/internal/adapters"
	"constella/services/research/internal/app"
)

const service = "research"

func main() {
	log := logx.New(service)
	if err := run(log); err != nil {
		log.Error("research stopped", "err", err)
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

	db, err := sqlitex.Open(ctx, envx.String("DB_PATH", "data/research.db"), adapters.Schema...)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // process is exiting

	token := os.Getenv("PLATFORM_TOKEN")
	rest := httpx.NewClient(httpx.ClientOptions{Timeout: 3 * time.Second, Retries: 1, Token: token, Log: log})
	catalog, err := adapters.NewCatalogClient(envx.String("CATALOG_GRPC", "localhost:9082"),
		envx.String("CATALOG_URL", "http://localhost:8082"), token, rest)
	if err != nil {
		return err
	}
	defer catalog.Close() //nolint:errcheck // process is exiting

	broker := mq.New(envx.String("AMQP_URL", "amqp://guest:guest@localhost:5672/"), service, log)
	defer broker.Close() //nolint:errcheck // process is exiting
	relay := outbox.NewRelay(db, broker, log, 2*time.Second)
	svc := app.New(adapters.Store{DB: db}, catalog, relay, log)
	if err := svc.Recover(ctx); err != nil {
		return err
	}

	var inj fault.Injector
	mux := http.NewServeMux()
	adapters.Routes(mux, svc)
	inj.Register(mux)
	handler := httpx.Wrap(mux, httpx.ServerOptions{
		Service: service, Log: log, Token: token, Inner: []httpx.Middleware{inj.Middleware},
	})

	var wg sync.WaitGroup
	wg.Go(func() { relay.Run(ctx) })
	serveErr := httpx.Serve(ctx, envx.String("HTTP_ADDR", ":8085"), handler, log)

	stop() // also when Serve failed: the relay must stop before the database closes
	drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc.Wait(drain)
	wg.Wait()
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	return nil
}
