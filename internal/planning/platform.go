package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/pkg/manifest"
)

// platformPlacer previews room on Loomplane Cloud. *platformcloud.Service
// is one. Its errors are safe to show the workspace: they never name a host.
type platformPlacer interface {
	Preview(context.Context, platformcloud.Request) (*platformcloud.Ref, error)
}

const (
	// platformRuntime is the runtime, provider and cloud of a Loomplane
	// Cloud target.
	platformRuntime = platformcloud.RuntimePlatform
	// platformBuilder builds a Loomplane Cloud service. Loomplane Cloud
	// builds on its own hosts, so a workspace never chooses the builder.
	platformBuilder = "platform"
	// platformServiceDiskMiB is the disk a Loomplane Cloud service asks for.
	// A manifest size has no disk, and this is what a Firecracker service
	// VM gets (internal/deployments/firecracker.go).
	platformServiceDiskMiB = 4096
	// platformMinMemoryMiB is the least memory a Firecracker service VM
	// gets (internal/deployments/firecracker.go), so a smaller service is
	// previewed and reserved at this.
	platformMinMemoryMiB = 128

	platformUnavailable = "Loomplane Cloud isn't available on this instance."
	platformLocation    = "Location constraints don't apply to Loomplane Cloud targets. Remove placement, or choose a target on a Cloud you connect."
)

// platformMessageError is a refusal whose text is shown to people as written.
type platformMessageError string

func (m platformMessageError) Error() string { return string(m) }

var (
	errPlatformUnavailable error = platformMessageError(platformUnavailable)
	// errPlatformSize is a size Loomplane Cloud can't run.
	errPlatformSize error = platformMessageError("Loomplane Cloud needs a size with cpu of at least 0.001 vCPU, in steps of 0.001, and memory of at least 1 MiB.")
)

func isPlatformTarget(t *cloudprofiles.Target) bool {
	return t != nil && t.Runtime == platformRuntime
}

// platformRequest asks Loomplane Cloud for room for one service of size in
// region.
func platformRequest(plan *Plan, name, region string, size *manifest.Size) (platformcloud.Request, error) {
	cpu, memory, err := ServerResources(size)
	if err != nil {
		return platformcloud.Request{}, errPlatformSize
	}
	return platformcloud.Request{
		OrgID: plan.OrgID, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvironmentID, Resource: name, Region: region, Kind: "service",
		VCPUs: int((cpu + 999) / 1000), MemoryMiB: max(platformMinMemoryMiB, int(memory>>20)), DiskMiB: platformServiceDiskMiB,
	}, nil
}

// refusePlatformLocation refuses location constraints on a service that a
// Loomplane Cloud target places, or that only Loomplane Cloud targets could
// place: the platform chooses its own hosts.
func refusePlatformLocation(name string, set manifest.Settings, a *cloudprofiles.Assignment) error {
	if set.Placement == nil || a == nil {
		return nil
	}
	selected, err := SelectCloudTarget(set, a, name)
	if err != nil {
		// Resolution reports a missing or ambiguous target.
		return nil
	}
	onlyPlatform := selected == nil && set.CloudTarget == "" && len(set.CloudTags) == 0 && len(a.Targets) > 0
	for i := range a.Targets {
		onlyPlatform = onlyPlatform && isPlatformTarget(&a.Targets[i])
	}
	if isPlatformTarget(selected) || onlyPlatform {
		return &CloudPlacementError{Code: "platform-location-unsupported", Path: "services." + name + ".placement", Message: platformLocation}
	}
	return nil
}

// resolvePlatform previews room for every service the plan places on a
// Loomplane Cloud target. A refusal is the plan's problem at the service.
// A Cloud default is never a Loomplane Cloud target, so only services that
// name or tag one are previewed.
func (s *Service) resolvePlatform(ctx context.Context, plan *Plan, m *manifest.Manifest, a *cloudprofiles.Assignment, held map[string]bool) (map[string]*platformcloud.Ref, error) {
	out := map[string]*platformcloud.Ref{}
	if a == nil {
		return out, nil
	}
	for _, name := range m.ServiceNames() {
		set := m.Services[name].For(plan.Environment, m.Defaults)
		if held[name] || !manifest.On(set.Enabled) || set.CloudTarget == "" && len(set.CloudTags) == 0 {
			continue
		}
		path := cloudPath(name, set)
		// Resolution reports a missing or ambiguous target.
		selected, selectErr := SelectCloudTarget(set, a, path)
		if selectErr != nil || !isPlatformTarget(selected) {
			continue
		}
		if err := refusePlatformLocation(name, set, a); err != nil {
			return nil, err
		}
		if s.platform == nil {
			return nil, &CloudPlacementError{Code: "platform-unavailable", Path: path, Message: platformUnavailable}
		}
		req, err := platformRequest(plan, name, selected.Region, set.Size)
		if err != nil {
			return nil, &CloudPlacementError{Code: "platform-size", Path: "services." + name + ".size", Message: err.Error()}
		}
		ref, err := s.platform.Preview(ctx, req)
		if err != nil {
			return nil, &CloudPlacementError{Code: "platform-placement", Path: "services." + name, Message: err.Error()}
		}
		out[name] = ref
	}
	return out, nil
}

// platformPlacement pins a service to a Loomplane Cloud target. It looks up
// no connection: the target has none, and the room is ref's.
func platformPlacement(name, path string, set manifest.Settings, a *cloudprofiles.Assignment, t cloudprofiles.Target, ref *platformcloud.Ref) (CloudPlacement, manifest.Settings, error) {
	if ref == nil {
		message := platformUnavailable
		if name == dbGatewayName {
			message = "The database gateway can't run on Loomplane Cloud. Give database_gateway a provider of its own."
		}
		return CloudPlacement{}, set, &CloudPlacementError{Code: "platform-unavailable", Path: path, Message: message}
	}
	if set.Region != "" && set.Region != t.Region {
		return CloudPlacement{}, set, &CloudPlacementError{Code: "platform-region", Path: path, Message: fmt.Sprintf("Loomplane Cloud target %s runs in %s. Remove region, or name the target for %s.", t.Name, t.Region, set.Region)}
	}
	regionSource := "default"
	if set.Region != "" {
		regionSource = "manifest"
	}
	p, err := platformCloudPin(a, t, ref, regionSource)
	if err != nil {
		return CloudPlacement{}, set, err
	}
	set.Provider, set.Region, set.Target = platformRuntime, t.Region, ""
	set.CloudTarget, set.CloudTags = "", nil
	return p, set, nil
}

// platformCloudPin pins the assignment's Loomplane Cloud target t with the
// room ref previewed in it. The pin names no connection, pool or server.
func platformCloudPin(a *cloudprofiles.Assignment, t cloudprofiles.Target, ref *platformcloud.Ref, regionSource string) (CloudPlacement, error) {
	if !isPlatformTarget(&t) || ref == nil || ref.Region != t.Region {
		return CloudPlacement{}, fmt.Errorf("a Loomplane Cloud placement needs its target's region previewed")
	}
	if regionSource == "" {
		regionSource = "default"
	}
	return CloudPlacement{GrantRevision: a.GrantRevision, CloudID: a.CloudID, CloudRevision: a.CloudRevision, AssignmentRevision: a.Revision, TargetID: t.ID, TargetRevision: t.Revision, TargetName: t.Name, Provider: t.Provider, Region: t.Region, RegionSource: regionSource, Platform: ref}, nil
}

// syncPlatformPlacements previews room on Loomplane Cloud for every applied
// service a Cloud sync moves onto one of after's Loomplane Cloud targets,
// at the size it was applied with. selector picks the services the sync
// moves, as appliedCloudPlan does.
func (s *Service) syncPlatformPlacements(ctx context.Context, plan *Plan, bindings []*Binding, after *cloudprofiles.Assignment, selector string) (map[string]CloudPlacement, error) {
	out := map[string]CloudPlacement{}
	if after == nil {
		return out, nil
	}
	for _, b := range bindings {
		if b.Subject != ServiceSubject {
			continue
		}
		var cfg ServiceConfig
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			return nil, err
		}
		pin := cfg.CloudPlacement
		if pin == nil || pin.Default || !selectsPlacement(selector, pin) {
			continue
		}
		i := slices.IndexFunc(after.Targets, func(t cloudprofiles.Target) bool { return t.Name == pin.TargetName })
		if i < 0 || !isPlatformTarget(&after.Targets[i]) {
			continue
		}
		t := after.Targets[i]
		if s.platform == nil {
			return nil, errPlatformUnavailable
		}
		req, err := platformRequest(plan, b.Name, t.Region, cfg.Size)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name, err)
		}
		ref, err := s.platform.Preview(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name, err)
		}
		if out[b.Name], err = platformCloudPin(after, t, ref, "default"); err != nil {
			return nil, err
		}
	}
	return out, nil
}
