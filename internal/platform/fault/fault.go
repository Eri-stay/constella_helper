// Package fault injects latency and errors into a service at runtime, so the
// experiments of Task 2 (slow or failing service) can be driven from the UI
// without restarting containers.
package fault

import (
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Path is where the settings are read (GET), changed (POST) and reset (DELETE).
const Path = "/debug/fault"

// Settings is the current fault configuration.
type Settings struct {
	LatencyMS int     `json:"latency_ms"`
	FailRate  float64 `json:"fail_rate"` // 0..1
	Status    int     `json:"status"`    // HTTP status for injected failures; default 503
}

// Injector holds Settings and applies them to requests.
type Injector struct {
	mu sync.RWMutex
	s  Settings
}

// Get returns a copy of the current settings.
func (in *Injector) Get() Settings {
	in.mu.RLock()
	defer in.mu.RUnlock()
	return in.s
}

// Set replaces the settings after clamping them to valid ranges.
func (in *Injector) Set(s Settings) Settings {
	s.LatencyMS = max(0, min(s.LatencyMS, 60_000))
	s.FailRate = max(0, min(s.FailRate, 1))
	if s.Status < 400 || s.Status > 599 {
		s.Status = http.StatusServiceUnavailable
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.s = s
	return s
}

// Middleware delays and fails requests according to the settings. Health
// checks and the debug endpoints themselves are never affected.
func (in *Injector) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/debug/") {
			next.ServeHTTP(w, r)
			return
		}
		s := in.Get()
		span := trace.SpanFromContext(r.Context())
		if s.LatencyMS > 0 {
			span.AddEvent("fault: latency", trace.WithAttributes(attribute.Int("latency_ms", s.LatencyMS)))
			select {
			case <-time.After(time.Duration(s.LatencyMS) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		if s.FailRate > 0 && rand.Float64() < s.FailRate {
			span.AddEvent("fault: injected failure", trace.WithAttributes(attribute.Int("status", s.Status)))
			writeJSON(w, s.Status, map[string]string{"code": "FAULT_INJECTED"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Register adds the debug endpoints to mux.
func (in *Injector) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+Path, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, in.Get())
	})
	mux.HandleFunc("POST "+Path, func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&s); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"code": "BAD_REQUEST"})
			return
		}
		writeJSON(w, http.StatusOK, in.Set(s))
	})
	mux.HandleFunc("DELETE "+Path, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, in.Set(Settings{}))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Headers are sent; an encode error means the client has gone.
	_ = json.NewEncoder(w).Encode(v)
}
