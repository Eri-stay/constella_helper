package events

import (
	"errors"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 11, 17, 12, 0, 0, 0, time.FixedZone("EET", 2*3600))
	parent := Envelope{ID: "p1", CorrelationID: "rel-1"}

	tests := []struct {
		name      string
		eventType string
		producer  string
		opts      []Option
		wantErr   error
		wantCorr  string
		wantCause string
	}{
		{name: "plain", eventType: ReleasePublished, producer: "release"},
		{name: "correlation", eventType: ReleasePublished, producer: "release",
			opts: []Option{WithCorrelation("rel-7")}, wantCorr: "rel-7"},
		{name: "caused by inherits correlation", eventType: BuildValidated, producer: "catalog",
			opts: []Option{CausedBy(parent)}, wantCorr: "rel-1", wantCause: "p1"},
		{name: "explicit correlation wins", eventType: BuildValidated, producer: "catalog",
			opts: []Option{WithCorrelation("x"), CausedBy(parent)}, wantCorr: "x", wantCause: "p1"},
		{name: "empty type", producer: "release", wantErr: ErrInvalidEnvelope},
		{name: "empty producer", eventType: ReleasePublished, wantErr: ErrInvalidEnvelope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := append([]Option{WithClock(func() time.Time { return fixed })}, tc.opts...)
			env, err := New(tc.eventType, tc.producer, map[string]int{"n": 1}, opts...)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if len(env.ID) != 32 {
				t.Errorf("id %q: want 32 hex chars", env.ID)
			}
			if !env.OccurredAt.Equal(fixed) || env.OccurredAt.Location() != time.UTC {
				t.Errorf("occurred_at = %v, want %v in UTC", env.OccurredAt, fixed)
			}
			if env.CorrelationID != tc.wantCorr || env.CausationID != tc.wantCause {
				t.Errorf("correlation/causation = %q/%q, want %q/%q",
					env.CorrelationID, env.CausationID, tc.wantCorr, tc.wantCause)
			}
			var got map[string]int
			if err := env.Decode(&got); err != nil || got["n"] != 1 {
				t.Errorf("decode = %v, %v", got, err)
			}
		})
	}
}

func TestNewIDUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for range 1000 {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
