// Package httpx holds the HTTP plumbing shared by all services: middleware,
// JSON helpers, graceful serving, and a resilient client (timeout, retry,
// circuit breaker) with trace propagation.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Error codes shared by all services. Services never send user-facing text,
// only a code and parameters; the UI translates (same rule as the product).
const (
	CodeBadRequest   = "BAD_REQUEST"
	CodeUnauthorized = "UNAUTHORIZED"
	CodeNotFound     = "NOT_FOUND"
	CodeConflict     = "CONFLICT"
	CodeInternal     = "INTERNAL"
	CodeUnavailable  = "UPSTREAM_UNAVAILABLE"
)

// maxBody limits request bodies; the platform only exchanges small JSON.
const maxBody = 1 << 20

// ErrorBody is the JSON shape of every error response.
type ErrorBody struct {
	Code   string         `json:"code"`
	Params map[string]any `json:"params,omitempty"`
}

// WriteJSON writes v with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent; an encoding error can only be a
	// broken connection, which the client sees anyway.
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes an ErrorBody.
func WriteError(w http.ResponseWriter, status int, code string, params map[string]any) {
	WriteJSON(w, status, ErrorBody{Code: code, Params: params})
}

// DecodeJSON reads a JSON request body into v and rejects unknown fields.
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	if dec.More() {
		return errors.New("decode request body: trailing data")
	}
	return nil
}
