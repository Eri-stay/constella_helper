package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"constella/internal/domain/release"
)

type memStore struct {
	mu      sync.Mutex
	streams map[string][]release.Event
}

func (m *memStore) Append(_ context.Context, id string, rev int, evs []release.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.streams[id]) != rev {
		return errors.New("conflict")
	}
	m.streams[id] = append(m.streams[id], evs...)
	return nil
}

func (m *memStore) Load(_ context.Context, id string) ([]release.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]release.Event(nil), m.streams[id]...), nil
}

func (m *memStore) Recent(context.Context, int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.streams {
		ids = append(ids, id)
	}
	return ids, nil
}

type fakeCatalog struct {
	reserveErr, markErr error
	calls               []string
}

func (f *fakeCatalog) Reserve(context.Context, string, string) (Reserved, error) {
	f.calls = append(f.calls, "reserve")
	if f.reserveErr != nil {
		return Reserved{}, f.reserveErr
	}
	return Reserved{Artifact: "cosmos-b1.db", SHA256: "abc", SizeBytes: 7}, nil
}

func (f *fakeCatalog) MarkReleased(context.Context, string, string) error {
	f.calls = append(f.calls, "mark")
	return f.markErr
}

func (f *fakeCatalog) Cancel(context.Context, string, string) error {
	f.calls = append(f.calls, "cancel")
	return nil
}

type fakePublisher struct {
	manifest  []byte
	switchErr error
}

func (f *fakePublisher) Switch(context.Context, string, Reserved, time.Time) ([]byte, error) {
	if f.switchErr != nil {
		return nil, f.switchErr
	}
	prev := f.manifest
	f.manifest = []byte("new")
	return prev, nil
}

func (f *fakePublisher) Restore(_ context.Context, prev []byte) error { f.manifest = prev; return nil }
func (f *fakePublisher) Current(context.Context) ([]byte, error)       { return f.manifest, nil }

type fakeAudit struct {
	err   error
	calls int
}

func (f *fakeAudit) Record(context.Context, string, string, any) error {
	f.calls++
	return f.err
}

type rig struct {
	svc *Service
	st  *memStore
	cat *fakeCatalog
	pub *fakePublisher
	aud *fakeAudit
}

func newRig() *rig {
	r := &rig{st: &memStore{streams: map[string][]release.Event{}}, cat: &fakeCatalog{},
		pub: &fakePublisher{manifest: []byte("old")}, aud: &fakeAudit{}}
	r.svc = New(r.st, r.cat, r.pub, r.aud, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return r
}

func TestPublishClassicSucceeds(t *testing.T) {
	t.Parallel()
	r := newRig()
	rel, err := r.svc.PublishClassic(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Status != release.Published || string(r.pub.manifest) != "new" || r.aud.calls != 1 {
		t.Errorf("release %+v, manifest %q, audit calls %d", rel, r.pub.manifest, r.aud.calls)
	}
	if !reflect.DeepEqual(r.cat.calls, []string{"reserve", "mark"}) {
		t.Errorf("catalog calls %v", r.cat.calls)
	}
	// The stored events replay to the live state.
	replayed, _, err := r.svc.Get(context.Background(), rel.ID)
	if err != nil || !reflect.DeepEqual(replayed, rel) {
		t.Errorf("replay differs: %+v vs %+v (err %v)", replayed, rel, err)
	}
}

func TestPublishClassicCompensates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		arrange  func(*rig)
		step     string
		code     string
		manifest string
		calls    []string
	}{
		{"audit down: everything is undone", func(r *rig) { r.aud.err = errors.New("503") },
			release.StepAudit, CodeAuditDown, "old", []string{"reserve", "mark", "cancel"}},
		{"mark released fails", func(r *rig) { r.cat.markErr = errors.New("timeout") },
			release.StepMarkReleased, CodeCatalogDown, "old", []string{"reserve", "mark", "cancel"}},
		{"switch fails: only the reservation is undone", func(r *rig) { r.pub.switchErr = errors.New("disk full") },
			release.StepSwitch, CodeSwitchFailed, "old", []string{"reserve", "cancel"}},
		{"catalog refuses: the build is not validated", func(r *rig) { r.cat.reserveErr = &RefusedError{Code: "BUILD_NOT_VALIDATED"} },
			release.StepReserve, "BUILD_NOT_VALIDATED", "old", []string{"reserve", "cancel"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig()
			tc.arrange(r)
			rel, err := r.svc.PublishClassic(context.Background(), "b1")
			var se *StepError
			if !errors.As(err, &se) || se.Step != tc.step || se.Code != tc.code || !se.Compensated {
				t.Fatalf("error = %v, want step %s code %s compensated", err, tc.step, tc.code)
			}
			if rel.Status != release.Failed || rel.Failure == nil || rel.Failure.Step != tc.step {
				t.Errorf("release: %+v", rel)
			}
			if string(r.pub.manifest) != tc.manifest {
				t.Errorf("manifest = %q, want %q (restored)", r.pub.manifest, tc.manifest)
			}
			if !reflect.DeepEqual(r.cat.calls, tc.calls) {
				t.Errorf("catalog calls %v, want %v", r.cat.calls, tc.calls)
			}
			replayed, _, _ := r.svc.Get(context.Background(), rel.ID)
			if !reflect.DeepEqual(replayed, rel) {
				t.Errorf("failed release does not replay to the same state")
			}
		})
	}
}

func TestFirstReleaseRollbackRemovesManifest(t *testing.T) {
	t.Parallel()
	r := newRig()
	r.pub.manifest = nil // nothing published yet
	r.aud.err = errors.New("down")
	if _, err := r.svc.PublishClassic(context.Background(), "b1"); err == nil {
		t.Fatal("want an error")
	}
	if r.pub.manifest != nil {
		t.Errorf("manifest = %q, want none", r.pub.manifest)
	}
}
