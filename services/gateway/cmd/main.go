// Command gateway is the single entry point of the platform: bearer-token
// auth, routing to services, API composition, the live event stream and the
// admin UI. Wiring only.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"constella/internal/platform/envx"
	"constella/internal/platform/httpx"
	"constella/internal/platform/logx"
	"constella/internal/platform/mq"
	"constella/internal/platform/otelx"
	"constella/services/gateway/internal/adapters"
	"constella/web"
)

const service = "gateway"

func main() {
	log := logx.New(service)
	if err := run(log); err != nil {
		log.Error("gateway stopped", "err", err)
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

	token := os.Getenv("PLATFORM_TOKEN")
	targets, err := serviceURLs()
	if err != nil {
		return err
	}

	client := httpx.NewClient(httpx.ClientOptions{Timeout: 2 * time.Second, Retries: 1, Token: token, Log: log})
	hub := adapters.NewHub(log)
	broker := mq.New(envx.String("AMQP_URL", "amqp://guest:guest@localhost:5672/"), service, log)
	defer broker.Close() //nolint:errcheck // process is exiting

	api := http.NewServeMux()
	adapters.NewProxy(targets, token).Register(api)
	api.Handle("GET /api/overview", adapters.Overview{Client: client, Summaries: []adapters.Summary{
		{Name: "dataprep", URL: targets["dataprep"].String() + "/v1/builds?limit=1"},
		{Name: "catalog", URL: targets["catalog"].String() + "/v1/summary"},
		{Name: "release", URL: targets["release"].String() + "/v1/releases?limit=1"},
		{Name: "telemetry", URL: targets["telemetry"].String() + "/v1/summary"},
	}})
	api.Handle("GET /api/stream", hub)
	apiHandler := httpx.Wrap(api, httpx.ServerOptions{Service: service, Log: log, Token: token})

	ui, err := uiFS()
	if err != nil {
		return err
	}
	root := http.NewServeMux()
	root.Handle("/api/", cors(os.Getenv("CORS_ORIGIN"), apiHandler))
	root.Handle(httpx.HealthPath, apiHandler)
	// The update channel is public (clients have no platform token).
	root.Handle("GET /updates/", adapters.NewUpdates(targets["release"], token))
	root.Handle("/", noCache(http.FileServerFS(ui))) // the UI is public; every call it makes needs the token

	var wg sync.WaitGroup
	wg.Go(func() {
		broker.Consume(ctx, mq.Binding{Queue: "gateway.sse." + service, Keys: []string{"#"}, Transient: true}, hub.Handle)
	})
	serveErr := httpx.Serve(ctx, envx.String("HTTP_ADDR", ":8080"), root, log)

	stop()
	wg.Wait()
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	return nil
}

// serviceURLs reads the base URL of every service the gateway routes to.
func serviceURLs() (map[string]*url.URL, error) {
	defaults := map[string]string{
		"dataprep":  "http://localhost:8081",
		"catalog":   "http://localhost:8082",
		"release":   "http://localhost:8083",
		"telemetry": "http://localhost:8084",
	}
	out := map[string]*url.URL{}
	for name, def := range defaults {
		raw := envx.String("UPSTREAM_"+strings.ToUpper(name), def)
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("UPSTREAM_%s=%q is not a valid URL", strings.ToUpper(name), raw)
		}
		out[name] = u
	}
	return out, nil
}

// uiFS serves the UI from WEB_DIR when set (live editing), else from the
// files embedded in the binary.
func uiFS() (fs.FS, error) {
	if dir := os.Getenv("WEB_DIR"); dir != "" {
		return os.DirFS(dir), nil
	}
	return web.FS(), nil
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// cors lets the UI served by a dev static server (Live Server) call the API.
// It is off unless CORS_ORIGIN lists exactly one allowed origin.
func cors(origin string, next http.Handler) http.Handler {
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
