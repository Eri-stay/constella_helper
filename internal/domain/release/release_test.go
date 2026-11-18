package release

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func TestHappyPathAndReplay(t *testing.T) {
	t.Parallel()
	r, e1, err := Request("r1", "b1", ModeClassic, t0)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{e1}
	step := func(e Event, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	step(r.Reserve("cosmos-b1.db", "abc", 42, t0))
	step(r.Switch("b1", t0))
	step(r.Publish(t0.Add(time.Second)))

	if r.Status != Published || r.Version != "b1" || r.Revision != 4 || !r.EndedAt.Equal(t0.Add(time.Second)) {
		t.Errorf("live state: %+v", r)
	}
	replayed, err := Replay("r1", events)
	if err != nil || !reflect.DeepEqual(replayed, r) {
		t.Errorf("replay differs:\n got %+v (err %v)\nwant %+v", replayed, err, r)
	}
}

func TestFailureReplay(t *testing.T) {
	t.Parallel()
	r, e1, _ := Request("r2", "b1", ModeClassic, t0)
	e2, _ := r.Reserve("a", "h", 1, t0)
	e3, err := r.Fail(StepSwitch, "DISK", true, t0)
	if err != nil || r.Status != Failed || r.Failure == nil || !r.Failure.Compensated {
		t.Fatalf("fail: %+v (err %v)", r, err)
	}
	replayed, _ := Replay("r2", []Event{e1, e2, e3})
	if !reflect.DeepEqual(replayed, r) {
		t.Errorf("replay differs: %+v vs %+v", replayed, r)
	}
}

func TestInvalidTransitions(t *testing.T) {
	t.Parallel()
	r, _, _ := Request("r", "b", ModeEDA, t0)
	if _, err := r.Switch("v", t0); !errors.Is(err, ErrTransition) {
		t.Errorf("switch before reserve: %v", err)
	}
	if _, err := r.Publish(t0); !errors.Is(err, ErrTransition) {
		t.Errorf("publish before switch: %v", err)
	}
	_, _ = r.Fail(StepReserve, "X", false, t0)
	if _, err := r.Fail(StepReserve, "X", false, t0); !errors.Is(err, ErrTransition) {
		t.Errorf("second fail: %v", err)
	}
	if _, _, err := Request("r", "b", "weird", t0); !errors.Is(err, ErrTransition) {
		t.Errorf("unknown mode: %v", err)
	}
	if _, err := Replay("x", nil); !errors.Is(err, ErrTransition) {
		t.Errorf("empty replay: %v", err)
	}
}
