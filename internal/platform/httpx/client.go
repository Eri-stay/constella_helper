package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// ErrCircuitOpen is returned without a network call while the breaker for the
// target host is open.
var ErrCircuitOpen = errors.New("circuit breaker open")

// StatusError is a non-2xx answer from another service.
type StatusError struct {
	Status int
	Body   ErrorBody
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("upstream status %d (%s)", e.Status, e.Body.Code)
}

// ClientOptions configures a Client. Zero values get defaults.
type ClientOptions struct {
	Timeout time.Duration // per attempt; default 3s
	Retries int           // extra attempts for retryable failures; default 0
	Backoff time.Duration // first retry delay, doubled each time; default 200ms
	Token   string        // bearer token sent to other services
	Log     *slog.Logger
	// TripAfter consecutive failures opens the breaker; default 3.
	TripAfter uint32
	// OpenFor is how long the breaker stays open before a probe; default 10s.
	OpenFor time.Duration
}

// Client calls other services with tracing, a per-attempt timeout, retries
// for idempotent requests, and one circuit breaker per target host.
type Client struct {
	opts ClientOptions
	http *http.Client

	mu       sync.Mutex
	breakers map[string]*gobreaker.CircuitBreaker[*http.Response]
}

// NewClient returns a client with defaults applied.
func NewClient(o ClientOptions) *Client {
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Second
	}
	if o.Backoff == 0 {
		o.Backoff = 200 * time.Millisecond
	}
	if o.TripAfter == 0 {
		o.TripAfter = 3
	}
	if o.OpenFor == 0 {
		o.OpenFor = 10 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Client{
		opts:     o,
		http:     &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)},
		breakers: map[string]*gobreaker.CircuitBreaker[*http.Response]{},
	}
}

// Request is one call. Body and Out are JSON; either may be nil.
type Request struct {
	Method string
	URL    string
	Body   any
	Out    any
	// IdempotencyKey is sent as a header and allows retrying a POST.
	IdempotencyKey string
}

// Do performs r. Network errors, 5xx and timeouts are retried when the
// request is idempotent (GET, PUT, DELETE, or an idempotency key is set).
func (c *Client) Do(ctx context.Context, r Request) error {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = json.Marshal(r.Body); err != nil {
			return fmt.Errorf("marshal request to %s: %w", r.URL, err)
		}
	}
	attempts := 1
	if r.retryable() {
		attempts += c.opts.Retries
	}
	delay := c.opts.Backoff
	var err error
	for i := range attempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(delay):
			}
			delay *= 2
		}
		err = c.attempt(ctx, r, body)
		if err == nil || !transient(err) {
			return err
		}
	}
	return err
}

func (r Request) retryable() bool {
	switch r.Method {
	case http.MethodGet, http.MethodPut, http.MethodDelete:
		return true
	}
	return r.IdempotencyKey != ""
}

// transient reports whether a retry may succeed (not for 4xx or an open breaker).
func transient(err error) bool {
	if errors.Is(err, ErrCircuitOpen) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status >= 500
	}
	return true
}

func (c *Client) attempt(ctx context.Context, r Request, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request %s %s: %w", r.Method, r.URL, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.opts.Token)
	}
	if r.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", r.IdempotencyKey)
	}

	resp, err := c.breaker(req.URL).Execute(func() (*http.Response, error) {
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 500 {
			return nil, readStatusError(resp)
		}
		return resp, nil
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return fmt.Errorf("%s %s: %w", r.Method, r.URL, ErrCircuitOpen)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", r.Method, r.URL, err)
	}
	defer resp.Body.Close() //nolint:errcheck // body fully read below

	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s: %w", r.Method, r.URL, readStatusError(resp))
	}
	if r.Out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(r.Out); err != nil {
		return fmt.Errorf("decode response of %s %s: %w", r.Method, r.URL, err)
	}
	return nil
}

func readStatusError(resp *http.Response) *StatusError {
	defer resp.Body.Close() //nolint:errcheck // error body is best effort
	se := &StatusError{Status: resp.StatusCode}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody)) // best effort: the status already says enough
	if json.Unmarshal(data, &se.Body) != nil || se.Body.Code == "" {
		se.Body.Code = http.StatusText(resp.StatusCode)
	}
	return se
}

// breaker returns the breaker for the URL's host, creating it on first use.
func (c *Client) breaker(u *url.URL) *gobreaker.CircuitBreaker[*http.Response] {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.breakers[u.Host]; ok {
		return b
	}
	b := gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
		Name:        u.Host,
		MaxRequests: 1,
		Timeout:     c.opts.OpenFor,
		ReadyToTrip: func(cnt gobreaker.Counts) bool { return cnt.ConsecutiveFailures >= c.opts.TripAfter },
		OnStateChange: func(name string, from, to gobreaker.State) {
			c.opts.Log.Warn("circuit breaker state changed", "target", name, "from", from.String(), "to", to.String())
		},
	})
	c.breakers[u.Host] = b
	return b
}

// BreakerStates reports the state of every breaker by host (for debug views).
func (c *Client) BreakerStates() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.breakers))
	for host, b := range c.breakers {
		out[host] = b.State().String()
	}
	return out
}
