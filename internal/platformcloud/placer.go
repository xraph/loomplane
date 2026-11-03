package platformcloud

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/xraph/loomplane/internal/metering"
)

const (
	kindService = "service"
	kindBuild   = "build"

	stateReserved  = "reserved"
	stateRunning   = "running"
	stateStopped   = "stopped"
	stateSuspended = "suspended"
	stateReleased  = "released"
)

// Request asks for room for one workload. Region is the region's slug
// (Region.Name). Kind is "service" or "build". An empty Architecture takes
// any host.
type Request struct {
	OrgID, ProjectID, EnvironmentID, Resource, Region, Kind, Architecture string
	VCPUs, MemoryMiB, DiskMiB                                             int
}

// Ref is what a plan keeps of a platform placement. It never names a host.
type Ref struct {
	Region    string `json:"region"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB int    `json:"memory_mib"`
	DiskMiB   int    `json:"disk_mib"`
}

// kindOf is the gate check a request needs.
func kindOf(r Request) metering.Kind {
	if r.Kind == kindBuild {
		return metering.KindBuild
	}
	return metering.KindDeploy
}

// workspaceError is a refusal whose text a workspace may read. It names no
// host. Unwrap gives the sentinel or the gate's error, if there is one.
type workspaceError struct {
	msg string
	err error
}

func (e *workspaceError) Error() string { return e.msg }
func (e *workspaceError) Unwrap() error { return e.err }

// errCouldNotPlace stands in for any failure whose text could say something
// about a host.
var errCouldNotPlace = &workspaceError{msg: "Loomplane Cloud couldn't place this right now."}

func noCapacity(r *Region) error {
	return &workspaceError{err: ErrNoCapacity, msg: fmt.Sprintf("Loomplane Cloud has no room for this in %s right now.", r.DisplayName)}
}

// amount says how to write a quantity in a limit refusal: one and many for
// what is needed and left, limit for the limit itself.
type amount struct{ one, many, limit string }

var (
	vcpuAmount   = amount{"%d vCPU", "%d vCPUs", "%d vCPU"}
	memoryAmount = amount{"%d MiB of memory", "%d MiB of memory", "%d MiB memory"}
	vmAmount     = amount{"%d VM", "%d VMs", "%d VM"}
	buildAmount  = amount{"%d concurrent build", "%d concurrent builds", "%d concurrent build"}
)

func (a amount) of(n int) string {
	if n == 1 {
		return fmt.Sprintf(a.one, n)
	}
	return fmt.Sprintf(a.many, n)
}

func limitError(a amount, need, used, limit int) error {
	left := max(limit-used, 0)
	return &workspaceError{err: ErrLimit, msg: fmt.Sprintf("This needs %s but the workspace has %s left of its %s limit.",
		a.of(need), a.of(left), fmt.Sprintf(a.limit, limit))}
}

// workspaceSafe passes on the refusals a workspace may read and replaces
// anything else, such as a raw driver error, with a generic one. The original
// is logged, without host fields, for the platform owner.
func workspaceSafe(ctx context.Context, op string, req Request, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*workspaceError); ok { //nolint:errorlint // only an unwrapped refusal is known to be safe
		return err
	}
	// Only the bare sentinels: a wrapped one could carry host text.
	if err == ErrNotEnabled || err == ErrSuspended || err == ErrConflict { //nolint:errorlint // see above
		return err
	}
	slog.ErrorContext(ctx, "platform cloud "+op+" failed", "org", req.OrgID, "environment", req.EnvironmentID, "resource", req.Resource, "error", err)
	return errCouldNotPlace
}

func validRequest(req *Request) error {
	if req.Kind == "" {
		req.Kind = kindService
	}
	switch {
	case req.OrgID == "" || req.EnvironmentID == "" || req.Resource == "" || req.Region == "":
		return &workspaceError{msg: "A platform placement needs an organization, environment, resource and region."}
	case req.Kind != kindService && req.Kind != kindBuild:
		return &workspaceError{msg: fmt.Sprintf("Loomplane Cloud doesn't place a %q.", req.Kind)}
	case req.VCPUs < 1 || req.MemoryMiB < 1 || req.DiskMiB < 0:
		return &workspaceError{msg: "A platform placement needs at least one vCPU and one MiB of memory."}
	}
	return nil
}

// Preview reports whether the request could be placed now: the tenant is
// enabled and not suspended, the gate lets it run, it fits the workspace's
// limits and some ready host in the region has room. It writes nothing and
// holds no lock, so Reserve checks it all again.
func (s *Service) Preview(ctx context.Context, req Request) (*Ref, error) {
	ref, err := s.preview(ctx, req)
	if err != nil {
		return nil, workspaceSafe(ctx, "preview", req, err)
	}
	return ref, nil
}

func (s *Service) preview(ctx context.Context, req Request) (*Ref, error) {
	if err := validRequest(&req); err != nil {
		return nil, err
	}
	t, err := s.store.GetTenant(ctx, req.OrgID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotEnabled
	}
	if err != nil {
		return nil, err
	}
	if t.Status != tenantEnabled {
		return nil, ErrNotEnabled
	}
	if admitErr := s.admit(ctx, t, req); admitErr != nil {
		return nil, admitErr
	}
	defaults, err := s.defaultLimits(ctx)
	if err != nil {
		return nil, err
	}
	region, old, err := s.checkLimits(ctx, s.store.db, req, limitsOf(t, defaults))
	if err != nil {
		return nil, err
	}
	hosts, err := s.candidates(ctx, s.store.db, req, region, old)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, noCapacity(region)
	}
	return &Ref{Region: region.Name, VCPUs: req.VCPUs, MemoryMiB: req.MemoryMiB, DiskMiB: req.DiskMiB}, nil
}

// Reserve places the request on a host and records it reserved. Under the
// tenant lock it checks the gate and the limits again, picks a ready host
// with fresh evidence and the right architecture, locks that host and checks
// it still fits before inserting. It prefers the host with the fewest of the
// workspace's placements, then the most free memory, then the lowest ID. A
// resource that already has a live placement stays on its host unless that
// host can no longer take it; either way the old placement is released in
// the same transaction, so a resize only needs the difference.
//
// Lock order: tenant row (WithTenantLock), then host rows (LockHost). The
// caller must not hold a host lock, and must take the organization's
// advisory lock, if it needs it, before calling.
func (s *Service) Reserve(ctx context.Context, req Request) (*Placement, error) {
	p, err := s.reserve(ctx, req)
	if err != nil {
		return nil, workspaceSafe(ctx, "reserve", req, err)
	}
	return p, nil
}

func (s *Service) reserve(ctx context.Context, req Request) (*Placement, error) {
	if err := validRequest(&req); err != nil {
		return nil, err
	}
	// Read before the lock: nothing inside it may use another connection.
	defaults, err := s.defaultLimits(ctx)
	if err != nil {
		return nil, err
	}
	var out *Placement
	err = s.store.WithTenantLock(ctx, req.OrgID, func(tx driverTx) error {
		t, lockErr := lockTenantTx(ctx, tx, req.OrgID)
		if lockErr != nil {
			return lockErr
		}
		if admitErr := s.admit(ctx, t, req); admitErr != nil {
			return admitErr
		}
		region, old, checkErr := s.checkLimits(ctx, tx, req, limitsOf(t, defaults))
		if checkErr != nil {
			return checkErr
		}
		hosts, listErr := s.candidates(ctx, tx, req, region, old)
		if listErr != nil {
			return listErr
		}
		if len(hosts) == 0 {
			return noCapacity(region)
		}
		if old != nil {
			if _, execErr := tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,updated_at=now() WHERE id=$1`, old.ID, stateReleased); execErr != nil {
				return execErr
			}
		}
		for _, h := range hosts {
			p, tryErr := s.tryHost(ctx, tx, req, region, h.ID)
			if tryErr != nil {
				return tryErr
			}
			if p != nil {
				out = p
				return nil
			}
		}
		return noCapacity(region)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// tryHost locks the host, checks it can still take the request and inserts
// the placement. It returns nil when the host can't take it, having let the
// host's lock go again, so trying hosts one after another never holds more
// than the one it placed on.
func (s *Service) tryHost(ctx context.Context, tx driverTx, req Request, region *Region, hostID string) (*Placement, error) {
	if _, err := tx.Exec(ctx, `SAVEPOINT platform_place_host`); err != nil {
		return nil, err
	}
	h, err := s.store.LockHost(ctx, tx, hostID)
	if err != nil {
		return nil, err
	}
	if s.eligible(h, req) {
		all, org, loadErr := hostLoad(ctx, tx, h.ID, req.OrgID, "")
		if loadErr != nil {
			return nil, loadErr
		}
		if fits(h, all, org, req) {
			p := &Placement{ID: newID("plc_"), OrgID: req.OrgID, ProjectID: req.ProjectID, EnvironmentID: req.EnvironmentID, Resource: req.Resource,
				Kind: req.Kind, RegionID: region.ID, HostID: &h.ID, VCPUs: req.VCPUs, MemoryMiB: req.MemoryMiB, DiskMiB: req.DiskMiB, State: stateReserved}
			_, err = tx.Exec(ctx, `INSERT INTO loomplane_platform_placements(id,org_id,project_id,environment_id,resource,kind,region_id,host_id,vcpus,memory_mib,disk_mib,state,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now(),now())`,
				p.ID, p.OrgID, p.ProjectID, p.EnvironmentID, p.Resource, p.Kind, p.RegionID, h.ID, p.VCPUs, p.MemoryMiB, p.DiskMiB, p.State)
			if err != nil {
				return nil, storeError(err)
			}
			return p, nil
		}
	}
	if _, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT platform_place_host`); err != nil {
		return nil, err
	}
	return nil, nil
}

// admit refuses a suspended tenant and anything the gate refuses.
func (s *Service) admit(ctx context.Context, t *Tenant, req Request) error {
	if len(t.SuspendReasons) > 0 {
		return ErrSuspended
	}
	if err := s.canRun().CanRun(ctx, req.OrgID, kindOf(req), true); err != nil {
		return &workspaceError{err: err, msg: "Loomplane Cloud can't start this for the workspace: " + err.Error()}
	}
	return nil
}

func (s *Service) defaultLimits(ctx context.Context) (Limits, error) {
	cfg, err := s.settings.PlatformCloud(ctx)
	if err != nil {
		return Limits{}, err
	}
	return cfg.DefaultLimits, nil
}

func limitsOf(t *Tenant, defaults Limits) Limits {
	if t.Limits != nil {
		return *t.Limits
	}
	return defaults
}

// checkLimits resolves the region, finds the resource's live placement, if
// any, and checks the request against the workspace's limits as if that
// placement were already released. Builds count only against concurrent
// builds and services only against vCPUs, memory and VMs.
func (s *Service) checkLimits(ctx context.Context, q querier, req Request, limits Limits) (*Region, *Placement, error) {
	region, err := regionByName(ctx, q, req.Region)
	if err != nil {
		return nil, nil, err
	}
	old, err := livePlacement(ctx, q, req)
	if err != nil {
		return nil, nil, err
	}
	if old != nil && old.Kind != req.Kind {
		return nil, nil, ErrConflict
	}
	use, err := tenantUse(ctx, q, req.OrgID)
	if err != nil {
		return nil, nil, err
	}
	if req.Kind == kindBuild {
		if old != nil && (old.State == stateReserved || old.State == stateRunning) {
			use.ConcurrentBuilds--
		}
		if use.ConcurrentBuilds+1 > limits.ConcurrentBuilds {
			return nil, nil, limitError(buildAmount, 1, use.ConcurrentBuilds, limits.ConcurrentBuilds)
		}
		return region, old, nil
	}
	if old != nil {
		use.VCPUs -= old.VCPUs
		use.MemoryMiB -= old.MemoryMiB
		use.VMs--
	}
	switch {
	case use.VCPUs+req.VCPUs > limits.VCPUs:
		return nil, nil, limitError(vcpuAmount, req.VCPUs, use.VCPUs, limits.VCPUs)
	case use.MemoryMiB+req.MemoryMiB > limits.MemoryMiB:
		return nil, nil, limitError(memoryAmount, req.MemoryMiB, use.MemoryMiB, limits.MemoryMiB)
	case use.VMs >= limits.VMs:
		return nil, nil, limitError(vmAmount, 1, use.VMs, limits.VMs)
	}
	return region, old, nil
}

func regionByName(ctx context.Context, q querier, name string) (*Region, error) {
	var r Region
	err := q.QueryRow(ctx, `SELECT id,name,display_name,status,revision FROM loomplane_platform_regions WHERE name=$1`, name).
		Scan(&r.ID, &r.Name, &r.DisplayName, &r.Status, &r.Revision)
	if err = storeError(err); errors.Is(err, ErrNotFound) {
		return nil, &workspaceError{err: ErrNotFound, msg: fmt.Sprintf("Loomplane Cloud has no region called %q.", name)}
	}
	if err != nil {
		return nil, err
	}
	if r.Status != "active" {
		return nil, &workspaceError{err: ErrNoCapacity, msg: fmt.Sprintf("Loomplane Cloud no longer takes new work in %s.", r.DisplayName)}
	}
	return &r, nil
}

// livePlacement is the resource's placement that is not released, or nil.
func livePlacement(ctx context.Context, q querier, req Request) (*Placement, error) {
	p, err := scanPlacement(q.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements
WHERE org_id=$1 AND environment_id=$2 AND resource=$3 AND state<>'released'`, req.OrgID, req.EnvironmentID, req.Resource))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return p, err
}

// eligible reports whether the host takes new placements of the request's
// architecture at all.
func (s *Service) eligible(h *Host, req Request) bool {
	return h.Status == hostReady && h.Fresh(s.now()) && (req.Architecture == "" || h.Evidence.Architecture == req.Architecture)
}

// hostLoad adds up the live placements on a host, all of them and the
// organization's, leaving out the placement exclude.
func hostLoad(ctx context.Context, q querier, hostID, orgID, exclude string) (all, org Allotment, err error) {
	err = q.QueryRow(ctx, `SELECT
coalesce(sum(vcpus),0)::int,coalesce(sum(memory_mib),0)::int,coalesce(sum(disk_mib),0)::int,count(*)::int,
coalesce(sum(vcpus) FILTER (WHERE org_id=$2),0)::int,coalesce(sum(memory_mib) FILTER (WHERE org_id=$2),0)::int,count(*) FILTER (WHERE org_id=$2)::int
FROM loomplane_platform_placements WHERE host_id=$1 AND state<>'released' AND id<>$3`, hostID, orgID, exclude).
		Scan(&all.VCPUs, &all.MemoryMiB, &all.DiskMiB, &all.VMs, &org.VCPUs, &org.MemoryMiB, &org.VMs)
	return all, org, err
}

// fits reports whether the request fits what is left of the host's
// allotment and the per-organization caps the host reported.
func fits(h *Host, all, org Allotment, req Request) bool {
	a, ev := h.Allotment, h.Evidence
	if all.VCPUs+req.VCPUs > a.VCPUs || all.MemoryMiB+req.MemoryMiB > a.MemoryMiB || all.DiskMiB+req.DiskMiB > a.DiskMiB || all.VMs+1 > a.VMs {
		return false
	}
	if ev.PerOrgVMs > 0 && org.VMs+1 > ev.PerOrgVMs {
		return false
	}
	if ev.PerOrgVCPUs > 0 && org.VCPUs+req.VCPUs > ev.PerOrgVCPUs {
		return false
	}
	return ev.PerOrgMemoryMiB <= 0 || org.MemoryMiB+req.MemoryMiB <= ev.PerOrgMemoryMiB
}

// candidates lists the region's hosts that could take the request, best
// first. The resource's current host leads when it can keep it.
func (s *Service) candidates(ctx context.Context, q querier, req Request, region *Region, old *Placement) ([]Host, error) {
	hosts, err := listHosts(ctx, q, region.ID)
	if err != nil {
		return nil, err
	}
	exclude, keep := "", ""
	if old != nil {
		exclude = old.ID
		if old.HostID != nil && old.RegionID == region.ID {
			keep = *old.HostID
		}
	}
	type ranked struct {
		host    Host
		orgVMs  int
		freeMem int
	}
	var out []ranked
	for i := range hosts {
		h := &hosts[i]
		if !s.eligible(h, req) {
			continue
		}
		all, org, loadErr := hostLoad(ctx, q, h.ID, req.OrgID, exclude)
		if loadErr != nil {
			return nil, loadErr
		}
		if !fits(h, all, org, req) {
			continue
		}
		out = append(out, ranked{host: *h, orgVMs: org.VMs, freeMem: h.Allotment.MemoryMiB - all.MemoryMiB})
	}
	slices.SortFunc(out, func(a, b ranked) int {
		if ak, bk := a.host.ID == keep, b.host.ID == keep; ak != bk {
			if ak {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.orgVMs, b.orgVMs), cmp.Compare(b.freeMem, a.freeMem), cmp.Compare(a.host.ID, b.host.ID))
	})
	sorted := make([]Host, len(out))
	for i, r := range out {
		sorted[i] = r.host
	}
	return sorted, nil
}

// Release gives a placement's room back. Releasing one already released
// changes nothing.
func (s *Service) Release(ctx context.Context, placementID string) error {
	res, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,updated_at=now() WHERE id=$1 AND state<>$2`, placementID, stateReleased)
	if err != nil {
		return storeError(err)
	}
	if n, nErr := res.RowsAffected(); nErr != nil || n == 1 {
		return nErr
	}
	return placementExists(ctx, s.store.db, placementID)
}

// SetState records where a placement's workload is: reserved, running,
// stopped or suspended. Released goes through Release. A released placement
// stays released.
func (s *Service) SetState(ctx context.Context, placementID, state string) error {
	switch state {
	case stateReleased:
		return s.Release(ctx, placementID)
	case stateReserved, stateRunning, stateStopped, stateSuspended:
	default:
		return fmt.Errorf("platformcloud: unknown placement state %q", state)
	}
	res, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements
SET state=$2,suspended_at=CASE WHEN $2='suspended' THEN coalesce(suspended_at,$3) ELSE NULL END,updated_at=$3
WHERE id=$1 AND state<>'released'`, placementID, state, s.now().UTC())
	if err != nil {
		return storeError(err)
	}
	if n, nErr := res.RowsAffected(); nErr != nil || n == 1 {
		return nErr
	}
	if existsErr := placementExists(ctx, s.store.db, placementID); existsErr != nil {
		return existsErr
	}
	return fmt.Errorf("%w: the placement is released", ErrConflict)
}

func placementExists(ctx context.Context, q querier, placementID string) error {
	var one int
	return storeError(q.QueryRow(ctx, `SELECT 1 FROM loomplane_platform_placements WHERE id=$1`, placementID).Scan(&one))
}
