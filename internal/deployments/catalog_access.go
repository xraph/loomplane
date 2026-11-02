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
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/owned"
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
	// ErrNotAJob is a component asked to run again as a job that's a
	// process.
	ErrNotAJob = errors.New("not a job")
)

// JobRunner runs a bound job component again. *OwnedTarget is the real
// one (see OwnedTarget.RerunJob). A rerun belongs to no deployment, so the
// ComponentRun its kind's hooks see, in Files, has an empty DeploymentID
// and Environment.
type JobRunner interface {
	RerunJob(ctx context.Context, b *planning.Binding, name string) (owned.JobResult, error)
}

// componentChannelTimeout bounds each request over a component channel.
const componentChannelTimeout = 30 * time.Second

// errChannelRedirect is a component answering with a redirect, which a
// component channel never follows.
var errChannelRedirect = errors.New("the component answered with a redirect, which Loomplane doesn't follow on its private channel")

// errChannelAuthorization is a request that brings its own Authorization
// header. Through the Kubernetes service proxy it would replace the
// cluster credential, and the API server never passes it on, so no
// channel carries one.
var errChannelAuthorization = errors.New("a component channel can't carry an Authorization header; send the component's own credential in another header, such as a kind-specific admin token header")

// errChannelHost is a request to anything but http://component.
var errChannelHost = errors.New("a component channel sends requests only to http://component")

// channelTransport refuses, the same way on every target, what a channel
// never sends.
type channelTransport struct{ next http.RoundTripper }

func (t channelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var refused error
	switch {
	case req.URL.Scheme != "http" || req.URL.Host != "component" || (req.Host != "" && req.Host != "component"):
		refused = errChannelHost
	case req.Header.Get("Authorization") != "":
		refused = errChannelAuthorization
	default:
		return t.next.RoundTrip(req)
	}
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, refused
}

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
// the composition's generated secrets. Every call that takes a principal
// checks its permission on the project first.
//
// The Internal calls and RunJob take none. They're for Loomplane's own
// background work, such as a kind's reconciler, and no HTTP handler calls
// them without checking the caller's permission on the project first.
type CatalogAccess struct{ d CatalogAccessDeps }

// NewCatalogAccess returns a CatalogAccess.
func NewCatalogAccess(d CatalogAccessDeps) *CatalogAccess { return &CatalogAccess{d: d} }

// Channel opens a channel to a control port of a running component. Send
// requests to http://component/...; close the channel when done. A request
// can't carry an Authorization header on any target, since the Kubernetes
// service proxy authenticates with it: send the component's own credential
// in another header, such as a kind-specific admin token header.
func (a *CatalogAccess) Channel(ctx context.Context, p security.Principal, need security.Permission, ref ComponentRef) (*ComponentChannel, error) {
	if err := a.d.Authz.Require(ctx, p, need, ref.ProjectID); err != nil {
		return nil, err
	}
	bindings, err := a.d.Bindings(ctx, p.OrgID, ref.EnvironmentID)
	if err != nil {
		return nil, err
	}
	return openComponentChannel(ctx, a.d.Executors, bindings, p, ref)
}

// openComponentChannel finds a running component's control port among an
// environment's bindings and reaches it through the dialer of the target
// it runs on, acting as p. It checks no permission: callers do.
func openComponentChannel(ctx context.Context, executors map[string]Executor, bindings []*planning.Binding, p security.Principal, ref ComponentRef) (*ComponentChannel, error) {
	b, port, err := controlPort(bindings, p.OrgID, ref)
	if err != nil {
		return nil, err
	}
	dialer, ok := executors[b.Provider].(ComponentDialer)
	if !ok {
		return nil, fmt.Errorf("%s runs on %s, which Loomplane can't reach privately", b.Name, b.Provider)
	}
	return openChannel(infrastructure.WithPrincipal(ctx, p), dialer, b, port)
}

// InternalChannel opens a channel to a control port of a running
// component, as Channel does, for Loomplane's own background work. It
// makes Channel's checks of the binding, the component and its port, and
// checks no permission: an HTTP handler checks the caller's first.
func (a *CatalogAccess) InternalChannel(ctx context.Context, orgID string, ref ComponentRef) (*ComponentChannel, error) {
	bindings, err := a.d.Bindings(ctx, orgID, ref.EnvironmentID)
	if err != nil {
		return nil, err
	}
	return openComponentChannel(ctx, a.d.Executors, bindings, security.Principal{OrgID: orgID}, ref)
}

// GeneratedSecret reads one of a composed resource's generated secrets.
func (a *CatalogAccess) GeneratedSecret(ctx context.Context, p security.Principal, need security.Permission, ref ResourceRef, name string) ([]byte, error) {
	if err := a.d.Authz.Require(ctx, p, need, ref.ProjectID); err != nil {
		return nil, err
	}
	return a.keptSecret(ctx, p.OrgID, ref, name)
}

// InternalSecret reads one of a composed resource's generated secrets,
// as GeneratedSecret does, for Loomplane's own background work. It makes
// GeneratedSecret's checks of the binding and the secret, and checks no
// permission: an HTTP handler checks the caller's first.
func (a *CatalogAccess) InternalSecret(ctx context.Context, orgID string, ref ResourceRef, name string) ([]byte, error) {
	return a.keptSecret(ctx, orgID, ref, name)
}

// keptSecret reads a generated secret the composed resource ref declares.
// It checks no permission: callers do.
func (a *CatalogAccess) keptSecret(ctx context.Context, orgID string, ref ResourceRef, name string) ([]byte, error) {
	bindings, err := a.d.Bindings(ctx, orgID, ref.EnvironmentID)
	if err != nil {
		return nil, err
	}
	parent := composedParent(bindings, orgID, ref)
	if parent == nil {
		return nil, fmt.Errorf("%w: %s isn't a composed resource in this environment", ErrComponentNotFound, ref.Resource)
	}
	composition := boundResource(parent).Composition
	if !slices.ContainsFunc(composition.Secrets, func(s recipes.GeneratedSecret) bool { return s.Name == name }) {
		return nil, fmt.Errorf("%w: %s has no generated secret %s", ErrComponentNotFound, ref.Resource, name)
	}
	if a.d.Secrets == nil {
		return nil, ErrNoCredential
	}
	value, err := a.d.Secrets.Get(ctx, resourceKey(orgID, ref.EnvironmentID, ref.Resource, "secret:"+name))
	if errors.Is(err, ErrNoCredential) {
		return nil, fmt.Errorf("%s's generated secret %s hasn't been kept yet", ref.Resource, name)
	}
	return value, err
}

// RunJob runs a bound job component of a composed resource again, now,
// as name, through the target it runs on (see JobRunner), for Loomplane's
// own background work. It records nothing, and checks no permission: an
// HTTP handler checks the caller's first. A process is ErrNotAJob. The
// rerun belongs to no deployment: its ComponentRun has an empty
// DeploymentID and Environment.
func (a *CatalogAccess) RunJob(ctx context.Context, orgID string, ref ResourceRef, component, name string) (owned.JobResult, error) {
	bindings, err := a.d.Bindings(ctx, orgID, ref.EnvironmentID)
	if err != nil {
		return owned.JobResult{}, err
	}
	action := planning.ComponentActionName(ref.Resource, component)
	for _, b := range bindings {
		if b.OrgID != orgID || b.ProjectID != ref.ProjectID || b.EnvironmentID != ref.EnvironmentID || b.Subject != planning.ResourceSubject || b.Name != action {
			continue
		}
		cfg := boundResource(b)
		if cfg == nil || cfg.Component == nil || cfg.Component.Resource != ref.Resource || cfg.Component.Component.Name != component {
			continue
		}
		if role := cfg.Component.Component.Role; role != recipes.RoleJob {
			return owned.JobResult{}, fmt.Errorf("%w: %s's %s is a %s", ErrNotAJob, ref.Resource, component, role)
		}
		runner, ok := a.d.Executors[b.Provider].(JobRunner)
		if !ok {
			return owned.JobResult{}, fmt.Errorf("%s runs on %s, which can't run catalog jobs again", b.Name, b.Provider)
		}
		return runner.RerunJob(ctx, b, name)
	}
	return owned.JobResult{}, fmt.Errorf("%w: %s has no job %s", ErrComponentNotFound, ref.Resource, component)
}

// Components lists a composed resource's bindings: the resource itself
// first, then each of its components, and each dependency-only
// dependency it provisioned, by name.
func (a *CatalogAccess) Components(ctx context.Context, p security.Principal, need security.Permission, ref ResourceRef) ([]*planning.Binding, error) {
	if err := a.d.Authz.Require(ctx, p, need, ref.ProjectID); err != nil {
		return nil, err
	}
	bindings, err := a.d.Bindings(ctx, p.OrgID, ref.EnvironmentID)
	if err != nil {
		return nil, err
	}
	parent := composedParent(bindings, p.OrgID, ref)
	if parent == nil {
		return nil, fmt.Errorf("%w: %s isn't a composed resource in this environment", ErrComponentNotFound, ref.Resource)
	}
	// A dependency-only dependency runs as its own resource.
	provisioned := map[string]bool{}
	for _, choice := range boundResource(parent).Composition.Dependencies {
		if choice.Mode == planning.DependencyNew {
			provisioned[choice.Resource] = true
		}
	}
	var parts []*planning.Binding
	for _, b := range bindings {
		if b.OrgID != p.OrgID || b.ProjectID != ref.ProjectID || b.EnvironmentID != ref.EnvironmentID || b.Subject != planning.ResourceSubject || !strings.HasPrefix(b.Name, ref.Resource+"--") {
			continue
		}
		if cfg := boundResource(b); cfg != nil && cfg.Component != nil && (cfg.Component.Resource == ref.Resource || (provisioned[b.Name] && cfg.Component.Resource == b.Name)) {
			parts = append(parts, b)
		}
	}
	slices.SortFunc(parts, func(x, y *planning.Binding) int { return strings.Compare(x.Name, y.Name) })
	return append([]*planning.Binding{parent}, parts...), nil
}

// composedParent finds a composed resource's own binding among an
// environment's bindings, or nil.
func composedParent(bindings []*planning.Binding, orgID string, ref ResourceRef) *planning.Binding {
	for _, b := range bindings {
		if b.OrgID != orgID || b.ProjectID != ref.ProjectID || b.EnvironmentID != ref.EnvironmentID || b.Subject != planning.ResourceSubject || b.Name != ref.Resource {
			continue
		}
		if cfg := boundResource(b); cfg != nil && cfg.Composition != nil {
			return b
		}
	}
	return nil
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
		Client: &http.Client{Transport: channelTransport{transport}, Timeout: componentChannelTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
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
