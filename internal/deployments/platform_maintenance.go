package deployments

import (
	"context"
	"errors"
	"strings"

	"github.com/xraph/grove/driver"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// Loomplane Cloud stops and removes VMs on its own as well as through a
// deployment: a suspension stops a workspace's work, a reclaim takes a long
// suspended VM off its host, and the platform cleanup removes what a move or
// a deleted organization left behind. These are the hooks the platform Cloud
// extension's instance control uses for that.

type platformSuspendKey struct{}

// WithPlatformSuspend marks ctx as a suspension's stop of a Loomplane Cloud
// instance. The stop leaves the placement as it is, for the suspension to
// mark suspended and remembered as running, instead of recording a user's
// stop, which a restore would leave stopped.
func WithPlatformSuspend(ctx context.Context) context.Context {
	return context.WithValue(ctx, platformSuspendKey{}, true)
}

func platformSuspendFrom(ctx context.Context) bool {
	on, ok := ctx.Value(platformSuspendKey{}).(bool)
	return ok && on
}

type platformMaintenanceKey struct{}

func platformMaintenanceFrom(ctx context.Context) bool {
	on, ok := ctx.Value(platformMaintenanceKey{}).(bool)
	return ok && on
}

// maintenanceOperation names one maintenance action on one placement's VM
// on one host: "<action>/platform-maintenance/<placement>/<host>". The
// authority is issued for exactly that.
func maintenanceOperation(action, placementID, hostID string) string {
	return action + "/" + maintenanceTag + "/" + placementID + "/" + hostID
}

const maintenanceTag = "platform-maintenance"

// platformMaintenanceAllowed decides whether Loomplane Cloud's own
// maintenance may stop or remove the VM under scope, in tx, which holds the
// organization's lock. The operation names the placement and the host it is
// for. Only a platform VM, only stop and remove, and only when that
// placement runs this instance on that host and is released or suspended,
// or its organization is suspended. A move keeps the instance, so another
// live placement of the same instance on the same host is the VM that
// should run, and is refused; one on another host is a different VM. A
// deploy in the environment that is still going wins; the maintenance tries
// again.
func platformMaintenanceAllowed(ctx context.Context, tx driver.Tx, scope fc.Scope, operation string) error {
	parts := strings.Split(operation, "/")
	if scope.Connection != fc.PlatformConnection || len(parts) != 4 || (parts[0] != "stop" && parts[0] != "remove") ||
		parts[1] != maintenanceTag || parts[2] == "" || parts[3] == "" {
		return fc.ErrConflict
	}
	placementID, hostID := parts[2], parts[3]
	var allowed, twin, busy bool
	err := tx.QueryRow(ctx, `SELECT
EXISTS(SELECT 1 FROM loomplane_platform_placements p
	WHERE p.id=$5 AND p.org_id=$1 AND p.project_id=$2 AND p.environment_id=$3 AND p.instance_id=$4 AND p.host_id=$6
	AND (p.state IN ('released','suspended') OR EXISTS(SELECT 1 FROM loomplane_platform_tenants t WHERE t.org_id=p.org_id AND cardinality(t.suspend_reasons)>0))),
EXISTS(SELECT 1 FROM loomplane_platform_placements q
	WHERE q.org_id=$1 AND q.instance_id=$4 AND q.host_id=$6 AND q.id<>$5 AND q.state<>'released'),
EXISTS(SELECT 1 FROM loomplane_workload_attempts WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND active AND NOT settled)`,
		scope.Org, scope.Project, scope.Environment, scope.Resource, placementID, hostID).Scan(&allowed, &twin, &busy)
	if err != nil {
		return err
	}
	if !allowed || twin || busy {
		return fc.ErrConflict
	}
	return nil
}

// PlatformVMAction stops or removes the VM of placement p on its host, as
// Loomplane Cloud's own maintenance. A VM that is already gone, or already
// stopped for a stop, is not an error, and neither is a placement with no
// host. The authority is issued for this placement on this host only, and
// only for what platformMaintenanceAllowed lets through. It records nothing
// about the placement: the caller does.
func PlatformVMAction(ctx context.Context, hosts PlatformHosts, authority fc.Issuer, p platformcloud.Placement, action string) error {
	if action != "stop" && action != "remove" {
		return fc.ErrInvalid
	}
	if hosts == nil || authority == nil {
		return fc.ErrUnavailable
	}
	if p.HostID == nil {
		return nil
	}
	hostID := *p.HostID
	c, scope, err := hosts.Open(ctx, &p)
	if err != nil {
		return err
	}
	if c == nil {
		return nil
	}
	defer c.Close()
	r, err := c.Inspect(ctx, scope)
	if errors.Is(err, fc.ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.State == "removed" || (action == "stop" && r.State == "stopped") {
		return nil
	}
	if r.Scope != scope {
		return fc.ErrConflict
	}
	q := readFirecracker(r)
	if q.Spec.Kind == "" && r.Pending != nil {
		q.Spec = r.Pending.Spec
	}
	maint := context.WithValue(ctx, platformMaintenanceKey{}, true)
	if q.Authority, err = authority.IssueFirecracker(maint, q.Scope, maintenanceOperation(action, p.ID, hostID), firecrackerDigest(q.Spec)); err != nil {
		return err
	}
	_, err = c.Mutate(ctx, q, action)
	return err
}

// PrepareRegion registers the region's Ctrl Plane provider, if it isn't yet,
// so its instances can be stopped and started before anything deployed to
// the region since this process started.
func (t *PlatformTarget) PrepareRegion(region string) error {
	_, err := t.backend(region)
	return err
}
