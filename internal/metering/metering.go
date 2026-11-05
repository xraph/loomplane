// Package metering is the seam between the features that use platform
// resources and the billing that pays for them. A Gate says whether an
// organization may start something, and a Meter records what it used.
package metering

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// Kind is what a gate check is for.
type Kind string

const (
	KindDeploy     Kind = "deploy"
	KindBuild      Kind = "build"
	KindAIPlatform Kind = "ai_platform"
)

// Gate decides whether an organization may run something.
type Gate interface {
	CanRun(ctx context.Context, orgID string, kind Kind, platformOwned bool) error
}

// Meter records usage. Record is idempotent on Event.ID.
type Meter interface {
	Record(ctx context.Context, e Event) error
}

// Event is one metered use of a platform resource.
type Event struct {
	ID            string
	OrgID         string
	Feature       string
	ResourceRef   string
	Quantity      float64
	OccurredAt    time.Time
	PlatformOwned bool
}

type allowAll struct{}

func (allowAll) CanRun(context.Context, string, Kind, bool) error { return nil }

type discard struct{}

func (discard) Record(context.Context, Event) error { return nil }

// AllowAll is a Gate that lets everything run.
var AllowAll Gate = allowAll{}

// Discard is a Meter that drops every event.
var Discard Meter = discard{}

// ErrBlocked is what every Gate refusal matches with errors.Is.
var ErrBlocked = errors.New("metering: the workspace can't run this right now")

// ErrNotReady refuses a call made before the edition that bills has chosen
// its Gate (RefuseUnset): billing is still starting, for a moment after
// every deploy. It is no refusal of the workspace and doesn't match
// ErrBlocked, so a queued task is tried again and nothing pauses or tells
// anyone about billing because of it.
var ErrNotReady = errors.New("billing isn't ready yet; try again in a moment")

// BlockedError is a Gate's refusal. Reason is a sentence a person reads; Error
// returns it unchanged, so a refusal wrapped with more context (platform
// Cloud's "Loomplane Cloud can't start this for the workspace: ...") still
// reads well.
type BlockedError struct{ Reason string }

func (b *BlockedError) Error() string        { return b.Reason }
func (b *BlockedError) Is(target error) bool { return target == ErrBlocked }

// Late is a Gate and a Meter chosen after they are handed out: forge
// registers every extension before it starts any, and the edition that
// bills provides its Gate and Meter from Register. Until Set, Late lets
// everything run and records nothing. Its zero value is ready to use.
type Late struct {
	gate  atomic.Pointer[Gate]
	meter atomic.Pointer[Meter]
}

// Set makes g and m the Gate and Meter Late delegates to.
func (l *Late) Set(g Gate, m Meter) {
	l.gate.Store(&g)
	l.meter.Store(&m)
}

// CanRun delegates to the Gate, or allows when none is set.
func (l *Late) CanRun(ctx context.Context, orgID string, kind Kind, platformOwned bool) error {
	if g := l.gate.Load(); g != nil && *g != nil {
		return (*g).CanRun(ctx, orgID, kind, platformOwned)
	}
	return nil
}

// Record delegates to the Meter, or drops the event when none is set.
func (l *Late) Record(ctx context.Context, e Event) error {
	if m := l.meter.Load(); m != nil && *m != nil {
		return (*m).Record(ctx, e)
	}
	return nil
}
