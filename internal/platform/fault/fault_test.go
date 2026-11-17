package fault

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInjector(t *testing.T) {
	t.Parallel()
	in := &Injector{}
	mux := http.NewServeMux()
	in.Register(mux)
	mux.HandleFunc("GET /v1/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := in.Middleware(mux)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	if rec := do(http.MethodGet, "/v1/x", ""); rec.Code != http.StatusOK {
		t.Fatalf("no fault: status %d", rec.Code)
	}

	do(http.MethodPost, Path, `{"fail_rate": 5, "latency_ms": 30, "status": 200}`)
	if got := in.Get(); got.FailRate != 1 || got.Status != http.StatusServiceUnavailable {
		t.Fatalf("settings not clamped: %+v", got)
	}
	start := time.Now()
	if rec := do(http.MethodGet, "/v1/x", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fault: status %d, want 503", rec.Code)
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Fatal("latency not applied")
	}
	if rec := do(http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("health affected by fault: %d", rec.Code)
	}

	do(http.MethodDelete, Path, "")
	if rec := do(http.MethodGet, "/v1/x", ""); rec.Code != http.StatusOK {
		t.Fatalf("after reset: status %d", rec.Code)
	}
}
