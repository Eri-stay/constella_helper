package adapters

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"constella/internal/domain/release"
	"constella/internal/platform/httpx"
	"constella/services/release/internal/app"
)

// Releases is what the handlers need from the use cases.
type Releases interface {
	PublishClassic(ctx context.Context, buildID string) (*release.Release, error)
	RequestEDA(ctx context.Context, buildID string) (*release.Release, error)
	Get(ctx context.Context, id string) (*release.Release, []release.Event, error)
	List(ctx context.Context, limit int) ([]*release.Release, error)
	Manifest(ctx context.Context) ([]byte, error)
}

// Routes registers the release REST API on mux.
//
//	POST /v1/releases?mode=classic  {build_id}  200 {release} | 502 {code, params: step, ...}
//	POST /v1/releases?mode=eda      {build_id}  202 {release}; progress via GET and the event stream
//	GET  /v1/releases, /v1/releases/{id}
//	GET  /v1/version                the published version.json
func Routes(mux *http.ServeMux, svc Releases) {
	h := handlers{svc: svc}
	mux.HandleFunc("POST /v1/releases", h.publish)
	mux.HandleFunc("GET /v1/releases", h.list)
	mux.HandleFunc("GET /v1/releases/{id}", h.get)
	mux.HandleFunc("GET /v1/version", h.version)
}

type handlers struct{ svc Releases }

// View is the JSON shape of a release.
type View struct {
	ID         string          `json:"id"`
	BuildID    string          `json:"build_id"`
	Mode       string          `json:"mode"`
	Status     release.Status  `json:"status"`
	Version    string          `json:"version,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	EndedAt    *time.Time      `json:"ended_at,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	Failure    *Failure        `json:"failure,omitempty"`
	Events     []release.Event `json:"events,omitempty"`
}

// Failure describes how a release failed.
type Failure struct {
	Step        string `json:"step"`
	Code        string `json:"code"`
	Compensated bool   `json:"compensated"`
	Detail      string `json:"detail,omitempty"`
}

func view(r *release.Release, evs []release.Event) View {
	v := View{ID: r.ID, BuildID: r.BuildID, Mode: r.Mode, Status: r.Status, Version: r.Version,
		StartedAt: r.StartedAt, Events: evs}
	if !r.EndedAt.IsZero() {
		end := r.EndedAt
		v.EndedAt = &end
		v.DurationMS = end.Sub(r.StartedAt).Milliseconds()
	}
	if r.Failure != nil {
		v.Failure = &Failure{Step: r.Failure.Step, Code: r.Failure.Code, Compensated: r.Failure.Compensated, Detail: r.Failure.Detail}
	}
	return v
}

func (h handlers) publish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BuildID string `json:"build_id"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil || in.BuildID == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, map[string]any{"field": "build_id"})
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = release.ModeClassic
	}
	switch mode {
	case release.ModeEDA:
		rel, err := h.svc.RequestEDA(r.Context(), in.BuildID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
			return
		}
		w.Header().Set("Location", "/v1/releases/"+rel.ID)
		httpx.WriteJSON(w, http.StatusAccepted, view(rel, nil))
		return
	case release.ModeClassic:
	default:
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, map[string]any{"field": "mode"})
		return
	}
	rel, err := h.svc.PublishClassic(r.Context(), in.BuildID)
	var se *app.StepError
	switch {
	case errors.As(err, &se):
		httpx.WriteError(w, http.StatusBadGateway, "RELEASE_FAILED", map[string]any{
			"release_id": rel.ID, "step": se.Step, "reason": se.Code, "compensated": se.Compensated})
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
	default:
		httpx.WriteJSON(w, http.StatusOK, view(rel, nil))
	}
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	n := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, map[string]any{"field": "limit"})
			return
		}
	}
	rels, err := h.svc.List(r.Context(), n)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
		return
	}
	items := make([]View, 0, len(rels))
	for _, rel := range rels {
		items = append(items, view(rel, nil))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	rel, evs, err := h.svc.Get(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, nil)
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
	default:
		httpx.WriteJSON(w, http.StatusOK, view(rel, evs))
	}
}

func (h handlers) version(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.Manifest(r.Context())
	switch {
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
	case b == nil:
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, map[string]any{"what": "version.json"})
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b) // a write error means the client left
	}
}
