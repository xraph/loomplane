package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/deployments"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/infrastructure"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

// startTimeout bounds a start in the background: one service's apply,
// image pull included.
const startTimeout = 15 * time.Minute

// instances is platformcloud.InstanceControl over Ctrl Plane and the
// platform hosts. A service's VM stops and starts through its Ctrl Plane
// instance, as Loomplane automation in its organization, so Ctrl Plane keeps
// the instance's state; a suspension's stop is marked so the placement is
// left for the suspension to record. Starts run in the background: a start
// re-applies the service, which reserves under the tenant lock that the
// caller of Start holds. A VM's removal, and a stop Ctrl Plane can't do from
// the instance's state, go straight to the host under Loomplane Cloud's
// maintenance authority.
type instances struct {
	svc       *platformcloud.Service
	target    *deployments.PlatformTarget
	ctrl      deployments.CtrlPlane
	authority fc.Issuer

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var (
	_ platformcloud.InstanceControl = (*instances)(nil)
	_ platformcloud.Redeployer      = (*instances)(nil)
)

func newInstances(svc *platformcloud.Service, target *deployments.PlatformTarget) *instances {
	ctx, cancel := context.WithCancel(context.Background())
	return &instances{svc: svc, target: target, ctrl: target.Ctrl, authority: target.Authority, ctx: ctx, cancel: cancel}
}

// close cuts the background starts short and waits for them.
func (ic *instances) close(ctx context.Context) {
	ic.cancel()
	done := make(chan struct{})
	go func() { ic.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
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
	if err = ic.target.PrepareRegion(region); err != nil {
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
	inst, err := ic.ctrl.Instances.Get(ctx, iid)
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
		return ic.ctrl.Instances.Stop(deployments.WithPlatformSuspend(ctx), iid)
	}
	// Starting, stopping, failed: Ctrl Plane can't stop it from here, so
	// the host is told directly.
	return deployments.PlatformVMAction(ctx, ic.svc, ic.authority, *p, "stop")
}

// Start starts the instance in the background and returns once that is on
// its way. A failed start leaves the instance failed and the placement for
// RepairMissingVMs.
func (ic *instances) Start(ctx context.Context, instanceID string) error {
	_, iid, ctx, err := ic.prepare(ctx, instanceID)
	if err != nil {
		return err
	}
	inst, err := ic.ctrl.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	switch inst.State {
	case cpprovider.StateRunning, cpprovider.StateStarting:
		return nil
	case cpprovider.StateStopped, cpprovider.StateFailed:
		ic.background(ctx, instanceID, iid)
		return nil
	}
	return fmt.Errorf("the instance is %s and can't be started", inst.State)
}

func (ic *instances) background(ctx context.Context, instanceID string, iid id.ID) {
	claims := context.WithoutCancel(ctx)
	ic.wg.Add(1)
	go func() {
		defer ic.wg.Done()
		run, cancel := context.WithTimeout(ic.ctx, startTimeout)
		defer cancel()
		run = mergeValues(run, claims)
		if err := ic.ctrl.Instances.Start(run, iid); err != nil {
			slog.ErrorContext(run, "platform cloud could not start an instance", "instance", instanceID, "error", err)
		}
	}()
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

// Redeploy puts the instance on a host again through the normal deploy
// path: a running instance is stopped, as a suspension stops it so the
// placement stays live, and started in the background, which reserves again
// and applies. A move off a draining host and a VM a host lost both come
// back this way.
func (ic *instances) Redeploy(ctx context.Context, instanceID string) error {
	_, iid, ctx, err := ic.prepare(ctx, instanceID)
	if err != nil {
		return err
	}
	inst, err := ic.ctrl.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	if inst.State == cpprovider.StateRunning {
		// A VM that is gone fails the stop, which leaves the instance
		// failed and startable.
		if stopErr := ic.ctrl.Instances.Stop(deployments.WithPlatformSuspend(ctx), iid); stopErr != nil {
			slog.WarnContext(ctx, "platform cloud redeploy: the stop failed; starting anyway", "instance", instanceID, "error", stopErr)
		}
		if inst, err = ic.ctrl.Instances.Get(ctx, iid); err != nil {
			return err
		}
	}
	switch inst.State {
	case cpprovider.StateStopped, cpprovider.StateFailed:
		ic.background(ctx, instanceID, iid)
		return nil
	case cpprovider.StateStarting:
		return nil
	}
	return fmt.Errorf("the instance is %s and can't be redeployed", inst.State)
}

// valuesOf is a context with the deadline and cancellation of one and the
// values of another.
type valuesOf struct {
	context.Context
	values context.Context
}

func (v valuesOf) Value(key any) any {
	if out := v.values.Value(key); out != nil {
		return out
	}
	return v.Context.Value(key)
}

func mergeValues(run, values context.Context) context.Context {
	return valuesOf{Context: run, values: values}
}
