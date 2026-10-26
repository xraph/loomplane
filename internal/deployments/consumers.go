package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// consumerPrefix starts the Field of an EnvLink to an output issued to the
// service alone.
const consumerPrefix = "consumer:"

// consumerOutput is the name of the per-consumer output a link reads.
func consumerOutput(link planning.EnvLink) (string, bool) {
	name, ok := strings.CutPrefix(link.Field, consumerPrefix)
	return name, ok && name != "" && link.Subject == planning.ResourceSubject
}

// consumerOf describes service as a consumer of a per-consumer output of
// the composed resource a link reads, with the options the link carries,
// and the binder of its kind.
func (r *Runner) consumerOf(ctx context.Context, d *Deployment, service, output string, link planning.EnvLink, resources map[string]*planning.Binding) (Consumer, ConsumerBinder, error) {
	var composition *planning.CompositionConfig
	if cfg := boundResource(resources[link.From]); cfg != nil {
		composition = cfg.Composition
	}
	if composition == nil {
		return Consumer{}, nil, fmt.Errorf("%s needs %s, which hasn't been provisioned as a composed catalog resource", service, link.From)
	}
	if !slices.ContainsFunc(composition.Outputs, func(o recipes.Output) bool { return o.Name == output && o.PerConsumer }) {
		return Consumer{}, nil, fmt.Errorf("%s has no per-consumer output %s", composition.Kind, output)
	}
	c := Consumer{
		OrgID: d.OrgID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Environment: d.Environment,
		Resource: link.From, Kind: composition.Kind, Output: output, Service: service,
		Channel: r.consumerChannel(d, link.From),
		Options: maps.Clone(link.Options),
		Address: r.consumerAddress(ctx, d, link.From),
	}
	binder, ok := r.catalogHooks[composition.Kind].(ConsumerBinder)
	if !ok {
		return c, nil, fmt.Errorf("%s has no consumer binder for output %s", composition.Kind, output)
	}
	return c, binder, nil
}

// bindConsumer issues the value of a per-consumer output to a service.
func (r *Runner) bindConsumer(ctx context.Context, d *Deployment, service string, link planning.EnvLink, resources map[string]*planning.Binding) (string, error) {
	output, _ := consumerOutput(link)
	c, binder, err := r.consumerOf(ctx, d, service, output, link, resources)
	if err != nil {
		return "", err
	}
	value, err := binder.BindConsumer(ctx, c)
	if err != nil {
		return "", fmt.Errorf("issue %s's %s from %s: %w", service, output, link.From, err)
	}
	return value, nil
}

// releaseConsumers releases the per-consumer outputs a service had
// through old and no longer has through now, which is all of them for a
// service that's gone (now is nil). It returns what failed to release as
// warnings: the service's binding has changed already, so a retry of the
// step wouldn't see anything to release again. A resource that's gone
// took what it issued with it.
func (r *Runner) releaseConsumers(ctx context.Context, d *Deployment, service string, old *planning.Binding, now *planning.ServiceConfig, resources map[string]*planning.Binding) []string {
	var before planning.ServiceConfig
	if old == nil || json.Unmarshal(old.Config, &before) != nil {
		return nil
	}
	type need struct{ resource, output string }
	var kept []need
	if now != nil {
		for _, link := range now.Env {
			if output, ok := consumerOutput(link); ok {
				kept = append(kept, need{link.From, output})
			}
		}
	}
	var warnings []string
	var released []need
	for _, link := range before.Env {
		output, ok := consumerOutput(link)
		n := need{link.From, output}
		if !ok || slices.Contains(kept, n) || slices.Contains(released, n) || resources[link.From] == nil {
			continue
		}
		released = append(released, n)
		c, binder, err := r.consumerOf(ctx, d, service, output, link, resources)
		if err == nil {
			err = binder.ReleaseConsumer(ctx, c)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("release %s's %s from %s: %v", service, output, link.From, err))
		}
	}
	return warnings
}

// consumerChannel reaches the control ports of a composed resource's
// running components for its kind's binder, over the channel the
// component's target gives the catalog API (see CatalogAccess.Channel).
// It looks the components up each time, since some start in an earlier
// step of the same deployment.
func (r *Runner) consumerChannel(d *Deployment, resource string) func(ctx context.Context, component, port string) (*ComponentChannel, error) {
	return func(ctx context.Context, component, port string) (*ComponentChannel, error) {
		bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
		if err != nil {
			return nil, err
		}
		ref := ComponentRef{ResourceRef: ResourceRef{ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, Resource: resource}, Component: component, Port: port}
		return openComponentChannel(ctx, r.executors, bindings, security.ActorPrincipal(d.OrgID, d.StartedBy), ref)
	}
}

// consumerAddress is where the composition reaches a port of one of a
// composed resource's running process components, from the address it
// kept when it started.
func (r *Runner) consumerAddress(ctx context.Context, d *Deployment, resource string) func(component, port string) (*url.URL, error) {
	return func(component, port string) (*url.URL, error) {
		if r.resources == nil {
			return nil, ErrNoCredential
		}
		name := planning.ComponentActionName(resource, component)
		addresses, err := keptAddresses(ctx, r.resources, d.OrgID, d.EnvironmentID, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		address, ok := addresses[port]
		if !ok {
			return nil, fmt.Errorf("%s has no %s port", name, port)
		}
		u, err := url.Parse(address)
		if err != nil {
			return nil, fmt.Errorf("%s's %s address isn't a URL", name, port)
		}
		return u, nil
	}
}
