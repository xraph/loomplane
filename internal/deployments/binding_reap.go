package deployments

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// resourceCredentialFields are the fields a deployed action may keep in
// Vault under its own name: a resource's address, its application password,
// the administrator's password of a database Loomplane runs itself, and
// the explicit SDK output fields of an attached bucket.
var resourceCredentialFields = append([]string{ResourceURL, passwordField, adminPasswordField}, bucketCredentialFields()...)

// BindingReaper cleans up after the services and resources deployed in a
// project or environment being deleted. The deletion takes every binding
// with it, by a cascade that can't reach Vault or a cloud, so it reads them
// first. Once the deletion has happened it deletes every credential the
// environment kept in Vault: each resource's address and passwords, the
// variables each deployment pinned, the runtime values of a service on a
// server or cluster of yours, and whatever a deployment that failed left
// there. What each binding deployed is out of reach by then: deleting it
// takes a deployment, through the connection that deployed it, and the
// binding that said where it runs is gone. So it warns, naming where each
// one runs, that it's yours to delete now, as a deployment does for a
// leftover it can't remove. A database gateway's binding gets its warning
// from DatabaseGatewayReaper, so it gets none here. It implements
// projects.Reaper.
type BindingReaper struct {
	// Bindings reads what's deployed in each environment.
	Bindings interface {
		ListBindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	}
	// Environments lists a project's environments.
	Environments interface {
		ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error)
	}
	// Secrets keeps the resources' credentials. It may be nil when nothing
	// is provisioned. When it's a ResourceSecretKeys every key under the
	// environment goes. Otherwise only each binding's own credentials do,
	// since nothing else can be found.
	Secrets ResourceSecrets
	// Executors name where a leftover runs, by provider name. It may be nil.
	Executors map[string]Executor
	// Warn logs a leftover, or a step that failed. It may be nil.
	Warn func(msg string, err error)
}

var _ projects.Reaper = (*BindingReaper)(nil)

// Reap implements projects.Reaper. It reads the bindings of the project's
// environments, or of environmentID alone when that isn't empty, and
// returns what cleans up after them. An environment with nothing bound
// still has credentials to delete when Secrets can list them, such as the
// variables a deployment that never finished pinned.
func (br *BindingReaper) Reap(ctx context.Context, orgID, projectID, environmentID string) func(context.Context) {
	envs, err := br.Environments.ListEnvironments(ctx, orgID, projectID)
	if err != nil {
		br.warn("read the environments being deleted, so their credentials stay in Vault", err)
		return nil
	}
	_, sweeps := br.Secrets.(ResourceSecretKeys)
	var found []reapedEnvironment
	for _, e := range envs {
		if environmentID != "" && e.ID != environmentID {
			continue
		}
		bindings, berr := br.Bindings.ListBindings(ctx, orgID, e.ID)
		if berr != nil {
			br.warn(fmt.Sprintf("read what's deployed in the environment %s being deleted, so what it leaves running isn't reported", e.Name), berr)
		}
		if len(bindings) > 0 || sweeps {
			found = append(found, reapedEnvironment{org: orgID, id: e.ID, name: e.Name, bindings: bindings})
		}
	}
	if len(found) == 0 {
		return nil
	}
	return func(ctx context.Context) {
		for _, r := range found {
			br.finish(ctx, r)
		}
	}
}

// finish runs once the environment is gone: it deletes the environment's
// credentials and warns about what each binding deployed, and about what a
// move left running.
func (br *BindingReaper) finish(ctx context.Context, r reapedEnvironment) {
	br.forget(ctx, r)
	for _, b := range r.bindings {
		if databaseGatewayOf(b) != nil || planning.ExistingResourceBinding(b) {
			continue
		}
		for _, left := range append([]planning.Binding{*b}, b.Replaced...) {
			br.warn(fmt.Sprintf("a deleted environment's %s may still run", b.Subject),
				fmt.Errorf("the %s %s went with the environment %s, and Loomplane no longer tracks it. If %s still runs, delete it yourself, in the account that deployed it",
					b.Subject, b.Name, r.name, locate(br.Executors[left.Provider], &left)))
		}
	}
}

// forget deletes what the environment kept in Vault: each binding's own
// credentials, then every key still under it when Secrets can list them.
func (br *BindingReaper) forget(ctx context.Context, r reapedEnvironment) {
	if br.Secrets == nil {
		return
	}
	// Each binding's own credentials go first: a resource's inventory says
	// which fields it kept, so the sweep mustn't delete it before it's read.
	for _, b := range r.bindings {
		if b.Subject == planning.ResourceSubject {
			br.reapResourceAccess(ctx, r, b.Name)
			continue
		}
		for _, field := range resourceCredentialFields {
			if err := br.Secrets.Delete(ctx, resourceKey(r.org, r.id, b.Name, field)); err != nil {
				br.warn(fmt.Sprintf("delete the credentials of %s, whose environment %s was deleted", b.Name, r.name), err)
			}
		}
	}
	if keys, ok := br.Secrets.(ResourceSecretKeys); ok {
		br.sweep(ctx, keys, r)
	}
}

// sweep deletes every key still under the environment, and reports whether
// it could list them.
func (br *BindingReaper) sweep(ctx context.Context, keys ResourceSecretKeys, r reapedEnvironment) bool {
	prefix, err := environmentKeys(r.org, r.id)
	if err == nil {
		var found []string
		if found, err = keys.Keys(ctx, prefix); err == nil {
			for _, key := range found {
				if derr := br.Secrets.Delete(ctx, key); derr != nil {
					br.warn(fmt.Sprintf("delete the credential %s, whose environment %s was deleted", key, r.name), derr)
				}
			}
			return true
		}
	}
	br.warn(fmt.Sprintf("list the credentials of the environment %s that was deleted, so only its deployments' own go", r.name), err)
	return false
}

func (br *BindingReaper) warn(msg string, err error) {
	if br.Warn != nil {
		br.Warn(msg, err)
	}
}
