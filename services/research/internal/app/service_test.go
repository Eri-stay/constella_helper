package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"constella/internal/domain/genconst"
	"constella/internal/platform/events"
)

type memStore struct {
	mu     sync.Mutex
	sweeps map[string]Sweep
	runs   map[string][]Run
	events []events.Envelope
}

func newMemStore() *memStore { return &memStore{sweeps: map[string]Sweep{}, runs: map[string][]Run{}} }

func (m *memStore) CreateSweep(_ context.Context, s Sweep) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweeps[s.ID] = s
	return nil
}

func (m *memStore) AddRun(_ context.Context, id string, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[id] = append(m.runs[id], r)
	s := m.sweeps[id]
	s.Done++
	m.sweeps[id] = s
	return nil
}

func (m *memStore) FinishSweep(_ context.Context, s Sweep, ev events.Envelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.Done = m.sweeps[s.ID].Done
	m.sweeps[s.ID] = s
	m.events = append(m.events, ev)
	return nil
}

func (m *memStore) Get(_ context.Context, id string) (Sweep, []Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sweeps[id]
	if !ok {
		return Sweep{}, nil, ErrNotFound
	}
	return s, append([]Run(nil), m.runs[id]...), nil
}

func (m *memStore) List(context.Context, int) ([]Sweep, error) { return nil, nil }

func (m *memStore) Running(context.Context) ([]Sweep, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Sweep
	for _, s := range m.sweeps {
		if s.Status == StatusRunning {
			out = append(out, s)
		}
	}
	return out, nil
}

// fakeCatalog serves a reproducible sky; the culture is what the base
// strategy draws, so base must score a perfect Jaccard against it.
type fakeCatalog struct {
	stars  []genconst.Star
	culmin genconst.Params
	err    error
}

func newFakeCatalog() *fakeCatalog {
	rng := rand.New(rand.NewPCG(5, 5))
	f := &fakeCatalog{culmin: genconst.Params{Strategy: genconst.Base, EpsDeg: 10}}
	for i := range 250 {
		f.stars = append(f.stars, genconst.Star{HIP: int64(i + 1), RA: rng.Float64() * 360, Dec: rng.Float64()*150 - 75, Mag: rng.Float64() * 5})
	}
	return f
}

func (f *fakeCatalog) ActiveBuild(context.Context) (string, error) { return "b1", f.err }

func (f *fakeCatalog) Stars(_ context.Context, _ string, magMax float64) ([]genconst.Star, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []genconst.Star
	for _, s := range f.stars {
		if s.Mag <= magMax {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeCatalog) Edges(_ context.Context, _, culture string) ([]genconst.Edge, error) {
	if culture == "nope" {
		return nil, ErrNoCulture
	}
	r, err := genconst.Generate(f.stars, f.culmin)
	return r.Edges, err
}

type noNotify struct{}

func (noNotify) Notify() {}

func newService(st Store, c Catalog) *Service {
	return New(st, c, noNotify{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func wait(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s.Wait(ctx)
	if ctx.Err() != nil {
		t.Fatal("the sweep did not finish")
	}
}

func TestSweepRunsAllCombinationsAndComparesWithTheCulture(t *testing.T) {
	t.Parallel()
	st := newMemStore()
	s := newService(st, newFakeCatalog())
	sw, err := s.Start(context.Background(), Spec{
		Culture: "demo", Strategies: []string{genconst.Base, genconst.Cycles}, EpsList: []float64{8, 10}, Seeds: []uint64{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sw.Total != 8 || sw.BuildID != "b1" {
		t.Fatalf("sweep: %+v", sw)
	}
	wait(t, s)

	got, runs, _ := s.Get(context.Background(), sw.ID)
	if got.Status != StatusCompleted || got.Done != 8 || len(runs) != 8 {
		t.Fatalf("sweep %+v, %d runs", got, len(runs))
	}
	for _, r := range runs {
		perfect := r.Params.Strategy == genconst.Base && r.Params.EpsDeg == 10
		if perfect && r.Metrics.Jaccard != 1 {
			t.Errorf("base eps 10 is the reference itself, Jaccard = %v", r.Metrics.Jaccard)
		}
		if !perfect && r.Metrics.Jaccard >= 1 {
			t.Errorf("%s eps %v should differ from the reference", r.Params.Strategy, r.Params.EpsDeg)
		}
		if r.Hash == "" || r.Edges == 0 {
			t.Errorf("run %d is empty: %+v", r.Seq, r)
		}
	}
	if len(st.events) != 1 || st.events[0].Type != events.ResearchRunCompleted {
		t.Fatalf("events: %+v", st.events)
	}
	var data events.ResearchCompletedData
	if err := st.events[0].Decode(&data); err != nil || data.Runs != 8 || data.BestStrategy != genconst.Base || data.BestJaccard != 1 {
		t.Errorf("completed event: %+v (err %v)", data, err)
	}
}

func TestSweepFailures(t *testing.T) {
	t.Parallel()
	t.Run("unknown culture", func(t *testing.T) {
		t.Parallel()
		st := newMemStore()
		s := newService(st, newFakeCatalog())
		sw, _ := s.Start(context.Background(), Spec{Culture: "nope", Strategies: []string{genconst.Base}})
		wait(t, s)
		got, _, _ := s.Get(context.Background(), sw.ID)
		if got.Status != StatusFailed || got.ErrorCode != CodeNoCulture || st.events[0].Type != events.ResearchRunFailed {
			t.Errorf("sweep %+v events %v", got, st.events)
		}
	})
	t.Run("catalog down at start", func(t *testing.T) {
		t.Parallel()
		f := newFakeCatalog()
		f.err = errors.New("connection refused")
		_, err := newService(newMemStore(), f).Start(context.Background(), Spec{Culture: "demo"})
		if !IsCatalogError(err) {
			t.Errorf("err = %v, want a catalog error", err)
		}
	})
}

func TestSpecValidation(t *testing.T) {
	t.Parallel()
	s := newService(newMemStore(), newFakeCatalog())
	bad := []Spec{
		{},
		{Culture: "demo", Strategies: []string{"voronoi"}},
		{Culture: "demo", EpsList: []float64{500}},
		{Culture: "demo", EpsList: make([]float64, 50), Seeds: make([]uint64, 50)}, // 5×50×50 runs
	}
	bad[3].EpsList[0] = 5
	for i := range bad[3].EpsList {
		bad[3].EpsList[i] = float64(i%50 + 1)
		bad[3].Seeds[i] = uint64(i)
	}
	for i, spec := range bad {
		if _, err := s.Start(context.Background(), spec); !errors.Is(err, ErrInvalid) {
			t.Errorf("spec %d accepted: %v", i, err)
		}
	}
}

func TestRecoverFailsInterruptedSweeps(t *testing.T) {
	t.Parallel()
	st := newMemStore()
	st.sweeps["old"] = Sweep{ID: "old", Status: StatusRunning}
	s := newService(st, newFakeCatalog())
	if err := s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := st.sweeps["old"]; got.Status != StatusFailed || got.ErrorCode != CodeInterrupted {
		t.Errorf("sweep %+v", got)
	}
}

// The determinism check of the lab: the same seed twice gives one hash, in
// every strategy, even with jitter.
func TestVerifyDeterminism(t *testing.T) {
	t.Parallel()
	s := newService(newMemStore(), newFakeCatalog())
	for _, strategy := range genconst.Strategies {
		v, err := s.Verify(context.Background(), Spec{Culture: "demo", Strategies: []string{strategy}, Seeds: []uint64{42},
			Params: genconst.Params{JitterDeg: 0.5}})
		if err != nil || !v.Equal || v.Hash1 == "" || v.Edges == 0 {
			t.Errorf("%s: %+v (err %v)", strategy, v, err)
		}
	}
}
