package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// flaky answers 503 for the first `failures` calls, then 200 {"ok":true}.
func flaky(failures int32, calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= failures {
			WriteError(w, http.StatusServiceUnavailable, CodeUnavailable, nil)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

func TestClientRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		method    string
		idemKey   string
		failures  int32
		wantCalls int32
		wantErr   bool
	}{
		{"get retried until success", http.MethodGet, "", 2, 3, false},
		{"post without key not retried", http.MethodPost, "", 1, 1, true},
		{"post with key retried", http.MethodPost, "k1", 1, 2, false},
		{"retries exhausted", http.MethodGet, "", 5, 3, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(flaky(tc.failures, &calls))
			t.Cleanup(srv.Close)
			c := NewClient(ClientOptions{Retries: 2, Backoff: time.Millisecond, TripAfter: 10, Log: quietLog()})

			var out struct{ OK bool }
			err := c.Do(context.Background(), Request{Method: tc.method, URL: srv.URL, Out: &out, IdempotencyKey: tc.idemKey})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if calls.Load() != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
			if !tc.wantErr && !out.OK {
				t.Fatal("response not decoded")
			}
		})
	}
}

func TestClientStatusErrorNotRetried(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		WriteError(w, http.StatusConflict, CodeConflict, map[string]any{"build": "b1"})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(ClientOptions{Retries: 3, Backoff: time.Millisecond, Log: quietLog()})

	err := c.Do(context.Background(), Request{Method: http.MethodGet, URL: srv.URL})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusConflict || se.Body.Code != CodeConflict {
		t.Fatalf("err = %v, want 409 CONFLICT", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (4xx is not retried)", calls.Load())
	}
}

func TestClientBreakerOpensAndRecovers(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(flaky(3, &calls))
	t.Cleanup(srv.Close)
	c := NewClient(ClientOptions{TripAfter: 3, OpenFor: 100 * time.Millisecond, Log: quietLog()})
	get := func() error {
		return c.Do(context.Background(), Request{Method: http.MethodGet, URL: srv.URL})
	}

	for range 3 {
		if err := get(); err == nil {
			t.Fatal("want upstream failure")
		}
	}
	if err := get(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (open breaker must not call)", calls.Load())
	}
	time.Sleep(150 * time.Millisecond)
	if err := get(); err != nil {
		t.Fatalf("half-open probe failed: %v", err)
	}
	for host, st := range c.BreakerStates() {
		if st != "closed" {
			t.Fatalf("breaker %s = %s, want closed", host, st)
		}
	}
}

func TestClientTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient(ClientOptions{Timeout: 50 * time.Millisecond, Log: quietLog()})

	start := time.Now()
	err := c.Do(context.Background(), Request{Method: http.MethodGet, URL: srv.URL})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("timeout not applied")
	}
}
