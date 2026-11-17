package httpx

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HealthPath answers 200 without auth or fault injection (for Compose health
// checks).
const HealthPath = "/healthz"

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// ServerOptions configures Wrap.
type ServerOptions struct {
	Service string
	Log     *slog.Logger
	// Token is the shared bearer token; empty disables auth (local dev).
	Token string
	// Inner middlewares run after auth, closest to the routes (for example
	// fault injection).
	Inner []Middleware
}

// Wrap adds health, tracing, panic recovery, access logging and auth to mux.
// Order, outermost first: otel → recover → log → auth → inner → mux.
func Wrap(mux *http.ServeMux, o ServerOptions) http.Handler {
	mux.HandleFunc("GET "+HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": o.Service})
	})
	var h http.Handler = mux
	for i := len(o.Inner) - 1; i >= 0; i-- {
		h = o.Inner[i](h)
	}
	h = auth(o.Token, h)
	h = accessLog(o.Log, h)
	h = recoverPanic(o.Log, h)
	return otelhttp.NewHandler(h, o.Service,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != HealthPath }),
	)
}

func recoverPanic(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.ErrorContext(r.Context(), "panic in handler",
					"panic", fmt.Sprint(v), "path", r.URL.Path, "stack", string(debug.Stack()))
				WriteError(w, http.StatusInternalServerError, CodeInternal, nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush keeps Server-Sent Events working through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == HealthPath {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.InfoContext(r.Context(), "http request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

func auth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == HealthPath {
			next.ServeHTTP(w, r)
			return
		}
		got := []byte(r.Header.Get("Authorization"))
		// EventSource cannot set headers, so SSE passes the token in the query.
		if len(got) == 0 && strings.HasSuffix(r.URL.Path, "/stream") {
			got = []byte("Bearer " + r.URL.Query().Get("token"))
		}
		if subtle.ConstantTimeCompare(got, want) != 1 {
			WriteError(w, http.StatusUnauthorized, CodeUnauthorized, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve runs h on addr until ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve %s: %w", addr, err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown %s: %w", addr, err)
	}
	return nil
}
