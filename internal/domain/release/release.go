// Package release is the pure domain of publishing a cosmos.db build: the
// Release aggregate, built in the style of the Lab 2 aggregates. A command
// checks the state and returns an Event; Apply changes the state. Replaying
// the stored events gives the same state as the live aggregate.
package release

import (
	"errors"
	"fmt"
	"time"
)

// Status of a release.
type Status string

// Release statuses: Requested → Reserved → Switched → Published, or Failed.
const (
	Requested Status = "requested"
	Reserved  Status = "reserved"
	Switched  Status = "switched"
	Published Status = "published"
	Failed    Status = "failed"
)

// Mode is how PublishRelease is executed.
const (
	ModeClassic = "classic"
	ModeEDA     = "eda"
)

// Steps of the saga, used in failures.
const (
	StepReserve      = "reserve"
	StepSwitch       = "switch"
	StepMarkReleased = "mark_released"
	StepAudit        = "audit"
)

// EventType names a fact about a release.
type EventType string

// Event types.
const (
	EvRequested EventType = "requested"
	EvReserved  EventType = "reserved"
	EvSwitched  EventType = "switched"
	EvPublished EventType = "published"
	EvFailed    EventType = "failed"
)

// Event is one stored fact. Only the fields of its Type are filled.
type Event struct {
	Type EventType `json:"type"`
	At   time.Time `json:"at"`

	BuildID string `json:"build_id,omitempty"` // requested
	Mode    string `json:"mode,omitempty"`     // requested

	Artifact  string `json:"artifact,omitempty"` // reserved
	SHA256    string `json:"sha256,omitempty"`   // reserved
	SizeBytes int64  `json:"size_bytes,omitempty"`

	Version  string `json:"version,omitempty"`  // switched
	Previous string `json:"previous,omitempty"` // switched: the manifest that was replaced, for rollback after a restart

	Step        string `json:"step,omitempty"` // failed
	Code        string `json:"code,omitempty"`
	Compensated bool   `json:"compensated,omitempty"`
	Detail      string `json:"detail,omitempty"` // technical cause of a failure, for operators
}

// ErrTransition is returned for a command that is not allowed in the current status.
var ErrTransition = errors.New("invalid release transition")

// Release is the aggregate.
type Release struct {
	ID        string
	BuildID   string
	Mode      string
	Status    Status
	Artifact  string
	SHA256    string
	SizeBytes int64
	Version   string
	Failure   *Event // the failed event, if any
	StartedAt time.Time
	EndedAt   time.Time
	// Revision is the number of applied events (for optimistic concurrency).
	Revision int
}

// Request starts a release and returns it with its first event.
func Request(id, buildID, mode string, at time.Time) (*Release, Event, error) {
	if id == "" || buildID == "" {
		return nil, Event{}, fmt.Errorf("%w: id and build are required", ErrTransition)
	}
	if mode != ModeClassic && mode != ModeEDA {
		return nil, Event{}, fmt.Errorf("%w: unknown mode %q", ErrTransition, mode)
	}
	r := &Release{ID: id}
	ev := Event{Type: EvRequested, At: at.UTC(), BuildID: buildID, Mode: mode}
	r.Apply(ev)
	return r, ev, nil
}

func (r *Release) expect(want Status, cmd string) error {
	if r.Status != want {
		return fmt.Errorf("%w: %s in status %s", ErrTransition, cmd, r.Status)
	}
	return nil
}

// Reserve records that the catalog reserved the build.
func (r *Release) Reserve(artifact, sha string, size int64, at time.Time) (Event, error) {
	if err := r.expect(Requested, "reserve"); err != nil {
		return Event{}, err
	}
	ev := Event{Type: EvReserved, At: at.UTC(), Artifact: artifact, SHA256: sha, SizeBytes: size}
	r.Apply(ev)
	return ev, nil
}

// Switch records that the published version.json now points at the build.
// previous is the replaced manifest ("" if there was none).
func (r *Release) Switch(version, previous string, at time.Time) (Event, error) {
	if err := r.expect(Reserved, "switch"); err != nil {
		return Event{}, err
	}
	ev := Event{Type: EvSwitched, At: at.UTC(), Version: version, Previous: previous}
	r.Apply(ev)
	return ev, nil
}

// Publish completes the release.
func (r *Release) Publish(at time.Time) (Event, error) {
	if err := r.expect(Switched, "publish"); err != nil {
		return Event{}, err
	}
	ev := Event{Type: EvPublished, At: at.UTC()}
	r.Apply(ev)
	return ev, nil
}

// Fail ends a release that has not been published. compensated tells that
// every completed step was undone.
func (r *Release) Fail(step, code string, compensated bool, at time.Time) (Event, error) {
	if r.Status == Published || r.Status == Failed {
		return Event{}, fmt.Errorf("%w: fail in status %s", ErrTransition, r.Status)
	}
	ev := Event{Type: EvFailed, At: at.UTC(), Step: step, Code: code, Compensated: compensated}
	r.Apply(ev)
	return ev, nil
}

// Apply changes the state by one event. It never fails: events are facts.
func (r *Release) Apply(e Event) {
	switch e.Type {
	case EvRequested:
		r.BuildID, r.Mode, r.Status, r.StartedAt = e.BuildID, e.Mode, Requested, e.At
	case EvReserved:
		r.Artifact, r.SHA256, r.SizeBytes, r.Status = e.Artifact, e.SHA256, e.SizeBytes, Reserved
	case EvSwitched:
		r.Version, r.Status = e.Version, Switched
	case EvPublished:
		r.Status, r.EndedAt = Published, e.At
	case EvFailed:
		ev := e
		r.Failure, r.Status, r.EndedAt = &ev, Failed, e.At
	}
	r.Revision++
}

// Replay rebuilds a release from its stored events.
func Replay(id string, events []Event) (*Release, error) {
	if len(events) == 0 || events[0].Type != EvRequested {
		return nil, fmt.Errorf("%w: stream of %s must start with %s", ErrTransition, id, EvRequested)
	}
	r := &Release{ID: id}
	for _, e := range events {
		r.Apply(e)
	}
	return r, nil
}
