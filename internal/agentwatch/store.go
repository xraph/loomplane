package agentwatch

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/xraph/loomplane/internal/notifications"
)

// State is where one workspace's watcher stands.
type State struct {
	OrgID string
	// EventCursor is the sequence of the last event the watcher read.
	EventCursor  int64
	Paused       bool
	PausedReason string // PauseBudget or PauseBilling
	PausedAt     *time.Time
	Checks       map[string]Check // by binding ID
	SweptAt      *time.Time
	RecheckedAt  *time.Time
	TickedAt     *time.Time
}

// clone returns a copy that shares nothing with st.
func (st *State) clone() *State {
	out := *st
	out.Checks = maps.Clone(st.Checks)
	out.PausedAt, out.SweptAt = cloneTime(st.PausedAt), cloneTime(st.SweptAt)
	out.RecheckedAt, out.TickedAt = cloneTime(st.RecheckedAt), cloneTime(st.TickedAt)
	return &out
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

// Store keeps each workspace's watcher state, and the lease that lets one
// replica watch a workspace at a time.
type Store interface {
	// Claim takes the workspace's lease for owner for ttl, when nobody
	// else holds a live one, and returns the state. The first claim of a
	// workspace inserts it with its cursor at highWater, so a new watcher
	// starts from now rather than replaying history. claimed is false,
	// with a nil state, when another owner holds the lease.
	Claim(ctx context.Context, orgID, owner string, ttl time.Duration, highWater func() (int64, error)) (st *State, claimed bool, err error)
	// Save writes st while owner holds a live lease, and is ErrLeaseLost
	// otherwise.
	Save(ctx context.Context, owner string, st *State) error
	// SaveNotifying is Save with send run in the same transaction, so a
	// notice is queued only with the state that announces it. Nothing is
	// saved when send fails.
	SaveNotifying(ctx context.Context, owner string, st *State, send func(ctx context.Context, q notifications.Execer) error) error
	// Release lets the lease go, when owner holds it.
	Release(ctx context.Context, orgID, owner string) error
	// Get returns the workspace's state: a zero State for one never
	// watched.
	Get(ctx context.Context, orgID string) (*State, error)
}

// ErrLeaseLost is a save finding another replica holds the workspace now.
var ErrLeaseLost = errors.New("the watcher's lease moved to another replica")
