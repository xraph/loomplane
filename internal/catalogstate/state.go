// Package catalogstate reads the state of one catalog resource in one
// environment from deployment facts alone: its binding, the open plans and
// the last deployment that ran it. Model status reads it now; the AI
// gateway's status adopts it later. It knows nothing about what the
// resource is.
package catalogstate

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

// State values.
const (
	// Absent: nothing deploys it, and nothing deployed it.
	Absent = "absent"
	// Planned: an open plan creates or changes it, and it isn't running.
	Planned = "planned"
	// Deploying: a deployment is running its step.
	Deploying = "deploying"
	// Ready: it's bound, and the last deployment that ran it didn't fail.
	Ready = "ready"
	// Failed: the last deployment that ran it failed before it was done.
	Failed = "failed"
)

// MaxMessage is the longest Message, in bytes.
const MaxMessage = 512

// State of one catalog resource in one environment, from deployment facts
// only.
type State struct {
	Value string // Absent, Planned, Deploying, Ready or Failed
	// PlanID is the open plan that creates or changes it, whatever Value
	// is: a failed model with a plan to fix it links to that plan.
	PlanID string
	// DeploymentID is the last deployment that ran it.
	DeploymentID string
	// Message is the failed step's error, or the deployment's when it
	// failed before the step ran, cut to MaxMessage bytes.
	Message string
	// ConnectionID is the resource's target connection: its binding's, or
	// the open plan's while it has none.
	ConnectionID string
}

// Deps read the facts. Each is required.
type Deps struct {
	// Bindings lists what's bound in the environment.
	Bindings func(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	// OpenPlans lists the environment's plans that can still be decided.
	OpenPlans func(ctx context.Context, orgID, environmentID string) ([]*planning.Plan, error)
	// LastRun returns the last deployment with a step for the resource, nil
	// or deployments.ErrNotFound when there's none.
	LastRun func(ctx context.Context, orgID, environmentID, resource string) (*deployments.Deployment, error)
}

// Of returns the state of resource in environmentID.
func Of(ctx context.Context, d Deps, orgID, environmentID, resource string) (State, error) {
	last, err := d.LastRun(ctx, orgID, environmentID, resource)
	if errors.Is(err, deployments.ErrNotFound) {
		last, err = nil, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read the last deployment of %s: %w", resource, err)
	}
	bindings, err := d.Bindings(ctx, orgID, environmentID)
	if err != nil {
		return State{}, fmt.Errorf("read what's bound for %s: %w", resource, err)
	}
	plans, err := d.OpenPlans(ctx, orgID, environmentID)
	if err != nil {
		return State{}, fmt.Errorf("read the open plans for %s: %w", resource, err)
	}

	var s State
	bound := false
	for _, b := range bindings {
		if b.Subject == planning.ResourceSubject && b.Name == resource {
			bound, s.ConnectionID = true, b.ConnectionID
			break
		}
	}
	if a, p := plannedAction(plans, resource); a != nil {
		s.PlanID = p.ID
		if s.ConnectionID == "" {
			s.ConnectionID = a.ConnectionID
		}
	}

	step := (*deployments.Step)(nil)
	if last != nil {
		s.DeploymentID = last.ID
		step = last.Step(string(planning.ResourceSubject) + ":" + resource)
	}
	stepDone := step != nil && step.Status == deployments.StepSucceeded
	switch {
	case last != nil && !stepDone && (last.Status == deployments.StatusRunning || last.Status == deployments.StatusPaused):
		s.Value = Deploying
	case last != nil && !stepDone && last.Status == deployments.StatusFailed:
		s.Value = Failed
		s.Message = last.Error
		if step != nil && step.Status == deployments.StepFailed && step.Message != "" {
			s.Message = step.Message
		}
		s.Message = cut(s.Message, MaxMessage)
	case bound:
		s.Value = Ready
	case s.PlanID != "":
		s.Value = Planned
	default:
		s.Value = Absent
	}
	return s, nil
}

// plannedAction returns the first open plan's action that creates or
// changes resource, and the plan. A delete doesn't count: nothing will run.
func plannedAction(plans []*planning.Plan, resource string) (*planning.Action, *planning.Plan) {
	for _, p := range plans {
		if !p.Open() {
			continue
		}
		for i := range p.Actions {
			a := &p.Actions[i]
			if a.Subject != planning.ResourceSubject || a.Name != resource {
				continue
			}
			switch a.Op {
			case planning.Create, planning.Update, planning.Replace:
				return a, p
			}
		}
	}
	return nil, nil
}

// cut returns s cut to at most n bytes, on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
