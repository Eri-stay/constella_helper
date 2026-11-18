// Package app holds the gateway use cases. The gateway has no data of its own;
// its one use case is API composition: one call that gathers a view from
// several services and degrades when some of them are down.
package app

import (
	"context"
	"sort"
	"sync"
)

// Upstream is a service the overview asks.
type Upstream struct {
	Name string
	// Fetch returns the service's JSON summary.
	Fetch func(ctx context.Context) (any, error)
}

// Part is one service's contribution to the overview.
type Part struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok" or "down"
	Data   any    `json:"data,omitempty"`
	Error  string `json:"error,omitempty"` // an error code, never text
}

// Overview asks all upstreams in parallel. A failing upstream is reported as
// "down" and does not fail the whole call.
func Overview(ctx context.Context, ups []Upstream, errorCode func(error) string) []Part {
	parts := make([]Part, len(ups))
	var wg sync.WaitGroup
	for i, u := range ups {
		wg.Go(func() {
			data, err := u.Fetch(ctx)
			if err != nil {
				parts[i] = Part{Name: u.Name, Status: "down", Error: errorCode(err)}
				return
			}
			parts[i] = Part{Name: u.Name, Status: "ok", Data: data}
		})
	}
	wg.Wait()
	sort.SliceStable(parts, func(a, b int) bool { return parts[a].Name < parts[b].Name })
	return parts
}
