package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpinstance "github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
	"github.com/xraph/grove/drivers/pgdriver"

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
	// resetStuck turns an instance that has said starting since before
	// since into failed, so it can be started again. A start whose own
	// failure write ran on a dead context leaves it starting for good.
	resetStuck func(ctx context.Context, iid id.ID, since time.Time) error
}

// startTimeout bounds one start: one service's apply, image pull included.
// An instance starting for longer than this is stuck.
const startTimeout = 14 * time.Minute

// fresh is a context that outlives ctx's cancellation, keeps its values and
// has a deadline of its own. What a VM's stop or start writes once it has
// begun must not die with the request or job that asked for it.
func fresh(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

var (
	_ platformcloud.InstanceControl = (*instances)(nil)
	_ platformcloud.Redeployer      = (*instances)(nil)
	_ placements                    = (*platformcloud.Service)(nil)
)

func newInstances(svc *platformcloud.Service, target *deployments.PlatformTarget, pg *pgdriver.PgDB) *instances {
	return &instances{svc: svc, regions: target, ctrl: target.Ctrl.Instances, authority: target.Authority,
		resetStuck: func(ctx context.Context, iid id.ID, since time.Time) error {
			_, err := pg.Exec(ctx, `UPDATE cp_instances SET state='failed',updated_at=now() WHERE id=$1 AND state='starting' AND updated_at<$2`, iid.String(), since)
			return err
		}}
}

// current reads the instance, turning one stuck starting into failed first.
func (ic *instances) current(ctx context.Context, iid id.ID) (*cpinstance.Instance, error) {
	inst, err := ic.ctrl.Get(ctx, iid)
	if err != nil || inst.State != cpprovider.StateStarting || time.Since(inst.UpdatedAt) <= startTimeout || ic.resetStuck == nil {
		return inst, err
	}
	slog.WarnContext(ctx, "platform cloud: an instance was stuck starting; starting it again", "instance", iid.String(), "since", inst.UpdatedAt)
	if checkErr := ic.resetStuck(ctx, iid, time.Now().Add(-startTimeout)); checkErr != nil {
		return nil, checkErr
	}
	return ic.ctrl.Get(ctx, iid)
}

// start starts the instance on a fresh context and waits for it.
func (ic *instances) start(ctx context.Context, iid id.ID) error {
	run, cancel := fresh(ctx, startTimeout)
	defer cancel()
	return ic.ctrl.Start(run, iid)
}

// stopForMove stops the instance as a suspension does, so its placement
// stays live, on a fresh context.
func (ic *instances) stopForMove(ctx context.Context, iid id.ID) error {
	run, cancel := fresh(ctx, startTimeout)
	defer cancel()
	return ic.ctrl.Stop(deployments.WithPlatformSuspend(run), iid)
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
	if checkErr := ic.regions.PrepareRegion(region); checkErr != nil {
		return nil, id.ID{}, ctx, checkErr
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
	inst, err := ic.current(ctx, iid)
	if err != nil {
		return err
	}
	switch inst.State {
	case cpprovider.StateRunning:
		return nil
	case cpprovider.StateStopped, cpprovider.StateFailed:
		return ic.start(ctx, iid)
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

// errMoveFailed is a move whose new host didn't take the service. The
// service was started again where it was.
var errMoveFailed = errors.New("the service could not be started on another host; it runs where it was")

// Redeploy puts the instance on a host again through the normal deploy path
// and returns once that has finished: a running instance is stopped, as a
// suspension stops it so the placement stays live, and started, which
// reserves again and applies. The reservation keeps the current host when it
// can still take the service, so a VM a healthy host lost comes back on the
// same host, in a region of one host too. An instance stuck starting is
// started again.
func (ic *instances) Redeploy(ctx context.Context, instanceID string) error {
	_, iid, ctx, err := ic.prepare(ctx, instanceID)
	if err != nil {
		return err
	}
	inst, err := ic.current(ctx, iid)
	if err != nil {
		return err
	}
	if inst.State == cpprovider.StateRunning {
		// A VM that is gone fails the stop, which leaves the instance
		// failed and startable.
		if stopErr := ic.stopForMove(ctx, iid); stopErr != nil {
			slog.WarnContext(ctx, "platform cloud redeploy: the stop failed; starting anyway", "instance", instanceID, "error", stopErr)
		}
		if inst, err = ic.current(ctx, iid); err != nil {
			return err
		}
	}
	switch inst.State {
	case cpprovider.StateStopped, cpprovider.StateFailed:
		return ic.start(ctx, iid)
	}
	return fmt.Errorf("the instance is %s and can't be redeployed", inst.State)
}

// Move moves the running instance off its host, which is draining, and
// returns once that has finished. A service with nowhere else to go is left
// running and the answer is platformcloud.ErrNoCapacity. Otherwise it is
// stopped, as a suspension stops it, and started, which reserves on another
// host and applies. If that start fails, it is started again on the host it
// was on, and the answer says the move failed. Every step after the stop
// runs on a context of its own, so a request that goes away mid-move still
// leaves the service running somewhere.
func (ic *instances) Move(ctx context.Context, instanceID string) error {
	ctx = context.WithoutCancel(ctx)
	p, iid, ctx, err := ic.prepare(ctx, instanceID)
	if err != nil {
		return err
	}
	inst, err := ic.current(ctx, iid)
	if err != nil {
		return err
	}
	if inst.State != cpprovider.StateRunning {
		return fmt.Errorf("the instance is %s, not running", inst.State)
	}
	from := ""
	if p.HostID != nil {
		from = *p.HostID
	}
	room, err := ic.svc.HasRoomElsewhere(ctx, *p)
	if err != nil {
		return err
	}
	if !room {
		return platformcloud.ErrNoCapacity
	}
	if stopErr := ic.stopForMove(ctx, iid); stopErr != nil {
		slog.WarnContext(ctx, "platform cloud move: the stop failed; starting anyway", "instance", instanceID, "error", stopErr)
	}
	if inst, err = ic.current(ctx, iid); err != nil {
		return err
	}
	if inst.State != cpprovider.StateStopped && inst.State != cpprovider.StateFailed {
		return fmt.Errorf("the instance is %s after its stop", inst.State)
	}
	startErr := ic.start(ctx, iid)
	if startErr == nil {
		return nil
	}
	slog.WarnContext(ctx, "platform cloud move: the start failed; starting it where it was", "instance", instanceID, "error", startErr)
	if from == "" {
		return startErr
	}
	back, cancel := fresh(ctx, startTimeout)
	defer cancel()
	if inst, err = ic.current(back, iid); err != nil {
		return errors.Join(startErr, err)
	}
	if inst.State != cpprovider.StateStopped && inst.State != cpprovider.StateFailed {
		return errors.Join(errMoveFailed, startErr)
	}
	if backErr := ic.ctrl.Start(platformcloud.WithKeepHost(back, from), iid); backErr != nil {
		return errors.Join(startErr, fmt.Errorf("start it where it was: %w", backErr))
	}
	return errors.Join(errMoveFailed, startErr)
}
