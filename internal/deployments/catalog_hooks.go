package deployments

import (
	"context"
	"net/http"
	"net/url"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/recipes"
)

// CatalogHooks is what a composed catalog kind does beyond its recipe.
// A kind registers one by name; a kind without one gets NoHooks.
type CatalogHooks interface {
	// Files renders a component's declared files, by File.Path, before
	// the component starts.
	Files(ctx context.Context, run ComponentRun) (map[string][]byte, error)
	// Ready runs after a process component passes its readiness check.
	Ready(ctx context.Context, run ComponentRun) error
	// Removed runs when the composed resource is deleted, before its
	// generated secrets go.
	Removed(ctx context.Context, res ComposedResource) error
}

// NoHooks does nothing. A kind's hooks embed it to implement only what
// they need.
type NoHooks struct{}

var _ CatalogHooks = NoHooks{}

// Files implements CatalogHooks.
func (NoHooks) Files(context.Context, ComponentRun) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}

// Ready implements CatalogHooks.
func (NoHooks) Ready(context.Context, ComponentRun) error { return nil }

// Removed implements CatalogHooks.
func (NoHooks) Removed(context.Context, ComposedResource) error { return nil }

// ComponentRun is one component of a composed resource as it's applied.
type ComponentRun struct {
	OrgID, ProjectID, EnvironmentID, Environment, PlanID, DeploymentID string
	// Resource is the composed resource's manifest name.
	Resource string
	Kind     string
	// Provider is "server" or "kubernetes".
	Provider  string
	Component recipes.ResolvedComponent
	// Env is what the component gets, secrets included.
	Env map[string]string
	// Secrets are the composition's generated secrets the component
	// reads, by GeneratedSecret.Name.
	Secrets map[string][]byte
	// Channel reaches a control port of a component already running in
	// this composition: the component itself once it has started (in
	// Ready), and components that started in an earlier step. One in the
	// same step, with no After ordering between them, can't be reached
	// yet. Requests can't carry an Authorization header (see
	// ComponentChannel).
	Channel func(ctx context.Context, component, port string) (*ComponentChannel, error)
}

// ComposedResource is a composed resource being deleted.
type ComposedResource struct{ OrgID, ProjectID, EnvironmentID, Resource, Kind string }

// ComponentChannel sends requests to one port of a running component.
// It sends nothing but requests to http://component, and no request may
// carry an Authorization header: through the Kubernetes service proxy it
// would replace the cluster credential and never reach the component, so
// it's refused on every target. Send the component's own credential in
// another header, such as a kind-specific admin token header.
type ComponentChannel struct {
	// Client sends requests to BaseURL, with a 30 second timeout and no
	// redirects.
	Client *http.Client
	// BaseURL is always http://component.
	BaseURL *url.URL
	Close   func() error
}

// EndpointResolver reads an external endpoint with its DSN.
// *catalogconnect.Service is the real one.
type EndpointResolver interface {
	Resolve(ctx context.Context, orgID, id string) (*catalogconnect.Endpoint, string, error)
}

// EndpointChecker checks an external endpoint from a target the way a plan
// does. *catalogconnect.Checker is the real one.
type EndpointChecker interface {
	Check(ctx context.Context, orgID, endpointID string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error)
}

var (
	_ EndpointResolver = (*catalogconnect.Service)(nil)
	_ EndpointChecker  = (*catalogconnect.Checker)(nil)
)
