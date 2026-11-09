package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	cpauth "github.com/xraph/ctrlplane/auth"

	"github.com/xraph/loomplane/internal/buildpolicy"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

// A placement's instance_id names the Firecracker scope resource its VM runs
// under on its host. A released placement whose instance_id is still set may
// have left its VM behind (a move whose old VM could not be removed yet), so
// whatever removes that VM clears it through Removed. The cleanup job sweeps
// what is left the same way.

// errHostUnreachable stands in for anything that went wrong reaching a host.
// Its text never names the host.
var errHostUnreachable = &workspaceError{msg: "Loomplane Cloud couldn't reach the host running this right now."}

// errNoWorkload is a workload with no placement the caller may reach.
var errNoWorkload = &workspaceError{err: ErrNotFound, msg: "Loomplane Cloud has no placement for this workload."}

// sessionError logs err for the platform owner and returns what a workspace
// may read instead.
func sessionError(ctx context.Context, op, orgID, placementID string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*workspaceError); ok { //nolint:errorlint // only an unwrapped refusal is known to be safe
		return err
	}
	if err == ErrNotEnabled || err == ErrSuspended || err == ErrConflict || err == ErrNotFound { //nolint:errorlint // bare sentinels only
		return err
	}
	slog.ErrorContext(ctx, "platform cloud "+op+" failed", "org", orgID, "placement", placementID, "error", err)
	return errHostUnreachable
}

// checkTenant refuses a caller acting for another organization.
func checkTenant(ctx context.Context, orgID string) error {
	if claims := cpauth.ClaimsFrom(ctx); claims != nil && claims.TenantID != orgID {
		return ErrConflict
	}
	return nil
}

// Scope is the Firecracker scope a placement's VM runs under: the
// organization's, on the platform connection.
func (p *Placement) Scope() fc.Scope {
	return fc.Scope{Org: p.OrgID, Connection: fc.PlatformConnection, Project: p.ProjectID, Environment: p.EnvironmentID, Resource: p.InstanceID}
}

// Session reserves room for req and opens the host it is placed on. A
// resource with a live placement keeps its host while that host can still
// take it (Reserve). resource is the Firecracker scope resource the VM runs
// under, the Ctrl Plane instance ID, and is recorded as the placement's
// instance before the host is opened, so a VM a failed apply leaves behind
// can still be found. The host must still report the runtime identity and
// platform evidence it passed its last check with. The caller closes the
// client.
func (s *Service) Session(ctx context.Context, orgID, projectID, envID, resource string, req Request) (*fc.Client, fc.Scope, *Placement, error) {
	if req.OrgID != orgID || req.ProjectID != projectID || req.EnvironmentID != envID || resource == "" {
		return nil, fc.Scope{}, nil, &workspaceError{msg: "A platform session needs the workload's own organization, project and environment."}
	}
	if err := checkTenant(ctx, orgID); err != nil {
		return nil, fc.Scope{}, nil, err
	}
	p, err := s.Reserve(ctx, req)
	if err != nil {
		return nil, fc.Scope{}, nil, err
	}
	if err = s.setInstance(ctx, p.ID, resource); err != nil {
		return nil, fc.Scope{}, nil, sessionError(ctx, "session", orgID, p.ID, err)
	}
	p.InstanceID = resource
	h, err := s.store.GetHost(ctx, *p.HostID)
	if err != nil {
		return nil, fc.Scope{}, nil, sessionError(ctx, "session", orgID, p.ID, err)
	}
	c, err := s.clientFor(ctx, h)
	if err != nil {
		return nil, fc.Scope{}, nil, sessionError(ctx, "session", orgID, p.ID, err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	caps, err := c.Check(checkCtx)
	if err == nil && caps.RuntimeIdentity != h.Evidence.RuntimeIdentity {
		err = errors.New("the host's runtime identity changed since its last check")
	}
	if err == nil {
		err = buildpolicy.CheckPlatform(platformEvidence(caps))
	}
	if err != nil {
		c.Close()
		return nil, fc.Scope{}, nil, sessionError(ctx, "session", orgID, p.ID, err)
	}
	scope := p.Scope()
	return c, scope, p, nil
}

// Attach opens the host the workload resource runs on, without reserving
// anything: the live placement of name (the placement's resource, the
// service) whose instance is resource. A placement whose VM was reclaimed has
// no host; Attach returns it with a nil client. It is ErrNotFound, in words a
// workspace may read, when there is no such placement.
func (s *Service) Attach(ctx context.Context, orgID, projectID, envID, resource, name string) (*fc.Client, fc.Scope, *Placement, error) {
	if err := checkTenant(ctx, orgID); err != nil {
		return nil, fc.Scope{}, nil, err
	}
	p, err := livePlacement(ctx, s.store.db, Request{OrgID: orgID, EnvironmentID: envID, Resource: name})
	if err != nil {
		return nil, fc.Scope{}, nil, sessionError(ctx, "attach", orgID, "", err)
	}
	if p == nil || p.ProjectID != projectID || p.InstanceID != resource {
		return nil, fc.Scope{}, nil, errNoWorkload
	}
	c, scope, err := s.Open(ctx, p)
	return c, scope, p, err
}

// Open opens the host of placement p, whatever its state, with the scope its
// VM runs under. A placement with no host gives a nil client.
func (s *Service) Open(ctx context.Context, p *Placement) (*fc.Client, fc.Scope, error) {
	if p == nil || p.InstanceID == "" {
		return nil, fc.Scope{}, errNoWorkload
	}
	if err := checkTenant(ctx, p.OrgID); err != nil {
		return nil, fc.Scope{}, err
	}
	if p.HostID == nil {
		return nil, p.Scope(), nil
	}
	c, err := s.client(ctx, *p.HostID)
	if err != nil {
		return nil, fc.Scope{}, sessionError(ctx, "open", p.OrgID, p.ID, err)
	}
	return c, p.Scope(), nil
}

// Leftovers are the released placements of the resource name whose VM may
// still be on their host: released, with a host and an instance.
func (s *Service) Leftovers(ctx context.Context, orgID, envID, name string) ([]Placement, error) {
	if err := checkTenant(ctx, orgID); err != nil {
		return nil, err
	}
	rows, err := s.store.db.Query(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements
WHERE org_id=$1 AND environment_id=$2 AND resource=$3 AND state='released' AND host_id IS NOT NULL AND instance_id<>''
ORDER BY created_at,id`, orgID, envID, name)
	if err != nil {
		return nil, sessionError(ctx, "leftovers", orgID, "", err)
	}
	defer rows.Close()
	out := []Placement{}
	for rows.Next() {
		p, scanErr := scanPlacement(rows)
		if scanErr != nil {
			return nil, sessionError(ctx, "leftovers", orgID, "", scanErr)
		}
		out = append(out, *p)
	}
	return out, sessionError(ctx, "leftovers", orgID, "", rows.Err())
}

// Running records that the organization's placement has its VM up as
// instance at generation. A placement that replaces another takes over from
// it here: in one transaction the old placement is released and the new one
// runs, after the old one's minutes up to now are counted, so the service is
// billed without a gap and no minute twice. While the organization is
// suspended it records nothing of the sort: the placement is kept suspended
// and remembered as running, the late VM is stopped, and the caller gets
// ErrSuspended, so a deploy that finished after the suspension can't bring
// the work back.
func (s *Service) Running(ctx context.Context, orgID, placementID, instance string, generation int64) error {
	if err := checkTenant(ctx, orgID); err != nil {
		return err
	}
	now := s.now().UTC()
	s.countReplaced(ctx, orgID, placementID, now)
	var found, suspended bool
	err := s.store.withTx(ctx, func(tx driverTx) error {
		var replaces string
		readErr := storeError(tx.QueryRow(ctx, `SELECT replaces FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2 AND state<>'released'`, placementID, orgID).Scan(&replaces))
		if errors.Is(readErr, ErrNotFound) {
			return s.isSuspended(ctx, tx, orgID, &suspended)
		}
		if readErr != nil {
			return readErr
		}
		// Both rows are locked in creation order, as a suspension locks
		// them, so the two never deadlock.
		if _, lockErr := tx.Exec(ctx, `SELECT 1 FROM loomplane_platform_placements WHERE id=ANY($1) ORDER BY created_at,id FOR UPDATE`, []string{placementID, replaces}); lockErr != nil {
			return lockErr
		}
		if readErr = storeError(tx.QueryRow(ctx, `SELECT replaces FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2 AND state<>'released'`, placementID, orgID).Scan(&replaces)); readErr != nil {
			if errors.Is(readErr, ErrNotFound) {
				return s.isSuspended(ctx, tx, orgID, &suspended)
			}
			return readErr
		}
		found = true
		if suspendedErr := s.isSuspended(ctx, tx, orgID, &suspended); suspendedErr != nil {
			return suspendedErr
		}
		if replaces != "" {
			// First, so the resource never has two current placements.
			if _, execErr := tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state='released',updated_at=now() WHERE id=$1 AND org_id=$2 AND state<>'released'`, replaces, orgID); execErr != nil {
				return execErr
			}
		}
		set := `state='running',suspended_at=NULL,restore_running=false,running_since=CASE WHEN state='running' AND generation=$4 THEN running_since ELSE $5 END`
		if suspended {
			set = `state='suspended',restore_running=true,suspended_at=coalesce(suspended_at,$5)`
		}
		_, execErr := tx.Exec(ctx, `UPDATE loomplane_platform_placements SET `+set+`,instance_id=$3,generation=$4,replaces='',updated_at=now()
WHERE id=$1 AND org_id=$2`, placementID, orgID, instance, generation, now)
		return storeError(execErr)
	})
	if err != nil {
		return err
	}
	if !suspended {
		if found {
			return nil
		}
		// A released or missing placement.
		var one int
		if existsErr := storeError(s.store.db.QueryRow(ctx, `SELECT 1 FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2`, placementID, orgID).Scan(&one)); existsErr != nil {
			return existsErr
		}
		return fmt.Errorf("%w: the placement is released", ErrConflict)
	}
	if !found {
		// The suspension released the placement, a deploy that never ran
		// before it: nothing tracks this VM, so stop it by the instance given.
		slog.WarnContext(ctx, "platform cloud: a deploy finished after its placement was released by a suspension", "org", orgID, "placement", placementID)
	}
	if s.instances != nil && instance != "" {
		if stopErr := callInstances(ctx, func(c context.Context) error { return s.instances.Stop(c, instance) }); stopErr != nil {
			slog.ErrorContext(ctx, "platform cloud could not stop a late deploy", "org", orgID, "placement", placementID, "error", stopErr)
		}
	}
	return ErrSuspended
}

func (s *Service) isSuspended(ctx context.Context, tx driverTx, orgID string, out *bool) error {
	return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_platform_tenants WHERE org_id=$1 AND cardinality(suspend_reasons)>0)`, orgID).Scan(out)
}

// countReplaced counts the minutes of the running placement that placement
// replaces, up to the minute now is in, before Running releases it. A
// failure is logged: the sampler has counted all but the last few minutes.
func (s *Service) countReplaced(ctx context.Context, orgID, placementID string, now time.Time) {
	old, err := scanPlacement(s.store.db.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements
WHERE id=(SELECT replaces FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2 AND state<>'released') AND org_id=$2 AND state='running'`, placementID, orgID))
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err == nil {
		err = s.countMinutes(ctx, *old, now.Truncate(time.Minute))
	}
	if err != nil {
		slog.WarnContext(ctx, "platform meter: could not count the last minutes of a replaced placement", "org", orgID, "placement", placementID, "error", err)
	}
}

// Abandon releases the organization's placement if it is a replacement that
// never ran: its deploy failed or was given up. The placement it would have
// replaced runs on. It keeps its host and instance, so whatever the deploy
// left on the host is swept. Any other placement is left as it is.
func (s *Service) Abandon(ctx context.Context, orgID, placementID string) error {
	if err := checkTenant(ctx, orgID); err != nil {
		return err
	}
	_, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET state='released',updated_at=now()
WHERE id=$1 AND org_id=$2 AND state='reserved' AND replaces<>''`, placementID, orgID)
	return storeError(err)
}

// Stopped records that someone stopped the organization's placement's VM.
// A user stop is never restarted by a restore, so restore_running is
// cleared.
func (s *Service) Stopped(ctx context.Context, orgID, placementID string) error {
	return s.setLive(ctx, orgID, placementID, `state='stopped',suspended_at=NULL,restore_running=false`)
}

// setLive updates a placement of the organization that isn't released. A
// released one is ErrConflict; one that isn't the organization's is
// ErrNotFound.
func (s *Service) setLive(ctx context.Context, orgID, placementID, set string, args ...any) error {
	if err := checkTenant(ctx, orgID); err != nil {
		return err
	}
	res, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET `+set+`,updated_at=now() WHERE id=$1 AND org_id=$2 AND state<>'released'`,
		append([]any{placementID, orgID}, args...)...)
	if err != nil {
		return storeError(err)
	}
	if n, nErr := res.RowsAffected(); nErr != nil || n == 1 {
		return nErr
	}
	var one int
	if existsErr := storeError(s.store.db.QueryRow(ctx, `SELECT 1 FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2`, placementID, orgID).Scan(&one)); existsErr != nil {
		return existsErr
	}
	return fmt.Errorf("%w: the placement is released", ErrConflict)
}

// Removed records that the organization's placement's VM is gone from its
// host: the placement is released, if it wasn't, with any replacement still
// waiting to take over from it, and no longer names an instance, so nothing
// tries to remove it again. A service nothing else runs
// any more loses its generated hostname.
func (s *Service) Removed(ctx context.Context, orgID, placementID string) error {
	if err := checkTenant(ctx, orgID); err != nil {
		return err
	}
	p, err := scanPlacement(s.store.db.QueryRow(ctx, `UPDATE loomplane_platform_placements SET state=$3,instance_id='',updated_at=now() WHERE id=$1 AND org_id=$2
RETURNING `+placementColumns, placementID, orgID, stateReleased))
	if err != nil {
		return err
	}
	// A replacement still waiting to take over from it goes with it. It
	// keeps its instance, so what its deploy left on its host is swept.
	if _, err = s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$3,updated_at=now() WHERE org_id=$1 AND replaces=$2 AND state<>$3`,
		orgID, placementID, stateReleased); err != nil {
		return storeError(err)
	}
	s.serviceGone(ctx, p)
	return nil
}

func (s *Service) setInstance(ctx context.Context, placementID, instance string) error {
	_, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET instance_id=$2,updated_at=now() WHERE id=$1 AND state<>'released'`, placementID, instance)
	return storeError(err)
}
