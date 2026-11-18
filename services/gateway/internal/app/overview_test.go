package app

import (
	"context"
	"errors"
	"testing"
)

func TestOverviewDegradesWhenAServiceIsDown(t *testing.T) {
	t.Parallel()
	ups := []Upstream{
		{Name: "dataprep", Fetch: func(context.Context) (any, error) { return nil, errors.New("connection refused") }},
		{Name: "catalog", Fetch: func(context.Context) (any, error) { return map[string]any{"active": true}, nil }},
	}
	parts := Overview(context.Background(), ups, func(error) string { return "UNREACHABLE" })

	if len(parts) != 2 || parts[0].Name != "catalog" || parts[1].Name != "dataprep" {
		t.Fatalf("parts are not sorted by name: %+v", parts)
	}
	if parts[0].Status != "ok" || parts[0].Data == nil {
		t.Errorf("catalog: %+v", parts[0])
	}
	if parts[1].Status != "down" || parts[1].Error != "UNREACHABLE" || parts[1].Data != nil {
		t.Errorf("dataprep: %+v", parts[1])
	}
}

func TestOverviewAsksInParallel(t *testing.T) {
	t.Parallel()
	started := make(chan string, 2)
	release := make(chan struct{})
	fetch := func(name string) func(context.Context) (any, error) {
		return func(context.Context) (any, error) {
			started <- name
			<-release // both must be running at once, or this deadlocks
			return name, nil
		}
	}
	done := make(chan []Part)
	go func() {
		done <- Overview(context.Background(), []Upstream{{"a", fetch("a")}, {"b", fetch("b")}}, nil)
	}()
	<-started
	<-started
	close(release)
	if parts := <-done; len(parts) != 2 {
		t.Errorf("parts = %+v", parts)
	}
}
