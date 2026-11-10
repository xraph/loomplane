package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// frontendResourceRecorder names, in the frontend capacity ledger, the
// provider resource a deploy made. planning.PgStore implements it.
type frontendResourceRecorder interface {
	RecordFrontendResource(ctx context.Context, org, plan, service, connection, external, replacing string) error
}

// trackFrontendResource has job record the provider resource its managed
// frontend deploy makes. Nothing else names it when the deploy fails
// before a binding does, and then the frontend's slot could never be
// released, nor its project deleted.
func (r *Runner) trackFrontendResource(job *Job) {
	rec, ok := r.plans.(frontendResourceRecorder)
	a := job.Action
	if !ok || job.Plan == nil || job.Connection == nil || a.Service == nil || a.Op == planning.Delete ||
		!cloudinfra.ManagedFrontend(a.Provider, a.Service.Workload, a.Service.Execution) {
		return
	}
	org, plan, service, connection := job.Deployment.OrgID, job.Plan.ID, a.Name, job.Connection.ID
	job.frontendResource = func(ctx context.Context, external, replacing string) error {
		return rec.RecordFrontendResource(ctx, org, plan, service, connection, external, replacing)
	}
}

// recordFrontend records external as what the job's frontend made, in
// place of replacing, what the job named and has since removed itself, if
// that isn't empty. It does nothing for a job that isn't a managed
// frontend's deploy.
func (j *Job) recordFrontend(ctx context.Context, external, replacing string) error {
	if j.frontendResource == nil {
		return nil
	}
	if err := j.frontendResource(ctx, external, replacing); err != nil {
		return fmt.Errorf("record %s in the frontend capacity ledger: %w", external, err)
	}
	return nil
}

// frontendAbsenceVerifier performs its provider read outside the cleanup transaction.
type frontendAbsenceVerifier interface {
	FrontendAbsent(context.Context, *Job, *planning.Binding) (bool, error)
}

func (v *Vercel) FrontendAbsent(ctx context.Context, job *Job, b *planning.Binding) (bool, error) {
	if b.ExternalID == "" {
		return false, errors.New("frontend project identity is missing")
	}
	if projectID, ref, native := planning.ParseVercelRef(b.ExternalID); native {
		target, err := refTarget(ref)
		if err != nil {
			return false, err
		}
		return v.nativeAbsent(ctx, token(job), projectID, target)
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
