package adapters

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"constella/internal/platform/httpx"
	"constella/services/dataprep/internal/app"
)

// BuildService is what the HTTP handlers need from the use cases.
type BuildService interface {
	Start(ctx context.Context) (app.Build, error)
	Get(ctx context.Context, id string) (app.Build, error)
	List(ctx context.Context, limit int) ([]app.Build, error)
}

// Routes registers the dataprep REST API on mux.
//
//	POST /v1/builds        202 {build}   start a build in the background
//	GET  /v1/builds        200 {items}   newest first, ?limit=
//	GET  /v1/builds/{id}   200 {build}
func Routes(mux *http.ServeMux, svc BuildService) {
	h := handlers{svc: svc}
	mux.HandleFunc("POST /v1/builds", h.start)
	mux.HandleFunc("GET /v1/builds", h.list)
	mux.HandleFunc("GET /v1/builds/{id}", h.get)
}

type handlers struct{ svc BuildService }

func (h handlers) start(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.Start(r.Context())
	switch {
	case errors.Is(err, app.ErrBusy):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, map[string]any{"reason": "BUILD_RUNNING"})
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
	default:
		w.Header().Set("Location", "/v1/builds/"+b.ID)
		httpx.WriteJSON(w, http.StatusAccepted, b)
	}
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, map[string]any{"field": "limit"})
			return
		}
		limit = n
	}
	items, err := h.svc.List(r.Context(), limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
		return
	}
	if items == nil {
		items = []app.Build{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.Get(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, nil)
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
	default:
		httpx.WriteJSON(w, http.StatusOK, b)
	}
}
