// Package metering is the seam between the features that use platform
// resources and the billing that pays for them. A Gate says whether an
// organization may start something, and a Meter records what it used.
package metering

import (
	"context"
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
