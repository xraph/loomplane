// Package buildrunner keeps runner pools: groups of runners, which are
// Loomplane nodes with the runner role. A runner builds each image in a fresh
// Firecracker microVM and relays deploys to hosts only it can reach. A pool
// pins the runtime identity and architecture of its first checked runner, so
// every build it takes runs on the toolchain plans reviewed.
package buildrunner

import (
	"errors"
	"time"
)

// Scopes a pool can have.
const (
	// ScopePlatform pools serve every workspace the platform owner enables.
	ScopePlatform = "platform"
	// ScopeOrganization pools serve every project of one organization.
	ScopeOrganization = "organization"
	// ScopeProject pools serve one project.
	ScopeProject = "project"
)

// Modes a pool can run in.
const (
	// ModeSelfHosted pools are runners people install with runner start.
	ModeSelfHosted = "self_hosted"
	// ModeOnDemand pools are runners Loomplane starts in a cloud account
	// when builds need them, and removes after.
	ModeOnDemand = "on_demand"
)

// Errors the service returns.
var (
	ErrNotFound = errors.New("buildrunner: not found")
	ErrInvalid  = errors.New("buildrunner: invalid")
	ErrConflict = errors.New("buildrunner: conflict")
	ErrInUse    = errors.New("buildrunner: in use")
)

// Pool is a runner pool.
type Pool struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id,omitempty"`
	Scope string `json:"scope"`
	// ProjectID is the project a project pool serves.
	ProjectID string `json:"project_id,omitempty"`
	Name      string `json:"name"`
	// ConnectionID names the pool in plans, as build.builder. A platform
	// pool has none: services reach it as the global default.
	ConnectionID string `json:"connection_id,omitempty"`
	Mode         string `json:"mode"`
	// Architecture and RuntimeIdentity are pinned from the pool's first
	// checked runner. Empty until one checks in.
	Architecture    string       `json:"architecture,omitempty"`
	RuntimeIdentity string       `json:"runtime_identity,omitempty"`
	Settings        PoolSettings `json:"settings"`
	// Revision advances when the pool's pins change, which makes plans that
	// reviewed the old ones stale.
	Revision  int64     `json:"revision"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PoolSettings are how an on-demand pool starts runners, and the limits
// every pool keeps.
type PoolSettings struct {
	// Provider and CloudConnectionID are the cloud account on-demand
	// runners start in: "digitalocean" today.
	Provider          string `json:"provider,omitempty"`
	CloudConnectionID string `json:"cloud_connection_id,omitempty"`
	Region            string `json:"region,omitempty"`
	// Sizes are the machine sizes an on-demand pool may start, smallest
	// first. Each must pass the runner's own KVM check.
	Sizes []string `json:"sizes,omitempty"`
	// Image is the base image on-demand runners boot, pinned at review.
	Image string `json:"image,omitempty"`
	// MinWarm runners stay up while idle and take builds as slots free.
	MinWarm int `json:"min_warm,omitempty"`
	// MaxRunners bounds the runners an on-demand pool may run at once.
	MaxRunners int `json:"max_runners,omitempty"`
	// MaxBuilds and MaxAgeMinutes replace a warm runner with a fresh one.
	MaxBuilds     int `json:"max_builds,omitempty"`
	MaxAgeMinutes int `json:"max_age_minutes,omitempty"`
	// IdleTimeoutMinutes is how long a runner above MinWarm waits for
	// another build before it is removed.
	IdleTimeoutMinutes int `json:"idle_timeout_minutes,omitempty"`
	// Spot asks for interruptible machines where the provider has them.
	Spot bool `json:"spot,omitempty"`
	// ToolchainURL and ToolchainSHA256 are the Firecracker toolchain bundle
	// on-demand runners install: an https URL and the bundle's sha256,
	// which the runner checks before it unpacks anything.
	ToolchainURL    string `json:"toolchain_url,omitempty"`
	ToolchainSHA256 string `json:"toolchain_sha256,omitempty"`
	// Default makes the pool build every service whose manifest names no
	// builder: a project pool its project's, an organization pool the rest.
	// One pool of each is the default.
	Default bool `json:"default,omitempty"`
	// Registry is the registry connection a defaulted build pushes to
	// when its manifest names none.
	Registry string `json:"registry,omitempty"`
}

// Member is a runner in a pool, with what it showed at its last check.
type Member struct {
	NodeID          string            `json:"node_id"`
	PoolID          string            `json:"pool_id"`
	OrgID           string            `json:"org_id"`
	Architecture    string            `json:"architecture,omitempty"`
	RuntimeIdentity string            `json:"runtime_identity,omitempty"`
	Evidence        map[string]string `json:"evidence,omitempty"`
	// Reason says why the runner takes no builds, when it doesn't.
	Reason      string     `json:"reason,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	Builds      int        `json:"builds"`
	LastBuildAt *time.Time `json:"last_build_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Takes reports whether m runs the toolchain p pins, so it may take p's
// builds.
func (p *Pool) Takes(m Member) bool {
	return p.RuntimeIdentity != "" && m.Reason == "" && m.RuntimeIdentity == p.RuntimeIdentity && m.Architecture == p.Architecture
}
