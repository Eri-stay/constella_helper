package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"constella/internal/platform/httpx"
	"constella/services/telemetry/internal/app"
)

// Routes registers the telemetry REST API on mux.
//
//	POST /v1/audit        201 {id} | 200 {id} for a repeated Idempotency-Key
//	GET  /v1/audit        ?limit=&type= (prefix)
//	POST /v1/client-log   201 {log}   paths are scrubbed of the user name
//	GET  /v1/client-log   ?limit=
//	GET  /v1/summary
func Routes(mux *http.ServeMux, svc *app.Service) {
	h := handlers{svc: svc}
	mux.HandleFunc("POST /v1/audit", h.addAudit)
	mux.HandleFunc("GET /v1/audit", h.audit)
	mux.HandleFunc("POST /v1/client-log", h.addClientLog)
	mux.HandleFunc("GET /v1/client-log", h.clientLogs)
	mux.HandleFunc("GET /v1/summary", h.summary)
}

type handlers struct{ svc *app.Service }

func internal(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, nil)
}

func (h handlers) addAudit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source        string          `json:"source"`
		Type          string          `json:"type"`
		CorrelationID string          `json:"correlation_id"`
		Data          json.RawMessage `json:"data"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, nil)
		return
	}
	id, added, err := h.svc.RecordAudit(r.Context(),
		app.AuditInput{Source: in.Source, Type: in.Type, CorrelationID: in.CorrelationID, Data: in.Data},
		r.Header.Get("Idempotency-Key"))
	switch {
	case errors.Is(err, app.ErrInvalid):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, nil)
	case err != nil:
		internal(w)
	case added:
		httpx.WriteJSON(w, http.StatusCreated, map[string]int64{"id": id})
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]int64{"id": id})
	}
}

func limit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 50, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 500 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, map[string]any{"field": "limit"})
		return 0, false
	}
	return n, true
}

func (h handlers) audit(w http.ResponseWriter, r *http.Request) {
	n, ok := limit(w, r)
	if !ok {
		return
	}
	items, err := h.svc.Audit(r.Context(), n, r.URL.Query().Get("type"))
	if err != nil {
		internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) addClientLog(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Installation string `json:"installation"`
		AppVersion   string `json:"app_version"`
		Level        string `json:"level"`
		Message      string `json:"message"`
		Stack        string `json:"stack"`
	}
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, nil)
		return
	}
	l, err := h.svc.AddClientLog(r.Context(), app.ClientLogInput{
		Installation: in.Installation, AppVersion: in.AppVersion, Level: in.Level, Message: in.Message, Stack: in.Stack})
	switch {
	case errors.Is(err, app.ErrInvalid):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, nil)
	case err != nil:
		internal(w)
	default:
		httpx.WriteJSON(w, http.StatusCreated, l)
	}
}

func (h handlers) clientLogs(w http.ResponseWriter, r *http.Request) {
	n, ok := limit(w, r)
	if !ok {
		return
	}
	items, err := h.svc.ClientLogs(r.Context(), n)
	if err != nil {
		internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h handlers) summary(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.Summary(r.Context())
	if err != nil {
		internal(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}
