package adapters

import (
	"context"
	"errors"
	"net/http"

	"constella/internal/platform/httpx"
	"constella/services/gateway/internal/app"
)

// Summary is one downstream service the overview asks.
type Summary struct {
	Name string
	URL  string // full URL of the service's summary endpoint
}

// Overview serves GET /api/overview: the API-composition endpoint. It asks the
// services in parallel and reports a service that is down instead of failing.
type Overview struct {
	Client    *httpx.Client
	Summaries []Summary
}

func (o Overview) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ups := make([]app.Upstream, len(o.Summaries))
	for i, s := range o.Summaries {
		ups[i] = app.Upstream{Name: s.Name, Fetch: func(ctx context.Context) (any, error) {
			var out any
			err := o.Client.Do(ctx, httpx.Request{Method: http.MethodGet, URL: s.URL, Out: &out})
			return out, err
		}}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"services": app.Overview(r.Context(), ups, errorCode),
		"breakers": o.Client.BreakerStates(),
	})
}

// errorCode reduces an upstream error to a code the UI can translate.
func errorCode(err error) string {
	var se *httpx.StatusError
	switch {
	case errors.Is(err, httpx.ErrCircuitOpen):
		return "CIRCUIT_OPEN"
	case errors.As(err, &se):
		return se.Body.Code
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	}
	return "UNREACHABLE"
}
