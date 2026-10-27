package deployments

import (
	"context"
	"crypto/rand"
	"errors"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
)

func retainedDatabaseSecret(ctx context.Context, job *Job, field string) (string, error) {
	if job.Recall == nil || job.Keep == nil {
		return "", ErrNoCredential
	}
	existing, err := job.Recall(ctx, field)
	if err == nil && len(existing) > 0 {
		return string(existing), nil
	}
	if err != nil && !errors.Is(err, ErrNoCredential) {
		return "", err
	}
	if job.Binding != nil {
		return "", errors.New("the deployed database credential is missing; recover it before changing the service")
	}
	value := rand.Text() + rand.Text()
	if checkErr := job.Keep(ctx, field, []byte(value)); checkErr != nil {
		return "", checkErr
	}
	return value, nil
}

func (e *OwnedTarget) applyDatabase(ctx context.Context, job *Job) (*Outcome, error) {
	if !planning.OwnedCloud(e.Cloud) || job.Connection == nil || job.Deployment == nil {
		return nil, ErrNoExecutor
	}
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	cfg, err := databaseService(job.Action.Resource)
	if err != nil {
		return nil, err
	}
	if cfg.Provider != e.Cloud || job.Connection.Cloud != e.Cloud || cfg.Target != job.Connection.ID {
		return nil, errors.New("database target differs from the reviewed connection")
	}
	appPassword, err := retainedDatabaseSecret(ctx, job, passwordField)
	if err != nil {
		return nil, err
	}
	adminPassword, err := retainedDatabaseSecret(ctx, job, adminPasswordField)
	if err != nil {
		return nil, err
	}
	copyJob := *job
	copyJob.Action.Resource = nil
	copyJob.Action.Service = cfg
	copyJob.Env, copyJob.Secrets = databaseEnvironment(cfg.Catalog.Engine(), databaseName(job.Action.Name), appPassword, adminPassword)
	out, err := e.Apply(ctx, &copyJob)
	if err != nil {
		return nil, err
	}
	iid, err := id.Parse(out.ExternalID)
	if err != nil {
		return nil, err
	}
	inst, err := e.Ctrl.Instances.Get(ctx, iid)
	if err != nil {
		return nil, err
	}
	backend, err := e.registeredBackend(e.Register(job.Deployment.OrgID, job.Connection.ID))
	if err != nil {
		return nil, err
	}
	database, ok := backend.(owned.DatabaseBackend)
	if !ok {
		return nil, errors.New("the target adapter cannot configure the database")
	}
	if checkErr := database.ConfigureDatabase(ctx, iid, inst.Labels, appPassword); checkErr != nil {
		return nil, checkErr
	}
	address, err := databaseAddress(job.Action.Resource, iid, databaseName(job.Action.Name), appPassword)
	if err != nil {
		return nil, err
	}
	if checkErr := job.Keep(ctx, ResourceURL, []byte(address)); checkErr != nil {
		return nil, checkErr
	}
	return out, nil
}
