package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/planning"
)

type managedActionCleanupKey struct{}

type managedActionCleanup struct {
	targets   []planning.ManagedCleanupTarget
	selection planning.ManagedCleanupSelection
	grants    []planning.ManagedCleanupGrant
}

func (m *managedActionCleanup) removed(b *planning.Binding) bool {
	if b == nil {
		return false
	}
	for _, g := range m.grants {
		s := g.Target.Scope
		if s.Org == b.OrgID && s.Project == b.ProjectID && s.Environment == b.EnvironmentID && s.Service == b.Name && s.Instance == b.ExternalID && s.Connection == b.ConnectionID {
			return true
		}
	}
	return false
}

func (m *managedActionCleanup) reviewed(b *planning.Binding) bool {
	for _, t := range m.targets {
		s := t.Scope
		if s.Org != b.OrgID || s.Project != b.ProjectID || s.Environment != b.EnvironmentID || s.Service != b.Name || s.Connection != b.ConnectionID || s.Instance != b.ExternalID {
			continue
		}
		if s.Plan == b.PlanID {
			return true
		}
		for _, ref := range t.BindingRefs {
			if ref.Binding.ID == b.ID && ref.Binding.PlanID == b.PlanID && ref.Binding.Revision == b.Revision {
				return true
			}
		}
	}
	return false
}

func (r *Runner) ensureManagedActionCleanup(ctx context.Context, d *Deployment, p *planning.Plan, a planning.Action, resume []byte, heartbeat func([]byte) error) (context.Context, *managedActionCleanup, error) {
	if len(a.CleanupTargets) == 0 {
		return ctx, nil, nil
	}
	sel := planning.ManagedCleanupSelection{Org: d.OrgID, Plan: p.ID, Deployment: d.ID, Action: a.ID}
	if completed, ok := ctx.Value(managedActionCleanupKey{}).(*managedActionCleanup); ok && completed.selection == sel {
		return ctx, completed, nil
	}
	if heartbeat == nil || r.managedCleanup == nil {
		return ctx, nil, planning.ErrManagedCleanupUnavailable
	}
	check := func() error { return heartbeat(resume) }
	grants, err := r.managedCleanup.execute(ctx, sel, check)
	if err != nil {
		return ctx, nil, err
	}
	// The retired receipt retains the verified runtime ref across a crash;
	// retain the enclosing action's own activity checkpoint during metadata work.
	job := &Job{Deployment: d, Plan: p, Action: a, Checkpoint: func([]byte) error { return check() }}
	if metadataErr := r.managedCleanup.removeMetadata(ctx, job, grants); metadataErr != nil {
		return ctx, nil, metadataErr
	}
	targets, readErr := r.managedCleanup.Store.ReviewedCleanupTargets(ctx, sel)
	if readErr != nil {
		return ctx, nil, readErr
	}
	result := &managedActionCleanup{selection: sel, grants: grants, targets: targets}
	return context.WithValue(ctx, managedActionCleanupKey{}, result), result, nil
}

func (r *Runner) managedDelete(ctx context.Context, d *Deployment, p *planning.Plan, a planning.Action, current *planning.Binding, step *Step, resume []byte, heartbeat func([]byte) error) (context.Context, bool, error) {
	if current != nil && len(current.Config) > 0 {
		var cfg planning.ServiceConfig
		if decodeErr := json.Unmarshal(current.Config, &cfg); decodeErr != nil {
			return ctx, true, decodeErr
		}
		if checkErr := cfg.CheckManagedServiceFinalizers(); checkErr != nil {
			return ctx, true, checkErr
		}
	}
	ctx, state, err := r.ensureManagedActionCleanup(ctx, d, p, a, resume, heartbeat)
	if err != nil {
		return ctx, true, err
	}
	managed, err := r.managedCleanup.Store.GuardManagedDeleteBinding(ctx, state.selection, current, false)
	if err != nil {
		return ctx, true, err
	}
	if current == nil {
		return ctx, true, nil
	}
	if !managed {
		return ctx, false, nil
	}
	if !state.removed(current) {
		return ctx, true, planning.ErrManagedCleanupBindingChanged
	}
	job := &Job{Deployment: d, Plan: p, Action: a, Binding: current, Checkpoint: heartbeat, Progress: func(string, string) {}, Forget: func(context.Context) error {
		return errors.New("retained workload credentials require their original connection")
	}}
	if replaceErr := r.removeReplaced(ctx, job, current, nil, nil, readReplaceCheckpoint(resume), heartbeat); replaceErr != nil {
		return ctx, true, replaceErr
	}
	if _, err = r.managedCleanup.Store.GuardManagedDeleteBinding(ctx, state.selection, current, true); err != nil {
		return ctx, true, err
	}
	step.Warnings = append(step.Warnings, r.released(ctx, d, current, true)...)
	return ctx, true, nil
}

func (r *Runner) guardUnreviewedRemoval(ctx context.Context, b *planning.Binding) error {
	source, ok := r.plans.(interface {
		RequiresReviewedRemoval(context.Context, *planning.Binding) (bool, error)
	})
	if !ok {
		return nil
	}
	required, err := source.RequiresReviewedRemoval(ctx, b)
	if err != nil {
		return err
	}
	if required {
		return ErrManagedCleanupReviewRequired
	}
	return nil
}
