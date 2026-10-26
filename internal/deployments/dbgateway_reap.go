package deployments

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// DatabaseGatewayReaper cleans up after the database gateways of a project
// or environment being deleted. The deletion takes each gateway's record and
// its binding with it, by a cascade that can't reach Vault or a cloud, so it
// reads both first. Once the deletion has happened it deletes each gateway's
// private keys from Vault. The gateway's cloud service is out of reach by
// then: deleting it takes a deployment, through the connection that deployed
// it, and the binding that said where it runs is gone. So it warns, naming
// where the service runs, that it's yours to delete now, as a deployment
// does for a leftover it can't remove. It implements projects.Reaper.
type DatabaseGatewayReaper struct {
	// Gateways reads each environment's recorded gateways.
	Gateways interface {
		Gateways(ctx context.Context, org, environmentID string) ([]dbgateway.Gateway, error)
	}
	// Bindings reads what's deployed in each environment.
	Bindings interface {
		ListBindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	}
	// Environments lists a project's environments.
	Environments interface {
		ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error)
	}
	// Secrets keeps the gateways' private keys. It may be nil when nothing
	// is provisioned.
	Secrets ResourceSecrets
	// Executors name where a leftover runs, by provider name. It may be nil.
	Executors map[string]Executor
	// Warn logs a leftover, or a step that failed. It may be nil.
	Warn func(msg string, err error)
}

var _ projects.Reaper = (*DatabaseGatewayReaper)(nil)

// reapedEnvironment is what one environment being deleted leaves behind.
type reapedEnvironment struct {
	org, id, name string
	gateways      []dbgateway.Gateway
	bindings      []*planning.Binding
}

// Reap implements projects.Reaper. It reads the gateways and gateway
// bindings of the project's environments, or of environmentID alone when
// that isn't empty, and returns what cleans up after them.
func (g *DatabaseGatewayReaper) Reap(ctx context.Context, orgID, projectID, environmentID string) func(context.Context) {
	envs, err := g.Environments.ListEnvironments(ctx, orgID, projectID)
	if err != nil {
		g.warn("read the environments being deleted, so their database gateways' keys stay in Vault", err)
		return nil
	}
	var found []reapedEnvironment
	for _, e := range envs {
		if environmentID != "" && e.ID != environmentID {
			continue
		}
		r := reapedEnvironment{org: orgID, id: e.ID, name: e.Name}
		if r.gateways, err = g.Gateways.Gateways(ctx, orgID, e.ID); err != nil {
			g.warn(fmt.Sprintf("read the database gateways of the environment %s being deleted, so their keys stay in Vault", e.Name), err)
		}
		bindings, berr := g.Bindings.ListBindings(ctx, orgID, e.ID)
		if berr != nil {
			g.warn(fmt.Sprintf("read what's deployed in the environment %s being deleted, so a database gateway it leaves running isn't reported", e.Name), berr)
		}
		for _, b := range bindings {
			if databaseGatewayOf(b) != nil {
				r.bindings = append(r.bindings, b)
			}
		}
		if len(r.gateways) > 0 || len(r.bindings) > 0 {
			found = append(found, r)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return func(ctx context.Context) {
		for _, r := range found {
			g.finish(ctx, r)
		}
	}
}

// finish runs once the environment is gone: it deletes each gateway's keys
// and warns about each gateway service still deployed, and about each one
// a move left running.
func (g *DatabaseGatewayReaper) finish(ctx context.Context, r reapedEnvironment) {
	if g.Secrets != nil {
		for _, gw := range r.gateways {
			for _, field := range []string{dbGatewaySigningField, dbGatewaySealingField} {
				if err := g.Secrets.Delete(ctx, dbGatewayKey(r.org, r.id, gw.ID, field)); err != nil {
					g.warn(fmt.Sprintf("delete the keys of the database gateway %s (%s), whose environment %s was deleted", gw.Name, gw.ID, r.name), err)
				}
			}
		}
	}
	for _, b := range r.bindings {
		name := databaseGatewayOf(b).Name
		for _, left := range append([]planning.Binding{*b}, b.Replaced...) {
			g.warn("a deleted environment's database gateway may still run",
				fmt.Errorf("the database gateway %s went with the environment %s, and Loomplane no longer tracks it. If %s still runs, delete it yourself, in the account that deployed it",
					name, r.name, locate(g.Executors[left.Provider], &left)))
		}
	}
}

func (g *DatabaseGatewayReaper) warn(msg string, err error) {
	if g.Warn != nil {
		g.Warn(msg, err)
	}
}
