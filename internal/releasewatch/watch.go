// Package releasewatch watches a release's health for a while after it
// finishes, from each cloud's own monitoring, and rolls it back when a
// service it changed breaches the environment's thresholds. A service a
// canary ramped was judged by its canary and isn't watched again; a
// rollback is never watched, so a watch never rolls a rollback back.
package releasewatch

import (
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/releasehealth"
)

// Watch statuses. A watch is watching until it passes, settles
// inconclusive at its bound, or a newer release supersedes it. A breach
// makes it rolling_back, then rolled_back, rollback_blocked when the
// rollback plan can't go ahead on its own, or rollback_failed when its
// deployment failed.
const (
	StatusWatching        = "watching"
	StatusPassed          = "passed"
	StatusInconclusive    = "inconclusive"
	StatusSuperseded      = "superseded"
	StatusRollingBack     = "rolling_back"
	StatusRolledBack      = "rolled_back"
	StatusRollbackBlocked = "rollback_blocked"
	StatusRollbackFailed  = "rollback_failed"
)

// DefaultDuration is how long canary and blue_green releases are watched
// when the release block names no watch.
const DefaultDuration = 15 * time.Minute

// grace is how long past its end a watch that can't decide keeps trying
// before it settles inconclusive.
const grace = 15 * time.Minute

// Errors the service and store return.
var (
	ErrNotFound = errors.New("release watch not found")
	// ErrConflict is a save of a watch someone else saved first.
	ErrConflict = errors.New("the release watch changed; read it again")
)

// WatchedService is one service a watch judges.
type WatchedService struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	BindingID string `json:"binding_id"`
	// Op is what the release did to it. A created or replaced service has
	// no history of its own to compare with, so it's judged on the
	// thresholds alone.
	Op string `json:"op"`
	// Revision is what the binding ran when the watch was armed, for
	// people. Health is read for the whole service.
	Revision  string     `json:"revision"`
	Verdict   string     `json:"verdict"` // the last releasehealth.Verdict, "" before the first judgement
	Reason    string     `json:"reason"`
	CheckedAt *time.Time `json:"checked_at"`
}

// UnwatchedService is a service the release changed that the watch can't
// judge, and why.
type UnwatchedService struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

// HeldRoute is a routed release whose retained instance a watch keeps.
// Nothing holds one yet.
type HeldRoute struct {
	Service  string   `json:"service"`
	Key      string   `json:"key"`
	RouteIDs []string `json:"route_ids"`
}

// Watch is one release's post-deploy watch.
type Watch struct {
	ID            string                   `json:"id" forge:"id"`
	OrgID         string                   `json:"org_id"`
	ProjectID     string                   `json:"project_id"`
	EnvironmentID string                   `json:"environment_id"`
	Environment   string                   `json:"environment"`
	DeploymentID  string                   `json:"deployment_id"`
	PlanID        string                   `json:"plan_id"`
	Status        string                   `json:"status"`
	Verdict       string                   `json:"verdict"`
	Reason        string                   `json:"reason"`
	Thresholds    releasehealth.Thresholds `json:"thresholds"`
	Services      []WatchedService         `json:"services"`
	Unwatched     []UnwatchedService       `json:"unwatched"`
	Held          []HeldRoute              `json:"held"`
	HeldReleased  bool                     `json:"held_released"`
	// Unverified is missing evidence: a changed service wasn't watched, or
	// the watch settled inconclusive. It never goes back to false.
	Unverified     bool   `json:"unverified"`
	RollbackPlanID string `json:"rollback_plan_id"`
	// RollbackApprovedAt is when Loomplane automation approved the
	// rollback plan, so it approves it once.
	RollbackApprovedAt *time.Time `json:"rollback_approved_at"`
	StartsAt           time.Time  `json:"starts_at"`
	EndsAt             time.Time  `json:"ends_at"`
	BoundAt            time.Time  `json:"bound_at"`
	CheckedAt          *time.Time `json:"checked_at"`
	SettledAt          *time.Time `json:"settled_at"`
	// Revision counts saves, so two ticks can't both save one.
	Revision  int64     `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Open reports whether the watch still has work to do.
func (w *Watch) Open() bool {
	return w.Status == StatusWatching || w.Status == StatusRollingBack
}

// ServiceCoverage says whether one service would be watched.
type ServiceCoverage struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Watchable bool   `json:"watchable"`
	Reason    string `json:"reason,omitempty"`
}

// Coverage is what a release in an environment would be watched for now.
type Coverage struct {
	Environment string `json:"environment"`
	// Duration is how long a release would be watched; 0 arms no watch.
	Duration time.Duration `json:"duration"`
	// Canary are the services a canary would ramp, which the watch leaves
	// to it.
	Canary []string `json:"canary"`
	// Services are every other service the environment runs.
	Services []ServiceCoverage `json:"services"`
	// Unverified says a release here now would leave missing evidence: no
	// watch armed, or a service not watched.
	Unverified bool `json:"unverified"`
}

// ReleaseEvidence is what the watch says about an environment's current
// release. Unverified true means missing evidence: ask a person.
type ReleaseEvidence struct {
	Environment  string `json:"environment"`
	DeploymentID string `json:"deployment_id"`
	Watched      bool   `json:"watched"`
	WatchID      string `json:"watch_id,omitempty"`
	Status       string `json:"status,omitempty"`
	Verdict      string `json:"verdict,omitempty"`
	Settled      bool   `json:"settled"`
	Unverified   bool   `json:"unverified"`
	Reason       string `json:"reason,omitempty"`
}
