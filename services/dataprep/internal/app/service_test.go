package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"constella/internal/domain/catalog"
	"constella/internal/platform/events"
)

type memStore struct {
	mu     sync.Mutex
	builds map[string]Build
	events []events.Envelope
}

func newMemStore() *memStore { return &memStore{builds: map[string]Build{}} }

func (m *memStore) Create(_ context.Context, b Build, ev events.Envelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.builds {
		if x.Status == StatusRunning {
			return ErrBusy
		}
	}
	m.builds[b.ID] = b
	m.events = append(m.events, ev)
	return nil
}

func (m *memStore) Finish(_ context.Context, b Build, ev events.Envelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builds[b.ID] = b
	m.events = append(m.events, ev)
	return nil
}

func (m *memStore) Get(_ context.Context, id string) (Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.builds[id]
	if !ok {
		return Build{}, ErrNotFound
	}
	return b, nil
}

func (m *memStore) List(context.Context, int) ([]Build, error) { return nil, nil }

func (m *memStore) Running(context.Context) ([]Build, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Build
	for _, b := range m.builds {
		if b.Status == StatusRunning {
			out = append(out, b)
		}
	}
	return out, nil
}

func (m *memStore) eventTypes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.events {
		out = append(out, e.Type)
	}
	return out
}

type fakeSources struct {
	err  error
	gate chan struct{} // when set, Load waits for it to be closed
}

func (f fakeSources) Load(ctx context.Context) (Input, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return Input{}, ctx.Err()
		}
	}
	if f.err != nil {
		return Input{}, f.err
	}
	return Input{
		Stars:    []catalog.Star{{ID: 1, HIP: 100, Con: "Ori", Mag: 1}, {ID: 2, HIP: 200, Con: "Ori", Mag: 2}},
		Cultures: []catalog.Culture{{ID: "c", Constellations: []catalog.CultureConstellation{{ID: "x", Polylines: [][]int64{{100, 200}}}}}},
	}, nil
}

type fakeWriter struct{ err error }

func (f fakeWriter) Write(_ context.Context, name string, _ catalog.Dataset) (Artifact, error) {
	if f.err != nil {
		return Artifact{}, f.err
	}
	return Artifact{File: name, SHA256: "abc", SizeBytes: 42}, nil
}

type noNotify struct{}

func (noNotify) Notify() {}

func newService(st Store, src Sources, w Writer) *Service {
	return New(st, src, w, noNotify{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func waitDone(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Wait(ctx)
	if ctx.Err() != nil {
		t.Fatal("build did not finish in time")
	}
}

func TestBuildCompletes(t *testing.T) {
	t.Parallel()
	st := newMemStore()
	s := newService(st, fakeSources{}, fakeWriter{})
	b, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, s)

	got, err := s.Get(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCompleted || got.SHA256 != "abc" || got.Stars != 2 || got.Edges != 1 || got.Artifact != "cosmos-"+b.ID+".db" {
		t.Errorf("unexpected build: %+v", got)
	}
	want := []string{events.BuildStarted, events.BuildCompleted}
	if types := st.eventTypes(); len(types) != 2 || types[0] != want[0] || types[1] != want[1] {
		t.Errorf("events = %v, want %v", types, want)
	}
}

func TestBuildFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  Sources
		w    Writer
		code string
	}{
		{"source missing", fakeSources{err: errors.New("no file")}, fakeWriter{}, CodeSourceUnavailable},
		{"write fails", fakeSources{}, fakeWriter{err: errors.New("disk full")}, CodeWriteFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newMemStore()
			s := newService(st, tc.src, tc.w)
			b, err := s.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			waitDone(t, s)
			got, _ := s.Get(context.Background(), b.ID)
			if got.Status != StatusFailed || got.ErrorCode != tc.code {
				t.Errorf("build = %+v, want failed with %s", got, tc.code)
			}
			if types := st.eventTypes(); types[len(types)-1] != events.BuildFailed {
				t.Errorf("last event = %v, want %s", types, events.BuildFailed)
			}
		})
	}
}

func TestOnlyOneBuildAtATime(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	s := newService(newMemStore(), fakeSources{gate: gate}, fakeWriter{})
	if _, err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start error = %v, want ErrBusy", err)
	}
	close(gate)
	waitDone(t, s)
	if _, err := s.Start(context.Background()); err != nil {
		t.Errorf("Start after the first build finished: %v", err)
	}
	waitDone(t, s)
}

func TestRecoverFailsInterruptedBuilds(t *testing.T) {
	t.Parallel()
	st := newMemStore()
	st.builds["old"] = Build{ID: "old", Status: StatusRunning, StartedAt: time.Now()}
	s := newService(st, fakeSources{}, fakeWriter{})
	if err := s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "old")
	if got.Status != StatusFailed || got.ErrorCode != CodeInterrupted {
		t.Errorf("build = %+v, want failed with %s", got, CodeInterrupted)
	}
	if _, err := s.Start(context.Background()); err != nil {
		t.Errorf("Start after Recover: %v", err)
	}
	waitDone(t, s)
}
