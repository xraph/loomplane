package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// CloudSyncAuthorizer rechecks the recorded actor before each provider mutation.
type CloudSyncAuthorizer interface {
	ValidateCloudSync(context.Context, *planning.Plan) error
}

// CheckAppliedInputs proves the pinned secret and artifact remain recoverable.
func (r *Runner) CheckAppliedInputs(ctx context.Context, org, environment string, cfg *planning.ServiceConfig) error {
	if cfg.Gateway != nil {
		return nil
	}
	inputs := cfg.AppliedInputs
	if inputs == nil || inputs.Variables == "" || inputs.Source == nil || inputs.ArtifactKey == "" {
		return errors.New("applied source or variable snapshot is missing")
	}
	if !strings.HasPrefix(inputs.Variables, org+"/"+environment+"/applied-inputs/") || !strings.HasPrefix(inputs.ArtifactKey, "sources/"+org+"/") {
		return errors.New("applied input references are outside this scope")
	}
	if r.resources == nil || r.sources == nil {
		return errors.New("applied input stores are unavailable")
	}
	raw, err := r.resources.Get(ctx, inputs.Variables)
	if err != nil {
		return errors.New("the applied variable snapshot is unavailable")
	}
	var variables appliedVariables
	if json.Unmarshal(raw, &variables) != nil {
		return errors.New("the applied variable snapshot cannot be read")
	}
	file, _, err := r.sources.Open(ctx, inputs.ArtifactKey)
	if err != nil {
		return errors.New("the applied source artifact is unavailable")
	}
	return file.Close()
}

// ReconcileCloudPlan resumes the existing deployment runner and its persisted
// step checkpoints. It never constructs a provider job outside Runner.Run.
func (r *Runner) ReconcileCloudPlan(ctx context.Context, org, planID string, heartbeat func([]byte) error) (string, error) {
	plan, err := r.plans.GetPlan(ctx, org, planID)
	if err != nil {
		return "", err
	}
	if plan.CloudSyncID == "" {
		return "", errors.New("migration requires a scoped Cloud sync plan")
	}
	if authErr := r.authorizeSync(ctx, plan); authErr != nil {
		return "", authErr
	}
	id, steps, err := r.Start(ctx, org, planID)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", nil
	}
	d, err := r.store.GetDeployment(ctx, org, id)
	if err != nil {
		return id, err
	}
	if d.Status == StatusSucceeded {
		return id, nil
	}
	if d.Status == StatusFailed {
		if _, err = r.store.Reopen(context.WithValue(ctx, cloudRetryKey{}, true), org, id); err != nil {
			return id, err
		}
	}
	fail := func(e error) (string, error) { return id, errors.Join(e, r.Finish(ctx, org, id, e.Error())) }
	if err = r.FetchSource(ctx, org, id); err != nil {
		return fail(err)
	}
	for _, group := range steps {
		for _, action := range group {
			if err = r.Run(ctx, org, id, action, nil, heartbeat); err != nil {
				return fail(err)
			}
		}
	}
	return id, r.Finish(ctx, org, id, "")
}

func (r *Runner) authorizeSync(ctx context.Context, plan *planning.Plan) error {
	if plan.CloudSyncID == "" {
		return nil
	}
	if r.cloudSyncAuth == nil {
		return errors.New("the Cloud sync authorization is unavailable")
	}
	return r.cloudSyncAuth.ValidateCloudSync(ctx, plan)
}

func (r *Runner) authorizeCloudSourceCleanup(ctx context.Context, plan *planning.Plan) error {
	_, err := r.cloudSourceCleanupPrincipal(ctx, plan)
	return err
}

func (r *Runner) cloudSourceCleanupPrincipal(ctx context.Context, plan *planning.Plan) (security.Principal, error) {
	if plan.CloudSyncID == "" {
		return security.Principal{}, nil
	}
	auth, ok := r.cloudSyncAuth.(interface {
		ValidateCloudSyncSourceCleanup(context.Context, *planning.Plan) (security.Principal, error)
	})
	if !ok {
		return security.Principal{}, errors.New("the Cloud source cleanup authorization is unavailable")
	}
	return auth.ValidateCloudSyncSourceCleanup(ctx, plan)
}

// CleanupCloudPlan runs only after every managed consumer and external route
// has completed. Each old binding keeps its original credential and checkpoint.
func (r *Runner) CleanupCloudPlan(ctx context.Context, org, planID string, heartbeat func([]byte) error) error {
	ctx, _, stop := keepHeartbeat(ctx, nil, heartbeat, 30*time.Second)
	defer stop()
	plan, err := r.plans.GetPlan(ctx, org, planID)
	if err != nil {
		return err
	}
	if plan.CloudSyncID == "" {
		return errors.New("cleanup requires a Cloud sync plan")
	}
	actor, authErr := r.cloudSourceCleanupPrincipal(ctx, plan)
	if authErr != nil {
		return authErr
	}
	d, err := r.store.ForPlan(ctx, org, planID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := reviewedActionsComplete(d, plan); err != nil {
		return err
	}
	for _, a := range plan.Actions {
		if (a.Op == planning.Keep && (a.Compensation == nil || !a.Compensation.Retained)) || a.Op == planning.Delete {
			continue
		}
		if err := r.finalizeReleaseAction(ctx, d, plan, a, true, nil, &actor); err != nil {
			return err
		}
	}

	return nil
}

type cloudRetryKey struct{}
