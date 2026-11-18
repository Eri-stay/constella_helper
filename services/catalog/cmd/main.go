// Command catalog validates cosmos.db builds announced by dataprep and serves
// star, constellation and culture queries from the active one. Wiring only.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"constella/internal/platform/envx"
	"constella/internal/platform/events"
	"constella/internal/platform/fault"
	"constella/internal/platform/httpx"
	"constella/internal/platform/logx"
	"constella/internal/platform/mq"
	"constella/internal/platform/otelx"
	"constella/internal/platform/outbox"
	"constella/internal/platform/sqlitex"
	"constella/services/catalog/internal/adapters"
	"constella/services/catalog/internal/app"
)

const service = "catalog"

func main() {
	log := logx.New(service)
	if err := run(log); err != nil {
		log.Error("catalog stopped", "err", err)
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

	db, err := sqlitex.Open(ctx, envx.String("DB_PATH", "data/catalog.db"), adapters.Schema...)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // process is exiting

	broker := mq.New(envx.String("AMQP_URL", "amqp://guest:guest@localhost:5672/"), service, log)
	defer broker.Close() //nolint:errcheck // process is exiting
	relay := outbox.NewRelay(db, broker, log, 2*time.Second)

	repo := adapters.Repo{DB: db}
	index := &adapters.Index{}
	artifactsDir := envx.String("ARTIFACTS_DIR", "artifacts")
	index.Resolve = func(ctx context.Context, buildID string) (string, error) {
		rec, err := repo.Report(ctx, buildID)
		if err != nil {
			return "", adapters.ErrNotFound
		}
		path := filepath.Join(artifactsDir, filepath.Base(rec.Artifact))
		if _, err := os.Stat(path); err != nil {
			return "", adapters.ErrNotFound
		}
		return path, nil
	}
	defer index.Close() //nolint:errcheck // process is exiting
	svc := app.New(repo, adapters.Inspector{}, index, relay, artifactsDir, log)
	if err := svc.Restore(ctx); err != nil {
		return err
	}

	var inj fault.Injector
	mux := http.NewServeMux()
	adapters.Routes(mux, svc, index)
	inj.Register(mux)
	handler := httpx.Wrap(mux, httpx.ServerOptions{
		Service: service, Log: log, Token: os.Getenv("PLATFORM_TOKEN"),
		Inner: []httpx.Middleware{inj.Middleware},
	})

	var wg sync.WaitGroup
	wg.Go(func() { relay.Run(ctx) })
	wg.Go(func() {
		broker.Consume(ctx, mq.Binding{Queue: "catalog.q", Keys: []string{events.BuildCompleted}}, svc.HandleBuildCompleted)
	})
	grpcAddr := envx.String("GRPC_ADDR", ":9082")
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("listen grpc %s: %w", grpcAddr, err)
	}
	grpcSrv := adapters.NewGRPCServer(index, os.Getenv("PLATFORM_TOKEN"))
	wg.Go(func() {
		log.Info("grpc listening", "addr", grpcAddr)
		if err := grpcSrv.Serve(lis); err != nil {
			log.Error("grpc server stopped", "err", err)
		}
	})
	wg.Go(func() { <-ctx.Done(); grpcSrv.GracefulStop() })
	serveErr := httpx.Serve(ctx, envx.String("HTTP_ADDR", ":8082"), handler, log)

	stop() // also when Serve failed: background loops must stop before the database closes
	wg.Wait()
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	return nil
}
