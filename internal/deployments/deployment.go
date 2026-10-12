// Package deployments carries out approved plans. A deployment is one run
// of a plan: its source stored as an artifact, then each action built and
// deployed by its provider's executor, in the plan's order, then checked.
// The deployment workflow (internal/orchestration) drives it one durable
// activity at a time, so a restart resumes where it was.
package deployments

import (
	"context"
	"errors"
	"time"
)

// Deployment statuses.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Step statuses. A step is pending until its turn, then works through
// building, deploying and verifying to succeeded, or stops at failed.
// Steps after a failure are skipped.
const (
	StepPending   = "pending"
	StepBuilding  = "building"
	StepDeploying = "deploying"
	StepVerifying = "verifying"
	StepSucceeded = "succeeded"
	StepFailed    = "failed"
	StepSkipped   = "skipped"
)

// Errors.
var (
	ErrNotFound = errors.New("deployment not found")
	// ErrNoExecutor is an action whose provider Loomplane can't act on.
	ErrNoExecutor = errors.New("loomplane has no adapter for this provider yet")
)

// Deployment is one run of an approved plan.
type Deployment struct {
	ID            string `json:"id" forge:"id"`
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Environment   string `json:"environment"`
	PlanID        string `json:"plan_id"`
	CommitSHA     string `json:"commit_sha"`
	Status        string `json:"status"`
	// Source is the artifact key of the commit's tarball, once stored.
	Source string `json:"source"`
	// Error says why the deployment failed.
	Error string `json:"error"`
	Steps []Step `json:"steps"`
	// Retries counts how often someone retried it after it failed.
	Retries    int        `json:"retries"`
	StartedBy  string     `json:"started_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Step is one action of the plan, as it's carried out.
type Step struct {
	// ActionID is the plan action's ID, such as "service:api".
	ActionID string `json:"action_id"`
	Name     string `json:"name"`
	Subject  string `json:"subject"`
	Op       string `json:"op"`
	Provider string `json:"provider"`
	// Order is the plan step it runs in.
	Order  int    `json:"order"`
	Status string `json:"status"`
	// Message says what the step is doing, or why it failed.
	Message string `json:"message"`
	// URL is where a deployed service answers.
	URL string `json:"url"`
	// Image is what a container step deployed.
	Image      string     `json:"image"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (d *Deployment) normalize() {
	d.StartedAt = d.StartedAt.UTC()
	if d.FinishedAt != nil {
		t := d.FinishedAt.UTC()
		d.FinishedAt = &t
	}
	if d.Steps == nil {
		d.Steps = []Step{}
	}
}

// Step returns the step for an action, or nil.
func (d *Deployment) Step(actionID string) *Step {
	for i := range d.Steps {
		if d.Steps[i].ActionID == actionID {
			return &d.Steps[i]
		}
	}
	return nil
}

// Store persists deployments, scoped to an organization.
type Store interface {
	// CreateDeployment inserts d, or returns the deployment that already
	// exists for its plan: a plan is deployed at most once.
	CreateDeployment(ctx context.Context, d *Deployment) (*Deployment, error)
	GetDeployment(ctx context.Context, orgID, id string) (*Deployment, error)
	// ForPlan returns the plan's deployment, or ErrNotFound.
	ForPlan(ctx context.Context, orgID, planID string) (*Deployment, error)
	ListDeployments(ctx context.Context, orgID, projectID string, limit int) ([]*Deployment, error)
	// SetSource records the stored source.
	SetSource(ctx context.Context, orgID, id, source string) error
	// UpdateStep writes one step.
	UpdateStep(ctx context.Context, orgID, id string, s Step) error
	// Finish settles a running deployment.
	Finish(ctx context.Context, orgID, id, status, reason string, at time.Time) error
	// Reopen sets a failed deployment running again, with its failed and
	// skipped steps pending. It returns false, with no error, when the
	// deployment isn't failed, such as when another retry won.
	Reopen(ctx context.Context, orgID, id string) (bool, error)
}

// Order groups a deployment's steps by plan step, in the order they run.
func (d *Deployment) Order() [][]string {
	var out [][]string
	last := 0
	for i, s := range d.Steps {
		if i == 0 || s.Order != last {
			out = append(out, nil)
			last = s.Order
		}
		out[len(out)-1] = append(out[len(out)-1], s.ActionID)
	}
	return out
}
