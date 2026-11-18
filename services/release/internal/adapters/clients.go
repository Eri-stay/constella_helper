package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"constella/internal/platform/httpx"
	"constella/services/release/internal/app"
)

// CatalogClient calls the catalog reservation API. Every call carries an
// idempotency key (the release id), so the client may retry.
type CatalogClient struct {
	Client  *httpx.Client
	BaseURL string
}

func (c CatalogClient) call(ctx context.Context, method, buildID, releaseID, key string, out any) error {
	u := c.BaseURL + "/v1/builds/" + url.PathEscape(buildID)
	var body any
	switch method {
	case http.MethodDelete:
		u += "/reserve?release_id=" + url.QueryEscape(releaseID)
	case http.MethodPost:
		if key == "release" {
			u += "/release"
		} else {
			u += "/reserve"
		}
		body = map[string]string{"release_id": releaseID}
	}
	err := c.Client.Do(ctx, httpx.Request{Method: method, URL: u, Body: body, Out: out, IdempotencyKey: key + ":" + releaseID})
	return translate(err)
}

// translate turns a 4xx answer into a RefusedError the saga understands.
func translate(err error) error {
	var se *httpx.StatusError
	if errors.As(err, &se) && se.Status < 500 {
		reason, _ := se.Body.Params["reason"].(string)
		if reason == "" {
			reason = se.Body.Code
		}
		return &app.RefusedError{Code: reason}
	}
	return err
}

// Reserve implements app.Catalog.
func (c CatalogClient) Reserve(ctx context.Context, buildID, releaseID string) (app.Reserved, error) {
	var out struct {
		Artifact  string `json:"artifact"`
		SHA256    string `json:"sha256"`
		SizeBytes int64  `json:"size_bytes"`
	}
	if err := c.call(ctx, http.MethodPost, buildID, releaseID, "reserve", &out); err != nil {
		return app.Reserved{}, err
	}
	return app.Reserved{Artifact: out.Artifact, SHA256: out.SHA256, SizeBytes: out.SizeBytes}, nil
}

// MarkReleased implements app.Catalog.
func (c CatalogClient) MarkReleased(ctx context.Context, buildID, releaseID string) error {
	return c.call(ctx, http.MethodPost, buildID, releaseID, "release", nil)
}

// Cancel implements app.Catalog.
func (c CatalogClient) Cancel(ctx context.Context, buildID, releaseID string) error {
	return c.call(ctx, http.MethodDelete, buildID, releaseID, "cancel", nil)
}

// AuditClient posts audit entries to telemetry.
type AuditClient struct {
	Client  *httpx.Client
	BaseURL string
}

// Record implements app.Audit. The key makes a retried call store one entry.
func (a AuditClient) Record(ctx context.Context, releaseID, eventType string, data any) error {
	return a.Client.Do(ctx, httpx.Request{
		Method: http.MethodPost, URL: a.BaseURL + "/v1/audit", IdempotencyKey: "audit:" + eventType + ":" + releaseID,
		Body: map[string]any{"source": "release", "type": eventType, "correlation_id": releaseID, "data": data},
	})
}
