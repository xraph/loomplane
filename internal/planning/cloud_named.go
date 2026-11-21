package planning

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/pkg/manifest"
)

// namedClouds resolves every Cloud a service names with cloud. A Cloud the
// project can't use is a finding on the first service that names it.
func (s *Service) namedClouds(ctx context.Context, plan *Plan, needs cloudNeeds) (map[string]*cloudprofiles.ProjectCloud, error) {
	if len(needs.Named) == 0 {
		return nil, nil
	}
	resolver, ok := s.cloudProfiles.(cloudprofiles.NamedResolver)
	if !ok {
		return nil, errors.New("services name a Cloud, and this planner can't resolve Clouds by name")
	}
	out := map[string]*cloudprofiles.ProjectCloud{}
	for _, name := range slices.Sorted(maps.Keys(needs.Named)) {
		c, err := resolver.ResolveNamedCloud(ctx, plan.OrgID, plan.ProjectID, name)
		if errors.Is(err, cloudprofiles.ErrNotFound) {
			return nil, &CloudPlacementError{Code: "cloud-unknown", Path: "services." + needs.Named[name][0] + ".cloud", Message: fmt.Sprintf("No Cloud named %s is granted to this project. Check the name, or ask an organization admin to grant it.", name)}
		}
		if err != nil {
			return nil, err
		}
		if c.OrgID != plan.OrgID || c.ProjectID != plan.ProjectID {
			return nil, fmt.Errorf("the Cloud named %s is outside this organization or project", name)
		}
		out[name] = c
	}
	return out, nil
}

// resolveAcrossClouds places every service through its own Cloud: one pass
// for the project's default Cloud, then one for each Cloud a service names.
// Each pass holds every service of another Cloud, so the single-Cloud
// resolution runs unchanged and the passes' placements merge.
func (s *Service) resolveAcrossClouds(ctx context.Context, plan *Plan, m *manifest.Manifest, defaultCloud *cloudprofiles.ProjectCloud, named map[string]*cloudprofiles.ProjectCloud, needs cloudNeeds, bindings []*Binding, options []ConnectionOption, selections map[string]locationSelection, input Input, intents map[string]*CapacityIntent) (*manifest.Manifest, map[string]CloudPlacement, error) {
	cloudOf := map[string]string{}
	for name, services := range needs.Named {
		for _, service := range services {
			cloudOf[service] = name
		}
	}
	passes := append([]string{""}, slices.Sorted(maps.Keys(named))...)
	// Each pass clears the default tags it resolved; the next still needs them.
	tags := slices.Clone(m.Defaults.CloudTags)
	out, placements := m, map[string]CloudPlacement{}
	for i, name := range passes {
		cloud := defaultCloud
		if name != "" {
			cloud = named[name]
		}
		held := maps.Clone(needs.Held)
		constrained := false
		for service, svc := range out.Services {
			if cloudOf[service] != name {
				held[service] = true
				continue
			}
			constrained = constrained || svc.For(plan.Environment, out.Defaults).Placement != nil
		}
		var pools map[string]*cloudinfra.Placement
		var err error
		if constrained {
			pools, err = s.resolvePoolsWithLocationInput(ctx, plan, out, cloud, bindings, options, held, selections, input, intents)
		} else {
			pools, err = s.resolvePools(ctx, plan, out, cloud, bindings, options, held, intents)
		}
		if err != nil {
			return nil, nil, err
		}
		var platform map[string]*platformcloud.Ref
		if platform, err = s.resolvePlatform(ctx, plan, out, cloud, held); err != nil {
			return nil, nil, err
		}
		next, placed, err := resolveCloudPass(out, plan.Environment, cloud, options, pools, platform, held, cloudPass{gateway: name == "", named: name != ""}, intents)
		if err != nil {
			return nil, nil, err
		}
		if i < len(passes)-1 {
			next.Defaults.CloudTags = slices.Clone(tags)
		}
		maps.Copy(placements, placed)
		out = next
	}
	return out, placements, nil
}

// cloudsByID chooses, for an applied pin, the view of the Cloud it pins:
// the default or a named one. A pin on a Cloud the plan doesn't use falls
// back to the default, as a single-Cloud plan always did.
func cloudsByID(defaultCloud *cloudprofiles.ProjectCloud, named map[string]*cloudprofiles.ProjectCloud) cloudChooser {
	views := map[string]*cloudprofiles.ProjectCloud{}
	for _, c := range named {
		views[c.CloudID] = c
	}
	if defaultCloud != nil {
		views[defaultCloud.CloudID] = defaultCloud
	}
	return func(pin *CloudPlacement) (*cloudprofiles.ProjectCloud, bool) {
		if c := views[pin.CloudID]; c != nil {
			return c, true
		}
		return defaultCloud, true
	}
}
