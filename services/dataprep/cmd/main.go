// Command dataprep builds cosmos.db from HYG and the sky cultures, as a
// background job with progress events. Wiring only; the logic is in internal/.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	"constella/services/dataprep/internal/adapters"
	"constella/services/dataprep/internal/app"
)

const service = "dataprep"

func main() {
	log := logx.New(service)
	if err := run(log); err != nil {
		log.Error("dataprep stopped", "err", err)
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

	db, err := sqlitex.Open(ctx, envx.String("DB_PATH", "data/dataprep.db"), adapters.Schema...)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // process is exiting

	broker := mq.New(envx.String("AMQP_URL", "amqp://guest:guest@localhost:5672/"), service, log)
	defer broker.Close() //nolint:errcheck // process is exiting
	relay := outbox.NewRelay(db, broker, log, 2*time.Second)

	input := envx.String("INPUT_DIR", "testdata")
	svc := app.New(
		adapters.Store{DB: db},
		adapters.FileSources{
			HYGPath:     envx.String("HYG_PATH", filepath.Join("data", "hyg_v42.csv")),
			CulturesDir: envx.String("CULTURES_DIR", filepath.Join(input, "cultures")),
			NamesUKPath: envx.String("NAMES_UK_PATH", filepath.Join(input, "names_uk.csv")),
		},
		adapters.ArtifactWriter{Dir: envx.String("ARTIFACTS_DIR", "artifacts")},
		relay, log)
	if err := svc.Recover(ctx); err != nil {
		return err
	}

	var inj fault.Injector
	mux := http.NewServeMux()
	adapters.Routes(mux, svc)
	inj.Register(mux)
	handler := httpx.Wrap(mux, httpx.ServerOptions{
		Service: service, Log: log, Token: os.Getenv("PLATFORM_TOKEN"),
		Inner: []httpx.Middleware{inj.Middleware},
	})

	var wg sync.WaitGroup
	wg.Go(func() { relay.Run(ctx) })
	serveErr := httpx.Serve(ctx, envx.String("HTTP_ADDR", ":8081"), handler, log)

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
