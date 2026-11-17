package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrap(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"pong": "1"})
	})
	mux.HandleFunc("GET /v1/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := Wrap(mux, ServerOptions{Service: "test", Log: quietLog(), Token: "secret"})

	tests := []struct {
		name   string
		path   string
		auth   string
		status int
	}{
		{"health needs no token", "/healthz", "", http.StatusOK},
		{"missing token", "/v1/ping", "", http.StatusUnauthorized},
		{"wrong token", "/v1/ping", "Bearer nope", http.StatusUnauthorized},
		{"good token", "/v1/ping", "Bearer secret", http.StatusOK},
		{"sse token in query", "/v1/stream?token=secret", "", http.StatusOK},
		{"panic becomes 500", "/v1/boom", "Bearer secret", http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
		})
	}
}
