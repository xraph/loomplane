package deployments

import (
	"context"
	"fmt"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// Canary statuses. A canary is pending until its candidates are up, ramping
// while it moves weight step by step, then promoting to promoted when every
// step passed. A failed step makes it reverting, then rolled_back, or
// revert_failed when traffic couldn't be moved back to the baseline. A
// canary still promoting when the deployment fails ends at promote_failed.
const (
	CanaryPending       = "pending"
	CanaryRamping       = "ramping"
	CanaryPromoting     = "promoting"
	CanaryPromoted      = "promoted"
	CanaryReverting     = "reverting"
	CanaryRolledBack    = "rolled_back"
	CanaryRevertFailed  = "revert_failed"
	CanaryPromoteFailed = "promote_failed"
)

// canaryActive are the statuses of a canary that still holds its
// environment: nothing else may deploy there until it settles. Planning
// owns the list, since it blocks new plans with it.
var canaryActive = planning.ActiveCanaryStatuses()

// Canary is a deployment's canary release: the weights it ramps through,
// where it is, and each service it ramps. It's stored on the deployment.
type Canary struct {
	Status      string                   `json:"status"`
	Steps       []int                    `json:"steps"`
	HoldSeconds int64                    `json:"hold_seconds"`
	Thresholds  releasehealth.Thresholds `json:"thresholds"`
	// Step indexes Steps; -1 before the first.
	Step int `json:"step"`
	// Weight is the percent of traffic the candidates still up take now. A
	// service already promoted runs the release in full whatever Weight
	// says, so when services diverge Weight describes only the unpromoted
	// ones: 0 once they are reverted.
	Weight int `json:"weight"`
	// Reason says why the canary rolled back or failed.
	Reason    string          `json:"reason,omitempty"`
	Services  []CanaryService `json:"services"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// CanaryService is one service a canary ramps.
type CanaryService struct {
	ActionID  string `json:"action_id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	BindingID string `json:"binding_id"`
	// Baseline and Candidate are revisions, as releasehealth reads them.
	// Candidate is empty while no candidate is up: before it starts, once
	// a revert removed it, and after a retry reopens the canary.
	Baseline  string `json:"baseline,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	Image     string `json:"image,omitempty"`
	// Refs are provider-specific handles, such as target group ARNs.
	Refs map[string]string `json:"refs,omitempty"`
}

// CanaryTarget is a candidate an executor started beside its service's
// baseline: both revisions, as releasehealth reads them, the image the
// candidate runs, and the executor's own handles.
type CanaryTarget struct {
	Baseline, Candidate string
	Image               string
	Refs                map[string]string
}

// Weighted is an executor whose target splits a service's traffic with its
// own native weighting, so a canary can ramp there. Each call may run again
// after a crash or a timeout, so each must be safe to repeat.
type Weighted interface {
	Executor
	// ApplyCandidate starts the action's candidate beside what runs, taking
	// no traffic, and returns both. It builds the image as Apply does, in
	// one build step per executor, so a later change can start the
	// candidate from a reviewed artifact instead. A candidate an earlier
	// attempt left is replaced.
	ApplyCandidate(ctx context.Context, job *Job) (*CanaryTarget, error)
	// SetWeights gives the candidate candidatePercent of the traffic and
	// the baseline the rest. At 0 the candidate stays attached.
	SetWeights(ctx context.Context, job *Job, t CanaryTarget, candidatePercent int) error
	// Promote makes the candidate what runs, with all of the traffic, and
	// returns what Apply would have, which the runner binds.
	Promote(ctx context.Context, job *Job, t CanaryTarget) (*Outcome, error)
	// Revert gives the baseline all of the traffic and removes the
	// candidate. Where the cloud keeps one template per service, as Cloud
	// Run and Azure Container Apps do, it restores the baseline's template
	// too, so the template, the traffic and the binding agree again and a
	// drift check finds nothing changed.
	Revert(ctx context.Context, job *Job, t CanaryTarget) error
}

// target is the candidate s names.
func (s CanaryService) target() CanaryTarget {
	return CanaryTarget{Baseline: s.Baseline, Candidate: s.Candidate, Image: s.Image, Refs: s.Refs}
}

// service returns the canary's service for an action, or nil.
func (c *Canary) service(actionID string) *CanaryService {
	if c == nil {
		return nil
	}
	for i := range c.Services {
		if c.Services[i].ActionID == actionID {
			return &c.Services[i]
		}
	}
	return nil
}

// reopen starts the canary over for a retry: back to pending before the
// first step, with no candidate. Baselines and provider handles stay, since
// the retry ramps from the same baseline.
func (c *Canary) reopen() {
	c.Status, c.Step, c.Weight, c.Reason = CanaryPending, -1, 0, ""
	for i := range c.Services {
		c.Services[i].Candidate, c.Services[i].Image = "", ""
	}
}

// CanaryVerdict is the judgement of one canary step's window for one
// service: pass, fail or inconclusive, with the health it was judged on.
type CanaryVerdict struct {
	// ID is "<deployment>/<retry>/<action>/<step>/<attempt>" (VerdictID).
	ID           string `json:"id"`
	OrgID        string `json:"org_id"`
	ProjectID    string `json:"project_id"`
	DeploymentID string `json:"deployment_id"`
	// Retry is the deployment's Retries when it was judged: a retried
	// deployment ramps again, and judges afresh.
	Retry    int                   `json:"retry"`
	ActionID string                `json:"action_id"`
	Service  string                `json:"service"`
	Step     int                   `json:"step"`
	Attempt  int                   `json:"attempt"`
	Weight   int                   `json:"weight"`
	Verdict  releasehealth.Verdict `json:"verdict"`
	Reason   string                `json:"reason"`
	// BaselineFallback marks a baseline taken from an earlier step because
	// the baseline served too little traffic in this window.
	BaselineFallback bool                     `json:"baseline_fallback"`
	Baseline         releasehealth.Health     `json:"baseline"`
	Candidate        releasehealth.Health     `json:"candidate"`
	Thresholds       releasehealth.Thresholds `json:"thresholds"`
	WindowStart      time.Time                `json:"window_start"`
	WindowEnd        time.Time                `json:"window_end"`
	DecidedAt        time.Time                `json:"decided_at"`
}

// VerdictID is a verdict's ID. It's deterministic, so a repeated judgement
// writes the verdict it already wrote.
func VerdictID(deploymentID string, retry int, actionID string, step, attempt int) string {
	return fmt.Sprintf("%s/%d/%s/%d/%d", deploymentID, retry, actionID, step, attempt)
}

// CanaryReport is a deployment's canary and its verdicts so far.
type CanaryReport struct {
	Canary   *Canary         `json:"canary"`
	Verdicts []CanaryVerdict `json:"verdicts"`
}

// CanaryRun is one deployment that ran a canary, as a release report reads
// it.
type CanaryRun struct {
	DeploymentID string `json:"deployment_id"`
	PlanID       string `json:"plan_id"`
	Environment  string `json:"environment"`
	// Status is the deployment's.
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Canary     *Canary         `json:"canary"`
	Verdicts   []CanaryVerdict `json:"verdicts"`
}

// CanaryStore persists canary state and verdicts, scoped to an
// organization.
type CanaryStore interface {
	// SetCanary writes the deployment's canary.
	SetCanary(ctx context.Context, orgID, deploymentID string, c *Canary) error
	// UpdateCanary reads the deployment, lets change edit its canary and
	// stores the canary, as one change: nothing else changes the canary in
	// between, so neither change is lost. An error from change stores
	// nothing and is returned as it is. A deployment without a canary is
	// an error.
	UpdateCanary(ctx context.Context, orgID, deploymentID string, change func(*Deployment) error) (*Deployment, error)
	// RecordVerdict stores v under VerdictID, or does nothing when that
	// verdict is stored. It sets v.ID, and v.ProjectID from the deployment,
	// and returns ErrNotFound when the deployment isn't in v.OrgID.
	RecordVerdict(ctx context.Context, v *CanaryVerdict) error
	// Verdicts returns a deployment's verdicts by retry, step, attempt and action.
	Verdicts(ctx context.Context, orgID, deploymentID string) ([]CanaryVerdict, error)
	// RecentCanaries returns up to limit of an environment's deployments
	// that ran a canary, newest first.
	RecentCanaries(ctx context.Context, orgID, projectID, environmentID string, limit int) ([]*Deployment, error)
	// RampingCanary returns the running deployment whose canary still holds
	// the environment, or "".
	RampingCanary(ctx context.Context, orgID, environmentID string) (string, error)
	// CanaryBinding reports whether a canary that still holds its
	// environment ramps the binding.
	CanaryBinding(ctx context.Context, orgID, bindingID string) (bool, error)
}
