package adapters

import (
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"

	"constella/internal/domain/genconst"
	"constella/internal/platform/httpx"
	"constella/services/research/internal/app"
)

// Sweeps is what the handlers need from the use cases.
type Sweeps interface {
	Start(ctx context.Context, spec app.Spec) (app.Sweep, error)
	Get(ctx context.Context, id string) (app.Sweep, []app.Run, error)
	List(ctx context.Context, limit int) ([]app.Sweep, error)
	Verify(ctx context.Context, spec app.Spec) (app.Verification, error)
}

// Routes registers the research REST API on mux.
//
//	POST /v1/sweeps            202 {sweep}  strategies × eps × seeds, in the background
//	GET  /v1/sweeps            ?limit=
//	GET  /v1/sweeps/{id}       sweep with its runs
//	GET  /v1/sweeps/{id}/csv   the runs as CSV (for the thesis tables)
//	POST /v1/verify            same parameters twice: the hashes must be equal
//	GET  /v1/strategies        strategy names and the default parameters
func Routes(mux *http.ServeMux, svc Sweeps) {
	h := handlers{svc: svc}
	mux.HandleFunc("POST /v1/sweeps", h.start)
	mux.HandleFunc("GET /v1/sweeps", h.list)
	mux.HandleFunc("GET /v1/sweeps/{id}", h.get)
	mux.HandleFunc("GET /v1/sweeps/{id}/csv", h.csv)
	mux.HandleFunc("POST /v1/verify", h.verify)
	mux.HandleFunc("GET /v1/strategies", h.strategies)
}

type handlers struct{ svc Sweeps }

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrInvalid), errors.Is(err, genconst.ErrParams):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, map[string]any{"reason": err.Error()})
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, nil)
	case app.IsCatalogError(err):
		httpx.WriteError(w, http.StatusBadGateway, httpx.CodeUnavailable, map[string]any{"service": "catalog"})
	default:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
	}
}

func (h handlers) start(w http.ResponseWriter, r *http.Request) {
	var spec app.Spec
	if err := httpx.DecodeJSON(r, &spec); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, nil)
		return
	}
	sw, err := h.svc.Start(r.Context(), spec)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Location", "/v1/sweeps/"+sw.ID)
	httpx.WriteJSON(w, http.StatusAccepted, sw)
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
	items, err := h.svc.List(r.Context(), n)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	sw, runs, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sweep": sw, "runs": runs})
}

func (h handlers) verify(w http.ResponseWriter, r *http.Request) {
	var spec app.Spec
	if err := httpx.DecodeJSON(r, &spec); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, nil)
		return
	}
	v, err := h.svc.Verify(r.Context(), spec)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h handlers) strategies(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"strategies": genconst.Strategies,
		"defaults":   genconst.Params{}.WithDefaults(),
		"max_runs":   app.MaxRuns,
	})
}

func (h handlers) csv(w http.ResponseWriter, r *http.Request) {
	sw, runs, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sw.ID+`.csv"`)
	_ = WriteCSV(w, sw, runs) // a write error means the client left
}

// WriteCSV writes one row per run, with the parameters that matter for the thesis tables.
func WriteCSV(w interface{ Write([]byte) (int, error) }, sw app.Sweep, runs []app.Run) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"sweep_id", "build_id", "culture", "strategy", "eps_deg", "seed", "jitter_deg", "mag_max",
		"stars", "edges", "clusters", "reference_edges", "common_edges", "jaccard", "precision", "recall", "hash", "duration_ms"})
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, r := range runs {
		m := r.Metrics
		_ = cw.Write([]string{sw.ID, sw.BuildID, sw.Culture, r.Params.Strategy, f(r.Params.EpsDeg),
			strconv.FormatUint(r.Params.Seed, 10), f(r.Params.JitterDeg), f(r.Params.MagMax),
			strconv.Itoa(r.Stars), strconv.Itoa(r.Edges), strconv.Itoa(r.Clusters),
			strconv.Itoa(m.Reference), strconv.Itoa(m.Common),
			strconv.FormatFloat(m.Jaccard, 'f', 4, 64), strconv.FormatFloat(m.Precision, 'f', 4, 64),
			strconv.FormatFloat(m.Recall, 'f', 4, 64), r.Hash, f(r.DurationMS)})
	}
	cw.Flush()
	return cw.Error()
}
