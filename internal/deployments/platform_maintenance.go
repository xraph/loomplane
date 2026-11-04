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
	on, _ := ctx.Value(platformSuspendKey{}).(bool)
	return on
}

type platformMaintenanceKey struct{}

func platformMaintenanceFrom(ctx context.Context) bool {
	on, _ := ctx.Value(platformMaintenanceKey{}).(bool)
	return on
}

// platformMaintenanceAllowed decides whether Loomplane Cloud's own
// maintenance may stop or remove the VM under scope, in tx, which holds the
// organization's lock. Only a platform VM, only stop and remove, and only a
// VM whose placement is released or suspended, or whose organization is
// suspended, so maintenance never touches work that should run. A deploy in
// the environment that is still going wins; the maintenance tries again.
func platformMaintenanceAllowed(ctx context.Context, tx driver.Tx, scope fc.Scope, operation string) error {
	if scope.Connection != fc.PlatformConnection || (!strings.HasPrefix(operation, "stop/") && !strings.HasPrefix(operation, "remove/")) {
		return fc.ErrConflict
	}
	var allowed, busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_platform_placements p
WHERE p.org_id=$1 AND p.project_id=$2 AND p.environment_id=$3 AND p.instance_id=$4
AND (p.state IN ('released','suspended') OR EXISTS(SELECT 1 FROM loomplane_platform_tenants t WHERE t.org_id=p.org_id AND cardinality(t.suspend_reasons)>0))),
EXISTS(SELECT 1 FROM loomplane_workload_attempts WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND active AND NOT settled)`,
		scope.Org, scope.Project, scope.Environment, scope.Resource).Scan(&allowed, &busy)
	if err != nil {
		return err
	}
	if !allowed || busy {
		return fc.ErrConflict
	}
	return nil
}

// PlatformVMAction stops or removes the VM of placement p on its host, as
// Loomplane Cloud's own maintenance. A VM that is already gone, or already
// stopped for a stop, is not an error, and neither is a placement with no
// host. The authority is issued only for what platformMaintenanceAllowed
// lets through. It records nothing about the placement: the caller does.
func PlatformVMAction(ctx context.Context, hosts PlatformHosts, authority fc.Issuer, p platformcloud.Placement, action string) error {
	if action != "stop" && action != "remove" {
		return fc.ErrInvalid
	}
	if hosts == nil || authority == nil {
		return fc.ErrUnavailable
	}
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
	if q.Authority, err = authority.IssueFirecracker(maint, q.Scope, action+"/platform-maintenance", firecrackerDigest(q.Spec)); err != nil {
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
