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
	// TargetSettings are the target's reviewed connection settings the
	// component runs with, such as a cluster's "namespace". They're a
	// copy: changing them changes nothing.
	TargetSettings map[string]string
}

// ConsumerBinder is implemented by the hooks of a kind with an Output that
// is PerConsumer. The runner type-asserts it on the kind's CatalogHooks.
type ConsumerBinder interface {
	// BindConsumer issues the value one consuming service gets. The
	// runner calls it on every release of the service and keeps the
	// value only in the service's applied variables, so it must give the
	// same consumer the same credential each time.
	BindConsumer(ctx context.Context, c Consumer) (string, error)
	// ReleaseConsumer revokes what BindConsumer issued, once the service
	// is deleted or no longer needs the resource. Releasing a consumer
	// that was never bound, or twice, is not an error.
	ReleaseConsumer(ctx context.Context, c Consumer) error
}

// Consumer is one service that needs an Output of a composed resource.
type Consumer struct {
	OrgID, ProjectID, EnvironmentID, Environment string
	// Resource is the composed resource's manifest name.
	Resource string
	Kind     string
	// Output is the name of the Output being issued.
	Output string
	// Service is the consuming service's manifest name.
	Service string
	// Channel reaches a control port of a component of the composed
	// resource, which is already running. Requests can't carry an
	// Authorization header (see ComponentChannel).
	Channel func(ctx context.Context, component, port string) (*ComponentChannel, error)
	// Options are what the service set for this need under
	// need_options, checked against the kind's ConsumerOptions when it
	// was planned. They're a copy.
	Options map[string]string
	// Address is where the composition reaches a port of one of its
	// running process components, by Component.Name and Port.Name, such
	// as http://lp-inst-...:8080. It's an error for a component that
	// hasn't started or a port it doesn't declare.
	Address func(component, port string) (*url.URL, error)
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
