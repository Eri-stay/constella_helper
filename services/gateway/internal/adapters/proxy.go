// Package adapters connects the gateway to HTTP clients, the broker and the
// embedded UI.
package adapters

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"constella/internal/platform/httpx"
)

// Proxy routes /api/{service}/{rest} to the service:
//
//	/api/dataprep/builds        → dataprep  /v1/builds
//	/api/catalog/stars?q=x      → catalog   /v1/stars?q=x
//	/api/catalog/debug/fault    → catalog   /debug/fault
//
// The caller's bearer token is replaced by the internal one, and the trace
// context travels on (the transport is instrumented).
type Proxy struct {
	targets map[string]*url.URL
	token   string
	rp      *httputil.ReverseProxy
}

// NewProxy returns a proxy for the given services (name → base URL).
func NewProxy(targets map[string]*url.URL, token string) *Proxy {
	p := &Proxy{targets: targets, token: token}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	p.rp = &httputil.ReverseProxy{
		Rewrite:   p.rewrite,
		Transport: otelhttp.NewTransport(transport),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			httpx.WriteError(w, http.StatusBadGateway, httpx.CodeUnavailable,
				map[string]any{"service": r.PathValue("service")})
		},
	}
	return p
}

// Register mounts the proxy on mux.
func (p *Proxy) Register(mux *http.ServeMux) {
	mux.Handle("/api/{service}/{rest...}", p)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.targets[r.PathValue("service")]; !ok {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, map[string]any{"service": r.PathValue("service")})
		return
	}
	p.rp.ServeHTTP(w, r)
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	target := p.targets[pr.In.PathValue("service")]
	pr.SetURL(target)
	rest := pr.In.PathValue("rest")
	if rest == "debug" || strings.HasPrefix(rest, "debug/") {
		pr.Out.URL.Path = "/" + rest
	} else {
		pr.Out.URL.Path = "/v1/" + rest
	}
	pr.Out.URL.RawPath = ""

	q := pr.Out.URL.Query()
	q.Del("token") // the UI token never travels to services
	pr.Out.URL.RawQuery = q.Encode()

	pr.Out.Header.Del("Authorization")
	if p.token != "" {
		pr.Out.Header.Set("Authorization", "Bearer "+p.token)
	}
}

// NewUpdates serves /updates/* (version.json and data files) from the release
// service. The path is kept; the internal token is added.
func NewUpdates(target *url.URL, token string) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Del("Authorization")
			if token != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+token)
			}
		},
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			httpx.WriteError(w, http.StatusBadGateway, httpx.CodeUnavailable, map[string]any{"service": "release"})
		},
	}
	return rp
}
