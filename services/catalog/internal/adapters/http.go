package adapters

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"constella/internal/platform/httpx"
	"constella/services/catalog/internal/app"
)

// CodeNoCatalog is the error code while no build has been validated yet.
const CodeNoCatalog = "NO_ACTIVE_CATALOG"

// Reports is what the report endpoints need from the use cases.
type Reports interface {
	Report(ctx context.Context, buildID string) (app.Record, error)
	Reports(ctx context.Context, limit int) ([]app.Record, error)
	Active(ctx context.Context) (app.Record, bool, error)
}

// Routes registers the catalog REST API on mux.
//
//	GET /v1/reports, /v1/reports/{build}            validation reports
//	GET /v1/summary                                 active build and counts
//	GET /v1/stars?q=&limit=                         star search
//	GET /v1/constellations                          the 88 IAU constellations
//	GET /v1/cultures, /v1/cultures/{id}             sky cultures and their figures
//	GET /v1/cultures/{id}/figures/{figure}          one figure with stars and lines
func Routes(mux *http.ServeMux, reports Reports, ix *Index) {
	h := handlers{reports: reports, ix: ix}
	mux.HandleFunc("GET /v1/reports", h.reportList)
	mux.HandleFunc("GET /v1/reports/{build}", h.reportOne)
	mux.HandleFunc("GET /v1/summary", h.summary)
	mux.HandleFunc("GET /v1/stars", h.stars)
	mux.HandleFunc("GET /v1/constellations", h.constellations)
	mux.HandleFunc("GET /v1/cultures", h.cultures)
	mux.HandleFunc("GET /v1/cultures/{id}", h.figures)
	mux.HandleFunc("GET /v1/cultures/{id}/figures/{figure}", h.figure)
}

type handlers struct {
	reports Reports
	ix      *Index
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrNotFound), errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, nil)
	case errors.Is(err, ErrNoActive):
		httpx.WriteError(w, http.StatusServiceUnavailable, CodeNoCatalog, nil)
	default:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
	}
}

func (h handlers) reportList(w http.ResponseWriter, r *http.Request) {
	limit, ok := intParam(w, r, "limit", 20, 1, 200)
	if !ok {
		return
	}
	items, err := h.reports.Reports(r.Context(), limit)
	if err != nil {
		fail(w, err)
		return
	}
	if items == nil {
		items = []app.Record{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) reportOne(w http.ResponseWriter, r *http.Request) {
	rec, err := h.reports.Report(r.Context(), r.PathValue("build"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rec)
}

func (h handlers) summary(w http.ResponseWriter, r *http.Request) {
	rec, active, err := h.reports.Active(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if !active {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	sum, err := h.ix.Summary(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"active": true, "build_id": rec.BuildID, "checked_at": rec.CheckedAt, "counts": sum,
	})
}

func (h handlers) stars(w http.ResponseWriter, r *http.Request) {
	limit, ok := intParam(w, r, "limit", 25, 1, 200)
	if !ok {
		return
	}
	items, err := h.ix.SearchStars(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) constellations(w http.ResponseWriter, r *http.Request) {
	items, err := h.ix.Constellations(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) cultures(w http.ResponseWriter, r *http.Request) {
	items, err := h.ix.Cultures(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) figures(w http.ResponseWriter, r *http.Request) {
	items, err := h.ix.CultureFigures(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) figure(w http.ResponseWriter, r *http.Request) {
	f, err := h.ix.FigureLines(r.Context(), r.PathValue("id"), r.PathValue("figure"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, f)
}

// intParam reads an optional integer query parameter within [lo, hi].
func intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, map[string]any{"field": name})
		return 0, false
	}
	return n, true
}
