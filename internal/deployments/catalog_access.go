package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// Errors CatalogAccess returns for what isn't there to reach.
var (
	// ErrComponentNotFound is a composed resource, component or generated
	// secret the environment doesn't have.
	ErrComponentNotFound = errors.New("no such catalog component")
	// ErrNotControlPort is a port the component doesn't declare as a
	// control port. Only those are reached privately.
	ErrNotControlPort = errors.New("not a control port")
)

// componentChannelTimeout bounds each request over a component channel.
const componentChannelTimeout = 30 * time.Second

// errChannelRedirect is a component answering with a redirect, which a
// component channel never follows.
var errChannelRedirect = errors.New("the component answered with a redirect, which Loomplane doesn't follow on its private channel")

// ComponentDialer reaches a port of a running catalog component privately,
// over the target's own connection. *OwnedTarget is the real one.
type ComponentDialer interface {
	ComponentTransport(ctx context.Context, b *planning.Binding, port int) (http.RoundTripper, io.Closer, error)
}

// ResourceRef names a composed resource in an environment.
type ResourceRef struct{ ProjectID, EnvironmentID, Resource string }

// ComponentRef names a control port of one of a composed resource's
// components.
type ComponentRef struct {
	ResourceRef
	// Component is the component's Component.Name.
	Component string
	// Port is the name of a port the component declares with Control.
	Port string
}

// CatalogAccessDeps is what CatalogAccess reads.
type CatalogAccessDeps struct {
	Authz Authorizer
	// Bindings lists what runs in an environment.
	Bindings func(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	// Executors are the targets, by provider. Those that reach components
	// privately implement ComponentDialer.
	Executors map[string]Executor
	Secrets   ResourceSecrets
}

// CatalogAccess lets a catalog kind's own API reach its running
// components: their control ports over the target's private channel, and
// the composition's generated secrets. Every call checks the caller's
// permission on the project first.
type CatalogAccess struct{ d CatalogAccessDeps }

// NewCatalogAccess returns a CatalogAccess.
func NewCatalogAccess(d CatalogAccessDeps) *CatalogAccess { return &CatalogAccess{d: d} }

// Channel opens a channel to a control port of a running component. Send
// requests to http://component/...; close the channel when done.
func (a *CatalogAccess) Channel(ctx context.Context, p security.Principal, need security.Permission, ref ComponentRef) (*ComponentChannel, error) {
	if err := a.d.Authz.Require(ctx, p, need, ref.ProjectID); err != nil {
		return nil, err
	}
	bindings, err := a.d.Bindings(ctx, p.OrgID, ref.EnvironmentID)
	if err != nil {
		return nil, err
	}
	b, port, err := controlPort(bindings, p.OrgID, ref)
	if err != nil {
		return nil, err
	}
	dialer, ok := a.d.Executors[b.Provider].(ComponentDialer)
	if !ok {
		return nil, fmt.Errorf("%s runs on %s, which Loomplane can't reach privately", b.Name, b.Provider)
	}
	return openChannel(infrastructure.WithPrincipal(ctx, p), dialer, b, port)
}

// GeneratedSecret reads one of a composed resource's generated secrets.
func (a *CatalogAccess) GeneratedSecret(ctx context.Context, p security.Principal, need security.Permission, ref ResourceRef, name string) ([]byte, error) {
	if err := a.d.Authz.Require(ctx, p, need, ref.ProjectID); err != nil {
		return nil, err
	}
	bindings, err := a.d.Bindings(ctx, p.OrgID, ref.EnvironmentID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(bindings, func(b *planning.Binding) bool {
		return b.OrgID == p.OrgID && b.ProjectID == ref.ProjectID && b.EnvironmentID == ref.EnvironmentID && b.Subject == planning.ResourceSubject && b.Name == ref.Resource
	})
	var composition *planning.CompositionConfig
	if i >= 0 {
		if cfg := boundResource(bindings[i]); cfg != nil {
			composition = cfg.Composition
		}
	}
	if composition == nil {
		return nil, fmt.Errorf("%w: %s isn't a composed resource in this environment", ErrComponentNotFound, ref.Resource)
	}
	if !slices.ContainsFunc(composition.Secrets, func(s recipes.GeneratedSecret) bool { return s.Name == name }) {
		return nil, fmt.Errorf("%w: %s has no generated secret %s", ErrComponentNotFound, ref.Resource, name)
	}
	if a.d.Secrets == nil {
		return nil, ErrNoCredential
	}
	value, err := a.d.Secrets.Get(ctx, resourceKey(p.OrgID, ref.EnvironmentID, ref.Resource, "secret:"+name))
	if errors.Is(err, ErrNoCredential) {
		return nil, fmt.Errorf("%s's generated secret %s hasn't been kept yet", ref.Resource, name)
	}
	return value, err
}

// controlPort finds a running process component of a composed resource
// among an environment's bindings, and the control port named.
func controlPort(bindings []*planning.Binding, orgID string, ref ComponentRef) (*planning.Binding, recipes.Port, error) {
	name := planning.ComponentActionName(ref.Resource, ref.Component)
	for _, b := range bindings {
		if b.OrgID != orgID || b.ProjectID != ref.ProjectID || b.EnvironmentID != ref.EnvironmentID || b.Subject != planning.ResourceSubject || b.Name != name {
			continue
		}
		cfg := boundResource(b)
		if cfg == nil || cfg.Component == nil || cfg.Component.Resource != ref.Resource || cfg.Component.Component.Name != ref.Component || cfg.Component.Component.Role != recipes.RoleProcess || b.ExternalID == "" {
			continue
		}
		port, ok := componentPort(cfg.Component.Component.Component, ref.Port)
		if !ok || !port.Control {
			return nil, recipes.Port{}, fmt.Errorf("%w: %s's %s has no control port %q", ErrNotControlPort, ref.Resource, ref.Component, ref.Port)
		}
		return b, port, nil
	}
	return nil, recipes.Port{}, fmt.Errorf("%w: %s has no running component %s", ErrComponentNotFound, ref.Resource, ref.Component)
}

// openChannel reaches a component's port through its target's dialer,
// with a client that bounds each request and never follows a redirect.
func openChannel(ctx context.Context, dialer ComponentDialer, b *planning.Binding, port recipes.Port) (*ComponentChannel, error) {
	transport, closer, err := dialer.ComponentTransport(ctx, b, port.Container)
	if err != nil {
		return nil, fmt.Errorf("reach %s's %s port: %w", b.Name, port.Name, err)
	}
	return &ComponentChannel{
		Client: &http.Client{Transport: transport, Timeout: componentChannelTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errChannelRedirect
		}},
		BaseURL: &url.URL{Scheme: "http", Host: "component"},
		Close:   closer.Close,
	}, nil
}

// runChannel reaches the components of the composition a job's component
// belongs to: the component itself once it runs (self), and the ones that
// started before it, from the environment's bindings.
func (e *OwnedTarget) runChannel(job *Job, self *planning.Binding) func(ctx context.Context, component, port string) (*ComponentChannel, error) {
	d, cfg := job.Deployment, job.Action.Resource.Component
	return func(ctx context.Context, component, port string) (*ComponentChannel, error) {
		ref := ComponentRef{ResourceRef: ResourceRef{ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Resource: cfg.Resource}, Component: component, Port: port}
		var bindings []*planning.Binding
		switch {
		case component == cfg.Component.Name:
			// What's bound is the release this one replaces.
			if self != nil {
				bindings = []*planning.Binding{self}
			}
		case e.Bindings == nil:
			return nil, errors.New("this target can't look up the composition's other components")
		default:
			listed, err := e.Bindings(ctx, d.OrgID, d.EnvironmentID)
			if err != nil {
				return nil, err
			}
			bindings = listed
		}
		b, p, err := controlPort(bindings, d.OrgID, ref)
		if err != nil {
			return nil, err
		}
		dial := e.componentDial
		if dial == nil {
			dial = e.ComponentTransport
		}
		return openChannel(infrastructure.WithPrincipal(ctx, principal(job)), dialerFunc(dial), b, p)
	}
}

// selfBinding is what a component's binding will say once its step
// finishes, for reaching it before then.
func selfBinding(job *Job, externalID string) (*planning.Binding, error) {
	raw, err := json.Marshal(job.Action.Resource)
	if err != nil {
		return nil, err
	}
	d := job.Deployment
	return &planning.Binding{
		OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Subject: planning.ResourceSubject, Name: job.Action.Name,
		Provider: job.Action.Resource.Provider, ConnectionID: job.Connection.ID, ExternalID: externalID, Config: raw,
	}, nil
}

// dialerFunc is a ComponentDialer from a function.
type dialerFunc func(ctx context.Context, b *planning.Binding, port int) (http.RoundTripper, io.Closer, error)

func (f dialerFunc) ComponentTransport(ctx context.Context, b *planning.Binding, port int) (http.RoundTripper, io.Closer, error) {
	return f(ctx, b, port)
}
