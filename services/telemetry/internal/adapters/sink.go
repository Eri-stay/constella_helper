package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"constella/services/telemetry/internal/app"
)

// NoopSink drops everything: the default, so nothing leaves the machine.
type NoopSink struct{}

// Name implements app.ErrorSink.
func (NoopSink) Name() string { return "noop" }

// Capture implements app.ErrorSink.
func (NoopSink) Capture(context.Context, app.ClientLog) error { return nil }

// SentrySink sends errors to Sentry with its store API. It is created only
// when SENTRY_DSN is set.
type SentrySink struct {
	endpoint string
	auth     string
	http     *http.Client
}

// NewSentrySink parses a DSN such as https://<key>@o1.ingest.sentry.io/<project>.
func NewSentrySink(dsn string) (*SentrySink, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || u.Host == "" {
		return nil, fmt.Errorf("sentry dsn is not of the form https://key@host/project")
	}
	project := strings.Trim(u.Path, "/")
	if project == "" {
		return nil, fmt.Errorf("sentry dsn has no project id")
	}
	return &SentrySink{
		endpoint: fmt.Sprintf("%s://%s/api/%s/store/", u.Scheme, u.Host, project),
		auth:     "Sentry sentry_version=7, sentry_client=constella-telemetry/1, sentry_key=" + u.User.Username(),
		http:     &http.Client{Timeout: 3 * time.Second},
	}, nil
}

// Name implements app.ErrorSink.
func (*SentrySink) Name() string { return "sentry" }

// Capture implements app.ErrorSink. The message is already scrubbed.
func (s *SentrySink) Capture(ctx context.Context, l app.ClientLog) error {
	level := l.Level
	if level == "warn" {
		level = "warning"
	}
	body, err := json.Marshal(map[string]any{
		"message": l.Message, "level": level, "platform": "other",
		"timestamp": l.At.Unix(), "release": l.AppVersion,
		"user":  map[string]string{"id": l.Installation},
		"extra": map[string]string{"stack": l.Stack},
	})
	if err != nil {
		return fmt.Errorf("marshal sentry event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build sentry request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sentry-Auth", s.auth)
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("send to sentry: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body is discarded
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sentry answered %d", resp.StatusCode)
	}
	return nil
}
