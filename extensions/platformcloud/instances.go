package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpinstance "github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/deployments"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

// placements is what the instance control reads of the control plane.
// *platformcloud.Service is one.
type placements interface {
	deployments.PlatformHosts
	PlacementOfInstance(ctx context.Context, instanceID string) (*platformcloud.Placement, error)
	RegionName(ctx context.Context, regionID string) (string, error)
	ActiveBuilds(ctx context.Context, orgID string) ([]platformcloud.Placement, error)
	HasRoomElsewhere(ctx context.Context, p platformcloud.Placement) (bool, error)
}

// lifecycle is the part of Ctrl Plane's instance service the instance
// control drives.
type lifecycle interface {
	Get(ctx context.Context, instanceID id.ID) (*cpinstance.Instance, error)
	Start(ctx context.Context, instanceID id.ID) error
	Stop(ctx context.Context, instanceID id.ID) error
}

// regions registers a region's Ctrl Plane provider.
type regions interface {
	PrepareRegion(region string) error
}

// instances is platformcloud.InstanceControl over Ctrl Plane and the
// platform hosts. A service's VM stops and starts through its Ctrl Plane
// instance, as Loomplane automation in its organization, so Ctrl Plane keeps
// the instance's state; a suspension's stop is marked so the placement is
// left for the suspension to record. Every call waits for its effect: the
// control plane calls Start with no lock held, so the start's own
// reservation is free to take the tenant lock. A VM's removal, and a stop
// Ctrl Plane can't do from the instance's state, go straight to the host
// under Loomplane Cloud's maintenance authority, bound to the placement and
// its host.
type instances struct {
	svc       placements
	regions   regions
	ctrl      lifecycle
	authority fc.Issuer
}

var (
	_ platformcloud.InstanceControl = (*instances)(nil)
	_ platformcloud.Redeployer      = (*instances)(nil)
	_ placements                    = (*platformcloud.Service)(nil)
)

func newInstances(svc *platformcloud.Service, target *deployments.PlatformTarget) *instances {
	return &instances{svc: svc, regions: target, ctrl: target.Ctrl.Instances, authority: target.Authority}
}

// prepare finds instanceID's placement, makes sure its region's provider is
// registered, and returns ctx acting as Loomplane automation in the
// placement's organization.
func (ic *instances) prepare(ctx context.Context, instanceID string) (*platformcloud.Placement, id.ID, context.Context, error) {
	p, err := ic.svc.PlacementOfInstance(ctx, instanceID)
	if err != nil {
		return nil, id.ID{}, ctx, fmt.Errorf("find the placement of %s: %w", instanceID, err)
	}
	region, err := ic.svc.RegionName(ctx, p.RegionID)
	if err != nil {
		return nil, id.ID{}, ctx, err
	}
	if err = ic.regions.PrepareRegion(region); err != nil {
		return nil, id.ID{}, ctx, err
	}
	iid, err := id.Parse(instanceID)
	if err != nil {
		return nil, id.ID{}, ctx, err
	}
	return p, iid, infrastructure.WithPrincipal(ctx, security.SystemPrincipal(p.OrgID)), nil
}

func (ic *instances) Stop(ctx context.Context, instanceID string) error {
	p, iid, ctx, err := ic.prepare(ctx, instanceID)
	if err != nil {
		return err
	}
	inst, err := ic.ctrl.Get(ctx, iid)
	switch {
	case errors.Is(err, ctrlplane.ErrNotFound):
		return deployments.PlatformVMAction(ctx, ic.svc, ic.authority, *p, "stop")
	case err != nil:
		return err
	}
	switch inst.State {
	case cpprovider.StateStopped, cpprovider.StateDestroyed:
		return nil
	case cpprovider.StateRunning:
		return ic.ctrl.Stop(deployments.WithPlatformSuspend(ctx), iid)
	}
	// Starting, stopping, failed: Ctrl Plane can't stop it from here, so
	// the host is told directly.
	return deployments.PlatformVMAction(ctx, ic.svc, ic.authority, *p, "stop")
}

// Start starts the instance and returns once it runs, or with why it
// doesn't.
func (ic *instances) Start(ctx context.Context, instanceID string) error {
	_, iid, ctx, err := ic.prepare(ctx, instanceID)
	if err != nil {
		return err
	}
	inst, err := ic.ctrl.Get(ctx, iid)
	if err != nil {
		return err
	}
	switch inst.State {
	case cpprovider.StateRunning:
		return nil
	case cpprovider.StateStopped, cpprovider.StateFailed:
		return ic.ctrl.Start(ctx, iid)
	}
	return fmt.Errorf("the instance is %s and can't be started", inst.State)
}

func (ic *instances) RemoveVM(ctx context.Context, p platformcloud.Placement) error {
	return deployments.PlatformVMAction(ctx, ic.svc, ic.authority, p, "remove")
}

// CancelBuilds removes the build VMs of the organization's running builds.
// Each build then fails, and its exit gives the placement back.
func (ic *instances) CancelBuilds(ctx context.Context, orgID string) error {
	builds, err := ic.svc.ActiveBuilds(ctx, orgID)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range builds {
		if b.HostID == nil || b.InstanceID == "" {
			continue
		}
		if removeErr := ic.RemoveVM(ctx, b); removeErr != nil {
			errs = append(errs, fmt.Errorf("build placement %s: %w", b.ID, removeErr))
		}
	}
	return errors.Join(errs...)
}

// errMoveFailed is a redeploy whose new host didn't take the service. The
// service was started again where it was.
var errMoveFailed = errors.New("the service could not be started on another host; it runs where it was")

// Redeploy puts the instance on a host again through the normal deploy
// path, and returns once that has finished. A service that runs and has
// nowhere else to go is left running and the answer is
// platformcloud.ErrNoCapacity. Otherwise it is stopped, as a suspension
// stops it so the placement stays live, and started, which reserves again,
// off a draining host, and applies. If that start fails, the service is
// started again on the host it was on, even a draining one, and the answer
// says the move failed. A VM a host lost comes back the same way.
func (ic *instances) Redeploy(ctx context.Context, instanceID string) error {
	p, iid, ctx, err := ic.prepare(ctx, instanceID)
	if err != nil {
		return err
	}
	inst, err := ic.ctrl.Get(ctx, iid)
	if err != nil {
		return err
	}
	from := ""
	if p.HostID != nil {
		from = *p.HostID
	}
	if inst.State == cpprovider.StateRunning {
		room, roomErr := ic.svc.HasRoomElsewhere(ctx, *p)
		if roomErr != nil {
			return roomErr
		}
		if !room {
			return platformcloud.ErrNoCapacity
		}
		// A VM that is gone fails the stop, which leaves the instance
		// failed and startable.
		if stopErr := ic.ctrl.Stop(deployments.WithPlatformSuspend(ctx), iid); stopErr != nil {
			slog.WarnContext(ctx, "platform cloud redeploy: the stop failed; starting anyway", "instance", instanceID, "error", stopErr)
		}
		if inst, err = ic.ctrl.Get(ctx, iid); err != nil {
			return err
		}
	}
	switch inst.State {
	case cpprovider.StateStopped, cpprovider.StateFailed:
	default:
		return fmt.Errorf("the instance is %s and can't be redeployed", inst.State)
	}
	startErr := ic.ctrl.Start(ctx, iid)
	if startErr == nil {
		return nil
	}
	slog.WarnContext(ctx, "platform cloud redeploy: the start failed; starting it where it was", "instance", instanceID, "error", startErr)
	if from == "" {
		return startErr
	}
	if inst, err = ic.ctrl.Get(ctx, iid); err != nil {
		return errors.Join(startErr, err)
	}
	if inst.State != cpprovider.StateStopped && inst.State != cpprovider.StateFailed {
		return errors.Join(errMoveFailed, startErr)
	}
	if backErr := ic.ctrl.Start(platformcloud.WithKeepHost(ctx, from), iid); backErr != nil {
		return errors.Join(startErr, fmt.Errorf("start it where it was: %w", backErr))
	}
	return errors.Join(errMoveFailed, startErr)
}
