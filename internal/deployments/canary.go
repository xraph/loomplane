package deployments

import (
	"context"
	"fmt"
	"time"

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
// environment: nothing else may deploy there until it settles.
var canaryActive = []string{CanaryPending, CanaryRamping, CanaryPromoting, CanaryReverting}

// Canary is a deployment's canary release: the weights it ramps through,
// where it is, and each service it ramps. It's stored on the deployment.
type Canary struct {
	Status      string                   `json:"status"`
	Steps       []int                    `json:"steps"`
	HoldSeconds int64                    `json:"hold_seconds"`
	Thresholds  releasehealth.Thresholds `json:"thresholds"`
	// Step indexes Steps; -1 before the first.
	Step int `json:"step"`
	// Weight is the candidate's percent of traffic now.
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
	Baseline  string `json:"baseline,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	Image     string `json:"image,omitempty"`
	// Refs are provider-specific handles, such as target group ARNs.
	Refs map[string]string `json:"refs,omitempty"`
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
	// ID is "<deployment>/<action>/<step>/<attempt>" (VerdictID).
	ID           string                   `json:"id"`
	OrgID        string                   `json:"org_id"`
	ProjectID    string                   `json:"project_id"`
	DeploymentID string                   `json:"deployment_id"`
	ActionID     string                   `json:"action_id"`
	Service      string                   `json:"service"`
	Step         int                      `json:"step"`
	Attempt      int                      `json:"attempt"`
	Weight       int                      `json:"weight"`
	Verdict      releasehealth.Verdict    `json:"verdict"`
	Reason       string                   `json:"reason"`
	Baseline     releasehealth.Health     `json:"baseline"`
	Candidate    releasehealth.Health     `json:"candidate"`
	Thresholds   releasehealth.Thresholds `json:"thresholds"`
	WindowStart  time.Time                `json:"window_start"`
	WindowEnd    time.Time                `json:"window_end"`
	DecidedAt    time.Time                `json:"decided_at"`
}

// VerdictID is a verdict's ID. It's deterministic, so a retried judgement
// writes the verdict it already wrote.
func VerdictID(deploymentID, actionID string, step, attempt int) string {
	return fmt.Sprintf("%s/%s/%d/%d", deploymentID, actionID, step, attempt)
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
	// RecordVerdict stores v under VerdictID, or does nothing when that
	// verdict is stored. It sets v.ID, and v.ProjectID from the deployment,
	// and returns ErrNotFound when the deployment isn't in v.OrgID.
	RecordVerdict(ctx context.Context, v *CanaryVerdict) error
	// Verdicts returns a deployment's verdicts by step, attempt and action.
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
