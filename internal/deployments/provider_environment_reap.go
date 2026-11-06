package deployments

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// ProviderEnvironmentReaper deletes what Loomplane made on Vercel and
// Render for an environment being deleted, native or fallback alike: each
// service and datastore, a datastore with its data, through the executor
// and connection that deployed it. The executor's Remove takes the rest:
// a Vercel service's variables and custom environment, and a Render
// environment, then its project, once nothing else is in them. What the
// caller keeps stays running, and so does what fails to go, and both are
// reported as left behind, as BindingReaper reports what runs elsewhere.
// A whole project's deletion removes nothing. The deletion takes the
// bindings and the provider environments' records with it, so it reads
// them first, and Render's Remove gets the record it read. It implements
// projects.ReaperWithOptions.
type ProviderEnvironmentReaper struct {
	// Bindings reads what's deployed in the environment.
	Bindings interface {
		ListBindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	}
	// Environments lists a project's environments, for the deleted one's
	// name.
	Environments interface {
		ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error)
	}
	// Records lists a project's provider environments.
	Records interface {
		ProviderEnvironments(ctx context.Context, orgID, projectID string) ([]*ProviderEnvironment, error)
	}
	// Credentials reads the connection that deployed each binding.
	Credentials Credentials
	// Executors remove what each binding names, by provider name.
	Executors map[string]Executor
	// Warn logs a leftover, or a step that failed. It may be nil.
	Warn func(msg string, err error)
}

var _ projects.ReaperWithOptions = (*ProviderEnvironmentReaper)(nil)

// Removes reports whether the reaper takes b when its environment is
// deleted: a service or datastore Loomplane made on Vercel or Render. A
// database gateway has a reaper of its own, and an existing resource isn't
// Loomplane's to delete.
func (pr *ProviderEnvironmentReaper) Removes(b *planning.Binding) bool {
	return (b.Provider == "vercel" || b.Provider == "render") &&
		databaseGatewayOf(b) == nil && !planning.ExistingResourceBinding(b)
}

// Reap implements projects.Reaper: it removes everything, keeping nothing.
func (pr *ProviderEnvironmentReaper) Reap(ctx context.Context, orgID, projectID, environmentID string) func(context.Context) {
	return pr.ReapWithOptions(ctx, orgID, projectID, environmentID, projects.DeleteEnvironmentOptions{}).Cleanup
}

// ReapWithOptions implements projects.ReaperWithOptions. It reads the
// environment's Vercel and Render bindings and records, and returns what
// removes each binding opts doesn't keep, with the IDs of those it removes
// and keeps.
func (pr *ProviderEnvironmentReaper) ReapWithOptions(ctx context.Context, orgID, projectID, environmentID string, opts projects.DeleteEnvironmentOptions) projects.Reaped {
	if environmentID == "" {
		return projects.Reaped{}
	}
	name := environmentID
	envs, err := pr.Environments.ListEnvironments(ctx, orgID, projectID)
	if err != nil {
		pr.warn("read the name of the environment being deleted", err)
	}
	for _, e := range envs {
		if e.ID == environmentID {
			name = e.Name
		}
	}
	bindings, err := pr.Bindings.ListBindings(ctx, orgID, environmentID)
	if err != nil {
		pr.warn(fmt.Sprintf("read what's deployed in the environment %s being deleted, so what it runs on Vercel and Render keeps running", name), err)
		return projects.Reaped{}
	}
	var owned []*planning.Binding
	for _, b := range bindings {
		if pr.Removes(b) {
			owned = append(owned, b)
		}
	}
	if len(owned) == 0 {
		return projects.Reaped{}
	}
	// A service goes before the databases it may use.
	service := func(b *planning.Binding) int {
		if b.Subject == planning.ServiceSubject {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(owned, func(a, b *planning.Binding) int { return cmp.Compare(service(a), service(b)) })
	records := map[string]*ProviderEnvironment{}
	all, err := pr.Records.ProviderEnvironments(ctx, orgID, projectID)
	if err != nil {
		pr.warn(fmt.Sprintf("read where the environment %s being deleted deploys on Render, so a Render environment it leaves empty stays", name), err)
	}
	for _, pe := range all {
		if pe.EnvironmentID == environmentID {
			records[recordOf(pe.Provider, pe.ConnectionID, pe.Scope)] = pe
		}
	}
	keep := map[string]bool{}
	for _, id := range opts.Keep {
		keep[id] = true
	}
	var out projects.Reaped
	for _, b := range owned {
		if keep[b.ID] {
			out.Keeps = append(out.Keeps, b.ID)
		} else {
			out.Removes = append(out.Removes, b.ID)
		}
	}
	d := &Deployment{OrgID: orgID, ProjectID: projectID, EnvironmentID: environmentID, Environment: name}
	out.Cleanup = func(ctx context.Context) {
		for _, b := range owned {
			if keep[b.ID] {
				reportLeft(pr.warn, pr.Executors, b, name, append([]planning.Binding{*b}, b.Replaced...))
				continue
			}
			if err := pr.remove(ctx, d, b, records[recordOf(b.Provider, b.ConnectionID, recordScope(b))]); err != nil {
				pr.warn(fmt.Sprintf("remove the %s %s of the deleted environment %s", b.Subject, b.Name, name), err)
				reportLeft(pr.warn, pr.Executors, b, name, append([]planning.Binding{*b}, b.Replaced...))
				continue
			}
			// What a move left running elsewhere isn't this reaper's.
			reportLeft(pr.warn, pr.Executors, b, name, b.Replaced)
		}
	}
	return out
}

// remove removes what b names through the executor and connection that
// deployed it. rec is the record of where its environment deployed on the
// provider, or nil.
func (pr *ProviderEnvironmentReaper) remove(ctx context.Context, d *Deployment, b *planning.Binding, rec *ProviderEnvironment) error {
	exec, ok := pr.Executors[b.Provider]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoExecutor, b.Provider)
	}
	if b.ConnectionID == "" || pr.Credentials == nil {
		return errors.New("nothing says which connection deployed it")
	}
	conn, cred, err := pr.Credentials.Credential(ctx, d.OrgID, b.ConnectionID)
	if err != nil {
		return fmt.Errorf("read the connection that deployed it: %w", err)
	}
	job := &Job{
		Deployment: d, Connection: conn, Credential: cred, Binding: b, record: rec,
		Action: planning.Action{
			Op: planning.Delete, Subject: b.Subject, Name: b.Name,
			Provider: b.Provider, Region: b.Region, ConnectionID: b.ConnectionID,
		},
		Progress:   func(string, string) {},
		Checkpoint: func([]byte) error { return nil },
		Keep: func(context.Context, string, []byte) error {
			return errors.New("removing a deleted environment's deployment keeps nothing")
		},
		Recall: func(context.Context, string) ([]byte, error) { return nil, ErrNoCredential },
		// BindingReaper deletes what the environment kept in Vault.
		Forget: func(context.Context) error { return nil },
		warn: func(err error) {
			pr.warn(fmt.Sprintf("remove the %s %s of the deleted environment %s", b.Subject, b.Name, d.Environment), err)
		},
	}
	return exec.Remove(ctx, job, b)
}

// recordOf keys a provider environment's record within one environment.
func recordOf(provider, connectionID, scope string) string {
	return provider + "|" + connectionID + "|" + scope
}

// recordScope is the scope of the record b was deployed under: its own
// name on Vercel, where each service has a project, and none on Render.
func recordScope(b *planning.Binding) string {
	if b.Provider == "vercel" {
		return b.Name
	}
	return ""
}

func (pr *ProviderEnvironmentReaper) warn(msg string, err error) {
	if pr.Warn != nil {
		pr.Warn(msg, err)
	}
}
