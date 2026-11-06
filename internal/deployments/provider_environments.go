package deployments

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// EnvTarget is where an environment's variables go on a provider.
type EnvTarget struct {
	Builtin string // "production" | "preview" | ""
	Name    string // the environment name; used when Builtin is ""
}

// targetFor maps an environment to a provider target. A production-kind
// environment takes the provider's production and a preview-kind one takes
// Preview, but only the oldest of its kind (firstOfKind). Everything else maps
// by name.
func targetFor(kind, name string, firstOfKind bool) EnvTarget {
	if firstOfKind && (kind == "production" || kind == "preview") {
		return EnvTarget{Builtin: kind}
	}
	return EnvTarget{Name: name}
}

// FallbackNotifier tells people an environment fell back.
type FallbackNotifier interface {
	NotifyFallback(ctx context.Context, pe *ProviderEnvironment, environment string) error
}

// NativeEnvironments is what the Vercel and Render executors hold.
type NativeEnvironments struct {
	Store  ProviderEnvironmentStore
	Notify FallbackNotifier // may be nil
	Now    func() time.Time // defaults to time.Now
}

func (n *NativeEnvironments) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// Target says which provider target the job's environment maps to.
func (n *NativeEnvironments) Target(ctx context.Context, job *Job) (EnvTarget, error) {
	d := job.Deployment
	kind, name, first, err := n.Store.EnvironmentRole(ctx, d.OrgID, d.ProjectID, d.EnvironmentID)
	if err != nil {
		return EnvTarget{}, fmt.Errorf("look up environment %s: %w", d.EnvironmentID, err)
	}
	return targetFor(kind, name, first), nil
}

// Record returns the saved native or fallback record for the job's
// environment on the job's connection, or nil when there is none yet.
func (n *NativeEnvironments) Record(ctx context.Context, job *Job, provider, scope string) (*ProviderEnvironment, error) {
	d := job.Deployment
	return n.Store.ProviderEnvironment(ctx, d.OrgID, d.EnvironmentID, provider, job.Connection.ID, scope)
}

func (n *NativeEnvironments) row(job *Job, provider, scope string) *ProviderEnvironment {
	d := job.Deployment
	return &ProviderEnvironment{
		OrgID:         d.OrgID,
		ProjectID:     d.ProjectID,
		EnvironmentID: d.EnvironmentID,
		Provider:      provider,
		ConnectionID:  job.Connection.ID,
		Scope:         scope,
	}
}

// Native records that the environment deploys to the provider's own
// environment.
func (n *NativeEnvironments) Native(ctx context.Context, job *Job, provider, scope, projectRef, environmentRef string) error {
	pe := n.row(job, provider, scope)
	pe.Mode, pe.ProjectRef, pe.EnvironmentRef = ModeNative, projectRef, environmentRef
	return n.Store.SaveProviderEnvironment(ctx, pe)
}

// FallBack records that the environment deploys to a separate project, says so
// in the deploy log, and notifies people once per record.
func (n *NativeEnvironments) FallBack(ctx context.Context, job *Job, provider, scope, reason string, permanent bool) error {
	pe := n.row(job, provider, scope)
	pe.Mode, pe.Permanent, pe.Reason = ModeFallback, permanent, reason
	if err := n.Store.SaveProviderEnvironment(ctx, pe); err != nil {
		return err
	}
	environment := job.Deployment.Environment
	if job.Progress != nil {
		job.Progress(StepDeploying, fmt.Sprintf("%s couldn't use a native %s environment (%s), so it deploys to a separate project", providerName(provider), environment, reason))
	}
	if permanent || n.Notify == nil {
		return nil
	}
	claimed, err := n.Store.ClaimFallbackNotice(ctx, pe, n.now())
	if err != nil || !claimed {
		return err
	}
	return n.Notify.NotifyFallback(ctx, pe, environment)
}

// otherNativeUsers reports whether another environment (or, on Vercel,
// another service) of the connection records or claims a native deploy on
// the provider project, leaving out the row of the environment and scope
// doing the asking.
func otherNativeUsers(ctx context.Context, store ProviderEnvironmentStore, provider, orgID, connectionID, projectID, environmentID, scope string) (bool, error) {
	sharing, err := store.SharingProject(ctx, orgID, provider, connectionID, projectID)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(sharing, func(pe *ProviderEnvironment) bool {
		return pe.EnvironmentID != environmentID || pe.Scope != scope
	}), nil
}

func providerName(provider string) string {
	if provider == "" {
		return ""
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}
