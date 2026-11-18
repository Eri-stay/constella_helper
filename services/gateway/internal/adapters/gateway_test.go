package adapters

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"constella/internal/platform/events"
	"constella/internal/platform/httpx"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestProxyRoutesAndRewrites(t *testing.T) {
	t.Parallel()
	var gotPath, gotQuery, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	mux := http.NewServeMux()
	NewProxy(map[string]*url.URL{"catalog": mustURL(t, upstream.URL)}, "internal-secret").Register(mux)

	tests := []struct {
		name, target, wantPath, wantQuery string
	}{
		{"api path gets /v1", "/api/catalog/stars?q=sirius&limit=3", "/v1/stars", "limit=3&q=sirius"},
		{"nested path", "/api/catalog/cultures/maya/figures/001", "/v1/cultures/maya/figures/001", ""},
		{"debug path stays", "/api/catalog/debug/fault", "/debug/fault", ""},
		{"ui token is stripped", "/api/catalog/summary?token=ui-secret", "/v1/summary", ""},
	}
	for _, tc := range tests {
		gotPath, gotQuery, gotAuth = "", "", ""
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		req.Header.Set("Authorization", "Bearer ui-secret")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s: status %d", tc.name, rec.Code)
		}
		if gotPath != tc.wantPath || gotQuery != tc.wantQuery {
			t.Errorf("%s: upstream saw %s?%s, want %s?%s", tc.name, gotPath, gotQuery, tc.wantPath, tc.wantQuery)
		}
		if gotAuth != "Bearer internal-secret" {
			t.Errorf("%s: upstream auth = %q, want the internal token", tc.name, gotAuth)
		}
	}
}

func TestProxyErrors(t *testing.T) {
	t.Parallel()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // nothing listens there any more

	mux := http.NewServeMux()
	NewProxy(map[string]*url.URL{"catalog": mustURL(t, deadURL)}, "").Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope/x", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), httpx.CodeNotFound) {
		t.Errorf("unknown service: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/stars", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), httpx.CodeUnavailable) {
		t.Errorf("dead upstream: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOverviewHandler(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"active": false})
	}))
	t.Cleanup(up.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, nil)
	}))
	t.Cleanup(down.Close)

	o := Overview{
		Client:    httpx.NewClient(httpx.ClientOptions{Timeout: time.Second, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}),
		Summaries: []Summary{{Name: "catalog", URL: up.URL}, {Name: "dataprep", URL: down.URL}},
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/overview", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"status":"ok"`) ||
		!strings.Contains(body, `"status":"down"`) || !strings.Contains(body, `"error":"NOT_FOUND"`) {
		t.Errorf("overview: %d %s", rec.Code, body)
	}
}

func TestHubStreamsEventsToSubscribers(t *testing.T) {
	t.Parallel()
	hub := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test response
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Scan() // ": connected"; the subscriber is registered before this line is sent

	env, _ := events.New(events.BuildCompleted, "dataprep", map[string]string{"build_id": "b1"})
	if err := hub.Handle(ctx, env); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for len(lines) < 2 && sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	if len(lines) < 2 || lines[0] != "event: "+events.BuildCompleted || !strings.HasPrefix(lines[1], "data: {") ||
		!strings.Contains(lines[1], env.ID) {
		t.Errorf("stream lines: %q", lines)
	}
}
