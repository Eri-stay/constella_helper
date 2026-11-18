package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"constella/internal/domain/release"
	"constella/internal/platform/events"
)

// flakyCatalog fails its first n Reserve calls the way an unreachable service does.
type flakyCatalog struct {
	fakeCatalog
	failures int
}

func (f *flakyCatalog) Reserve(ctx context.Context, b, r string) (Reserved, error) {
	if f.failures > 0 {
		f.failures--
		f.calls = append(f.calls, "reserve(failed)")
		return Reserved{}, errors.New("connection refused")
	}
	return f.fakeCatalog.Reserve(ctx, b, r)
}

func newEDARig(t *testing.T) (*rig, *flakyCatalog) {
	t.Helper()
	r := newRig()
	fc := &flakyCatalog{}
	r.svc = New(r.st, fc, r.pub, r.aud, r.svc.log).WithRetry(4, time.Millisecond)
	return r, fc
}

func types(out []events.Envelope) []string {
	var t []string
	for _, e := range out {
		t = append(t, e.Type)
	}
	return t
}

func TestEDARequestReturnsBeforeAnyStep(t *testing.T) {
	t.Parallel()
	r, fc := newEDARig(t)
	rel, err := r.svc.RequestEDA(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Status != release.Requested || len(fc.calls) != 0 || string(r.pub.manifest) != "old" {
		t.Errorf("nothing may run in the request: %+v calls %v", rel, fc.calls)
	}
	if got := types(r.st.outbox); !reflect.DeepEqual(got, []string{events.ReleaseRequested}) {
		t.Errorf("outbox = %v", got)
	}
	if open, _ := r.st.Open(context.Background()); len(open) != 1 {
		t.Errorf("open releases = %v", open)
	}
}

func TestEDAWorkerPublishesWithoutAudit(t *testing.T) {
	t.Parallel()
	r, fc := newEDARig(t)
	ctx := context.Background()
	rel, _ := r.svc.RequestEDA(ctx, "b1")
	if err := r.svc.Advance(ctx, rel.ID); err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.svc.Get(ctx, rel.ID)
	if got.Status != release.Published || string(r.pub.manifest) != "new" {
		t.Errorf("release %+v manifest %q", got, r.pub.manifest)
	}
	if r.aud.calls != 0 {
		t.Error("event mode must not call the audit synchronously: telemetry reads the events")
	}
	if !reflect.DeepEqual(fc.calls, []string{"reserve", "mark"}) {
		t.Errorf("catalog calls %v", fc.calls)
	}
	want := []string{events.ReleaseRequested, events.ReleasePublished}
	if got := types(r.st.outbox); !reflect.DeepEqual(got, want) {
		t.Errorf("outbox = %v, want %v", got, want)
	}
	var data events.ReleaseData
	if err := r.st.outbox[1].Decode(&data); err != nil || data.Version != "b1" || data.SHA256 != "abc" || data.ReleaseID != rel.ID {
		t.Errorf("published data %+v (err %v)", data, err)
	}
	if r.st.outbox[1].CorrelationID != rel.ID {
		t.Error("events of one release must share the correlation id")
	}
}

func TestEDARetriesTransientFailures(t *testing.T) {
	t.Parallel()
	r, fc := newEDARig(t)
	fc.failures = 2 // catalog is down for two attempts
	ctx := context.Background()
	rel, _ := r.svc.RequestEDA(ctx, "b1")
	if err := r.svc.Advance(ctx, rel.ID); err != nil {
		t.Fatalf("a slow catalog must not fail the release: %v", err)
	}
	got, _, _ := r.svc.Get(ctx, rel.ID)
	if got.Status != release.Published {
		t.Errorf("status %s", got.Status)
	}
	if !reflect.DeepEqual(fc.calls[:3], []string{"reserve(failed)", "reserve(failed)", "reserve"}) {
		t.Errorf("calls %v", fc.calls)
	}
}

func TestEDAGivesUpAndCompensates(t *testing.T) {
	t.Parallel()
	r, fc := newEDARig(t)
	fc.failures = 100
	ctx := context.Background()
	rel, _ := r.svc.RequestEDA(ctx, "b1")
	err := r.svc.Advance(ctx, rel.ID)
	var se *StepError
	if !errors.As(err, &se) || se.Step != release.StepReserve || se.Code != CodeCatalogDown || !se.Compensated {
		t.Fatalf("error = %v", err)
	}
	if got := types(r.st.outbox); !reflect.DeepEqual(got, []string{events.ReleaseRequested, events.ReleaseFailed}) {
		t.Errorf("outbox = %v", got)
	}
	if open, _ := r.st.Open(ctx); len(open) != 0 {
		t.Errorf("a failed release must leave the open list: %v", open)
	}
}

func TestEDARefusalIsNotRetried(t *testing.T) {
	t.Parallel()
	r, fc := newEDARig(t)
	fc.reserveErr = &RefusedError{Code: "BUILD_NOT_VALIDATED"}
	ctx := context.Background()
	rel, _ := r.svc.RequestEDA(ctx, "b1")
	_ = r.svc.Advance(ctx, rel.ID)
	if n := countOf(fc.calls, "reserve"); n != 1 {
		t.Errorf("reserve called %d times, want 1", n)
	}
}

func countOf(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// A release interrupted after the switch resumes from its stored state and,
// if it then fails, restores the manifest that was saved in the event.
func TestEDAResumesAndRollsBackAfterRestart(t *testing.T) {
	t.Parallel()
	r, fc := newEDARig(t)
	ctx := context.Background()
	at := time.Now()
	rel, first, _ := release.Request("rX", "b1", release.ModeEDA, at)
	e2, _ := rel.Reserve("cosmos-b1.db", "abc", 7, at)
	e3, _ := rel.Switch("b1", "old-manifest", at)
	r.st.streams["rX"] = []release.Event{first, e2, e3}
	r.pub.manifest = []byte("new") // the switch happened before the crash
	fc.markErr = errors.New("catalog down after restart")

	err := r.svc.Advance(ctx, "rX")
	var se *StepError
	if !errors.As(err, &se) || se.Step != release.StepMarkReleased {
		t.Fatalf("error = %v", err)
	}
	if string(r.pub.manifest) != "old-manifest" {
		t.Errorf("manifest = %q, want the one stored in the switched event", r.pub.manifest)
	}
	if countOf(fc.calls, "reserve") != 0 {
		t.Error("finished steps must not run again")
	}
}

func TestEDAShutdownLeavesReleaseOpen(t *testing.T) {
	t.Parallel()
	r, fc := newEDARig(t)
	fc.failures = 100
	rel, _ := r.svc.RequestEDA(context.Background(), "b1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the process is stopping
	if err := r.svc.Advance(ctx, rel.ID); !errors.Is(err, errAborted) {
		t.Fatalf("error = %v, want errAborted", err)
	}
	got, _, _ := r.svc.Get(context.Background(), rel.ID)
	if got.Status != release.Requested {
		t.Errorf("a shutdown must not fail the release: %s", got.Status)
	}
}
