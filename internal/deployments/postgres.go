package deployments

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"reflect"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

func postgresService(cfg *planning.ResourceConfig) (*planning.ServiceConfig, error) {
	if cfg == nil || cfg.Kind != "postgres" || cfg.Provider != "server" || cfg.Target == "" || cfg.Catalog == nil {
		return nil, errors.New("PostgreSQL needs a reviewed server recipe")
	}
	expected, err := recipes.Postgres(cfg.Version, cfg.TargetSettings["architecture"])
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(cfg.Catalog, expected) {
		return nil, errors.New("the PostgreSQL recipe changed; create a new plan")
	}
	return &planning.ServiceConfig{Provider: cfg.Provider, Target: cfg.Target, TargetSettings: cfg.TargetSettings, Workload: provider.Worker, Execution: provider.Container, Port: 5432, Catalog: cfg.Catalog, BuildTarget: &planning.BuildTarget{Image: cfg.Catalog.Image}}, nil
}

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

func (e *OwnedTarget) applyPostgres(ctx context.Context, job *Job) (*Outcome, error) {
	if e.Cloud != connections.Server || job.Connection == nil || job.Deployment == nil {
		return nil, ErrNoExecutor
	}
	ctx = infrastructure.WithPrincipal(ctx, principal(job))
	cfg, err := postgresService(job.Action.Resource)
	if err != nil {
		return nil, err
	}
	if cfg.Target != job.Connection.ID {
		return nil, errors.New("PostgreSQL target differs from the reviewed connection")
	}
	appPassword, err := retainedDatabaseSecret(ctx, job, passwordField)
	if err != nil {
		return nil, err
	}
	adminPassword, err := retainedDatabaseSecret(ctx, job, "admin-password")
	if err != nil {
		return nil, err
	}
	copyJob := *job
	copyJob.Action.Resource = nil
	copyJob.Action.Service = cfg
	copyJob.Env = map[string]string{"POSTGRES_DB": databaseName(job.Action.Name), "POSTGRES_PASSWORD": adminPassword}
	copyJob.Secrets = map[string]bool{"POSTGRES_PASSWORD": true}
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
	database, ok := backend.(interface {
		ConfigurePostgres(context.Context, id.ID, map[string]string, string) error
	})
	if !ok {
		return nil, errors.New("the server adapter cannot configure PostgreSQL")
	}
	if checkErr := database.ConfigurePostgres(ctx, iid, inst.Labels, appPassword); checkErr != nil {
		return nil, checkErr
	}
	address := fmt.Sprintf("postgresql://%s@%s:5432/%s?sslmode=disable", url.UserPassword(sqlUser, appPassword).String(), server.ContainerName(iid), databaseName(job.Action.Name))
	if checkErr := job.Keep(ctx, ResourceURL, []byte(address)); checkErr != nil {
		return nil, checkErr
	}
	return out, nil
}
