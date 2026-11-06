package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// frontendAbsenceVerifier performs its provider read outside the cleanup transaction.
type frontendAbsenceVerifier interface {
	FrontendAbsent(context.Context, *Job, *planning.Binding) (bool, error)
}

func (v *Vercel) FrontendAbsent(ctx context.Context, job *Job, b *planning.Binding) (bool, error) {
	if b.ExternalID == "" {
		return false, errors.New("frontend project identity is missing")
	}
	if projectID, ref, native := planning.ParseVercelRef(b.ExternalID); native {
		return v.nativeAbsent(ctx, token(job), projectID, refTarget(ref))
	}
	_, err := v.Client.GetProject(ctx, token(job), b.ExternalID)
	if vercel.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

// nativeAbsent: the project is gone, or its one environment is: a custom
// one that 404s, or a built-in one with no deployments.
func (v *Vercel) nativeAbsent(ctx context.Context, tok vercel.Token, projectID string, target vercel.Target) (bool, error) {
	if _, err := v.Client.GetProject(ctx, tok, projectID); err != nil {
		return vercel.IsNotFound(err), ignoreNotFound(err)
	}
	if target.CustomID != "" {
		_, err := v.Client.GetCustomEnvironment(ctx, tok, projectID, target.CustomID)
		return vercel.IsNotFound(err), ignoreNotFound(err)
	}
	deployments, err := v.Client.TargetDeployments(ctx, tok, projectID, target)
	return err == nil && len(deployments) == 0, err
}

func ignoreNotFound(err error) error {
	if vercel.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *Runner) confirmFrontendRemoval(ctx context.Context, exec Executor, job *Job, b *planning.Binding) error {
	verifier, ok := exec.(frontendAbsenceVerifier)
	if !ok {
		return nil
	}
	recorder, ok := r.plans.(interface {
		RecordFrontendAbsence(context.Context, cloudinfra.FrontendAbsence) error
	})
	if !ok {
		return nil
	}
	var cfg planning.ServiceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		return err
	}
	if !cloudinfra.ManagedFrontend(b.Provider, cfg.Workload, cfg.Execution) {
		return nil
	}

	account, err := frontendCleanupAccount(job, b)
	if err != nil {
		return err
	}
	absent, err := verifier.FrontendAbsent(ctx, job, b)
	if err != nil {
		return err
	}
	if !absent {
		return errors.New("frontend provider removal is not confirmed")
	}
	return recorder.RecordFrontendAbsence(ctx, cloudinfra.FrontendAbsence{OrgID: b.OrgID, ProjectID: b.ProjectID, EnvironmentID: b.EnvironmentID, Service: b.Name, Provider: b.Provider, ConnectionID: b.ConnectionID, AccountID: account, ExternalID: b.ExternalID, PlanID: b.PlanID})
}

func frontendCleanupAccount(job *Job, b *planning.Binding) (string, error) {
	var cfg planning.ServiceConfig
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		return "", err
	}
	if !cloudinfra.ManagedFrontend(b.Provider, cfg.Workload, cfg.Execution) {
		return "", nil
	}
	if job.Connection == nil || job.Connection.ID != b.ConnectionID {
		return "", errors.New("frontend cleanup connection identity is missing")
	}
	account := connections.AccountIdentity(job.Connection.Cloud, job.Connection.Account, job.Connection.Details)
	if account == "" || cfg.CloudPlacement != nil && cfg.CloudPlacement.AccountIdentity != account || cfg.FrontendDemand != nil && cfg.FrontendDemand.AccountID != account {
		return "", errors.New("frontend cleanup account identity changed")
	}
	return account, nil
}

func validateFrontendCleanup(exec Executor, job *Job, b *planning.Binding) error {
	if _, ok := exec.(frontendAbsenceVerifier); !ok {
		return nil
	}
	_, err := frontendCleanupAccount(job, b)
	return err
}
