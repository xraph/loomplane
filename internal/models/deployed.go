package models

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/page"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// Deployed reads what deployments left behind for a model resource: its
// state, the bindings of its environment, the guard's address and the
// model's key. *DeployedFacts is the real one. Nothing here checks a
// permission, and every read is scoped to the organization: the service
// checks the caller first.
type Deployed interface {
	// State is the resource's state in the environment, from deployment
	// facts (catalogstate).
	State(ctx context.Context, orgID, projectID, environmentID, resource string) (catalogstate.State, error)
	// Bindings lists what's bound in the environment.
	Bindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	// URL is the guard's address, what linked services get as MODEL_URL,
	// or "" when no deployment kept one.
	URL(ctx context.Context, orgID, environmentID, resource string) (string, error)
	// APIKey is the model's key, what linked services get as
	// MODEL_API_KEY. It's a secret: it goes to a gateway, never to a page,
	// a log or an error.
	APIKey(ctx context.Context, orgID, projectID, environmentID, resource string) (string, error)
}

// Secrets reads what a deployment kept for a resource. deployments'
// ResourceSecrets is the real one.
type Secrets interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// InternalSecrets reads a composed resource's generated secrets for
// Loomplane's own work. *deployments.CatalogAccess is the real one.
type InternalSecrets interface {
	InternalSecret(ctx context.Context, orgID string, ref deployments.ResourceRef, name string) ([]byte, error)
}

var _ InternalSecrets = (*deployments.CatalogAccess)(nil)

// How far back DeployedFacts looks.
const (
	// openPlansRead is how many of an environment's newest plans are read
	// for one that's still open.
	openPlansRead = 50
	// runsRead is how many of a project's newest deployments are read for
	// the last one that ran a resource.
	runsRead = 200
	runsPage = 50
)

// DeployedFacts is Deployed over the planning and deployments stores.
type DeployedFacts struct {
	// ListBindings is planning's PgStore.ListBindings.
	ListBindings func(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	// ListPlans is planning's PgStore.ListPlans.
	ListPlans func(ctx context.Context, orgID, projectID, environmentID string, before page.Cursor, limit int) ([]*planning.Plan, error)
	// ListRuns is deployments' PgStore.ListDeployments.
	ListRuns func(ctx context.Context, orgID, projectID string, before page.Cursor, limit int) ([]*deployments.Deployment, error)
	// Secrets holds the addresses deployments keep.
	Secrets Secrets
	// Generated reads the model's key.
	Generated InternalSecrets
}

var _ Deployed = (*DeployedFacts)(nil)

// State implements Deployed.
func (f *DeployedFacts) State(ctx context.Context, orgID, projectID, environmentID, resource string) (catalogstate.State, error) {
	return catalogstate.Of(ctx, catalogstate.Deps{
		Bindings: f.ListBindings,
		OpenPlans: func(ctx context.Context, orgID, environmentID string) ([]*planning.Plan, error) {
			plans, err := f.ListPlans(ctx, orgID, projectID, environmentID, page.Cursor{}, openPlansRead)
			if err != nil {
				return nil, err
			}
			open := plans[:0]
			for _, p := range plans {
				if p.Open() {
					open = append(open, p)
				}
			}
			return open, nil
		},
		LastRun: func(ctx context.Context, orgID, environmentID, resource string) (*deployments.Deployment, error) {
			return f.lastRun(ctx, orgID, projectID, environmentID, resource)
		},
	}, orgID, environmentID, resource)
}

// lastRun is the newest of the project's last runsRead deployments that
// has a step for the resource in the environment.
func (f *DeployedFacts) lastRun(ctx context.Context, orgID, projectID, environmentID, resource string) (*deployments.Deployment, error) {
	action := string(planning.ResourceSubject) + ":" + resource
	var before page.Cursor
	for read := 0; read < runsRead; {
		runs, err := f.ListRuns(ctx, orgID, projectID, before, runsPage)
		if err != nil {
			return nil, err
		}
		for _, d := range runs {
			if d.EnvironmentID == environmentID && d.Step(action) != nil {
				return d, nil
			}
		}
		if len(runs) < runsPage {
			break
		}
		read += len(runs)
		last := runs[len(runs)-1]
		before = page.Cursor{At: last.StartedAt, ID: last.ID}
	}
	return nil, deployments.ErrNotFound
}

// Bindings implements Deployed.
func (f *DeployedFacts) Bindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error) {
	return f.ListBindings(ctx, orgID, environmentID)
}

// URL implements Deployed.
func (f *DeployedFacts) URL(ctx context.Context, orgID, environmentID, resource string) (string, error) {
	if f.Secrets == nil {
		return "", nil
	}
	v, err := f.Secrets.Get(ctx, deployments.ResourceURLKey(orgID, environmentID, resource))
	if errors.Is(err, deployments.ErrNoCredential) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s's address: %w", resource, err)
	}
	return string(v), nil
}

// APIKey implements Deployed.
func (f *DeployedFacts) APIKey(ctx context.Context, orgID, projectID, environmentID, resource string) (string, error) {
	if f.Generated == nil {
		return "", errors.New("models: the model keys aren't configured")
	}
	v, err := f.Generated.InternalSecret(ctx, orgID, deployments.ResourceRef{ProjectID: projectID, EnvironmentID: environmentID, Resource: resource}, recipes.ModelAPIKey)
	if err != nil {
		// No error from the read holds the key's value.
		return "", fmt.Errorf("read %s's key: %w", resource, err)
	}
	return string(v), nil
}
