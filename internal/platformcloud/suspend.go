package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/notifications"
)

// ReclaimAfter is how long a workspace's work stays suspended before its VMs
// are taken off the hosts. The placements stay, so the workspace keeps its
// place in its limits, and a restore puts them on a host again.
const ReclaimAfter = 7 * 24 * time.Hour

// Notification kinds for a change in a workspace's platform work.
const (
	NoticeSuspended       = "platform_cloud.suspended"
	NoticeRestored        = "platform_cloud.restored"
	NoticeWaitingCapacity = "platform_cloud.waiting_capacity"

	noticePath = "/clouds"
)

// InstanceControl is what suspend, restore and reclaim ask of the machinery
// that runs a workspace's instances. Every call must be safe to repeat.
type InstanceControl interface {
	// Stop stops the instance's VM. Stopping a stopped one is not an error.
	Stop(ctx context.Context, instanceID string) error
	// Start starts the instance's VM, putting it on the host its placement
	// names. Starting a running one is not an error.
	Start(ctx context.Context, instanceID string) error
	// RemoveVM takes the placement's VM off its host. Removing one that is
	// already gone is not an error.
	RemoveVM(ctx context.Context, placement Placement) error
	// CancelBuilds cancels the organization's platform builds.
	CancelBuilds(ctx context.Context, orgID string) error
}

// Suspender is how billing, spending limits and the platform owner stop and
// restart an organization's platform work. An organization stays suspended
// while any reason is left.
type Suspender interface {
	Suspend(ctx context.Context, orgID string, reason SuspendReason) error
	Restore(ctx context.Context, orgID string, reason SuspendReason) error
}

var _ Suspender = (*Service)(nil)

// WithInstances sets what stops, starts and removes instances and returns the
// service. Without it Suspend, Restore and DisableTenant still record their
// reasons, but fail when there is a VM to stop or start.
func (s *Service) WithInstances(ic InstanceControl) *Service {
	s.instances = ic
	return s
}

func knownReason(r SuspendReason) bool {
	return r == ReasonSpendingLimit || r == ReasonPastDue || r == ReasonPlatformOwner
}

// instanceCallTimeout bounds every call to InstanceControl, and lockWait how
// long a suspend, restore or reclaim waits for a row another transaction
// holds, so one hung host or stuck transaction can't hold an organization
// up.
const (
	instanceCallTimeout = 60 * time.Second
	lockWait            = "10s"
)

// errEffects is what a caller of Suspend, Restore, DisableTenant or the cron
// jobs reads when a VM couldn't be stopped, started or removed. The details,
// which may name hosts or carry provider text, go to the platform owner's log.
var errEffects = &workspaceError{msg: "Loomplane Cloud couldn't finish changing this workspace's work right now. It will try again shortly."}

func effectsFailed(ctx context.Context, op, orgID string, errs ...error) error {
	err := errors.Join(errs...)
	if err == nil {
		return nil
	}
	slog.ErrorContext(ctx, "platform cloud "+op+" failed", "org", orgID, "error", err)
	return errEffects
}

// callInstances runs one InstanceControl call with a time limit.
func callInstances(ctx context.Context, fn func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, instanceCallTimeout)
	defer cancel()
	return fn(c)
}

// withTenantRow is WithTenantLock for a reason change: it also passes a
// disabled tenant, since the platform owner's disable is itself a reason.
// A missing tenant is ErrNotEnabled. It gives up on a lock held too long.
func (s *PgStore) withTenantRow(ctx context.Context, orgID string, fn func(driverTx) error) error {
	return s.withTx(ctx, func(tx driverTx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '`+lockWait+`'`); err != nil {
			return err
		}
		if _, err := lockTenantTx(ctx, tx, orgID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrNotEnabled
			}
			return err
		}
		return fn(tx)
	})
}

// Suspend adds reason to the organization's suspend reasons. The first one
// stops new platform work, even if the VMs can't be stopped right now; every
// call stops whatever still runs and remembers it for the restore. Calling it
// again changes and stops nothing.
func (s *Service) Suspend(ctx context.Context, orgID string, reason SuspendReason) error {
	if orgID == "" || !knownReason(reason) {
		return fmt.Errorf("platformcloud: suspend needs an organization and a known reason, got %q", reason)
	}
	var reasons []SuspendReason
	var revision int64
	added := false
	err := s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
		t, err := lockTenantTx(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if slices.Contains(t.SuspendReasons, reason) {
			return nil
		}
		next := *t
		next.SuspendReasons = append(slices.Clone(t.SuspendReasons), reason)
		if len(t.SuspendReasons) == 0 {
			at := s.now().UTC()
			next.SuspendedAt = &at
		}
		if err = updateTenantTx(ctx, tx, &next); err != nil {
			return err
		}
		added, reasons, revision = true, next.SuspendReasons, next.Revision
		return nil
	})
	if err != nil {
		return err
	}
	if added {
		s.notifyOrg(ctx, orgID, NoticeSuspended, fmt.Sprintf("%s:%d", orgID, revision), "Loomplane Cloud work is stopped", suspendMessage(reasons))
	}
	return s.stopWork(ctx, orgID)
}

// Restore removes reason. When it was the last one, the work that was
// running comes back: placements that lost their VM are placed again first,
// and one with no room waits for RetryRestores. The platform owner's reason
// is not Restore's to lift: enabling the tenant does that.
func (s *Service) Restore(ctx context.Context, orgID string, reason SuspendReason) error {
	if orgID == "" || !knownReason(reason) {
		return fmt.Errorf("platformcloud: restore needs an organization and a known reason, got %q", reason)
	}
	if reason == ReasonPlatformOwner {
		return fmt.Errorf("%w: the platform owner's suspension is lifted by enabling the tenant", ErrConflict)
	}
	cleared := false
	var revision int64
	err := s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
		t, err := lockTenantTx(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if !slices.Contains(t.SuspendReasons, reason) {
			return nil
		}
		next := *t
		next.SuspendReasons = slices.DeleteFunc(slices.Clone(t.SuspendReasons), func(r SuspendReason) bool { return r == reason })
		if len(next.SuspendReasons) == 0 {
			next.SuspendedAt = nil
			cleared = true
		}
		if err = updateTenantTx(ctx, tx, &next); err != nil {
			return err
		}
		revision = next.Revision
		return nil
	})
	if err != nil {
		return err
	}
	if !cleared {
		return nil
	}
	s.notifyOrg(ctx, orgID, NoticeRestored, fmt.Sprintf("%s:%d", orgID, revision), "Loomplane Cloud work is starting again", "Loomplane Cloud work is starting again because everything that stopped it is resolved.")
	return s.restoreWork(ctx, orgID)
}

type stopTarget struct{ id, instance string }

// stopWork stops the organization's running platform services and cancels its
// builds, if the organization is suspended. It reads the work under the tenant
// lock, stops it with the lock let go, and then marks what it stopped
// suspended and remembered for the restore, if it is still running as the
// same instance. A service the user stopped is left stopped. A failure on one
// service doesn't keep the others from stopping, and every step is safe to
// repeat, so a job that crashed or failed can run it again.
func (s *Service) stopWork(ctx context.Context, orgID string) error {
	var targets []stopTarget
	builds := false
	err := s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
		t, err := lockTenantTx(ctx, tx, orgID)
		if err != nil || len(t.SuspendReasons) == 0 {
			return err
		}
		live, err := queryPlacements(ctx, tx, `org_id=$1 AND state IN ('reserved','running') FOR UPDATE`, orgID)
		if err != nil {
			return err
		}
		for i := range live {
			p := &live[i]
			switch {
			case p.Kind == kindBuild:
				builds = true
			case p.State == stateReserved && p.InstanceID == "":
				// Nothing was ever started: new work, which suspension blocks.
				if _, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,updated_at=now() WHERE id=$1`, p.ID, stateReleased); err != nil {
					return err
				}
			default:
				targets = append(targets, stopTarget{p.ID, p.InstanceID})
			}
		}
		return nil
	})
	if err != nil {
		return effectsFailed(ctx, "suspend", orgID, err)
	}
	if len(targets) == 0 && !builds {
		return nil
	}
	if s.instances == nil {
		return effectsFailed(ctx, "suspend", orgID, errors.New("no instance control"))
	}
	var errs []error
	var stopped []stopTarget
	for _, tg := range targets {
		if tg.instance != "" {
			if stopErr := callInstances(ctx, func(c context.Context) error { return s.instances.Stop(c, tg.instance) }); stopErr != nil {
				errs = append(errs, fmt.Errorf("stop placement %s: %w", tg.id, stopErr))
				continue
			}
		}
		stopped = append(stopped, tg)
	}
	cancelled := false
	if builds {
		if cancelErr := callInstances(ctx, func(c context.Context) error { return s.instances.CancelBuilds(c, orgID) }); cancelErr != nil {
			errs = append(errs, fmt.Errorf("cancel builds: %w", cancelErr))
		} else {
			cancelled = true
		}
	}
	if len(stopped) > 0 || cancelled {
		now := s.now().UTC()
		err = s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
			for _, tg := range stopped {
				// Still running as the same instance: remember it. One a
				// restore or a redeploy changed since is left as it is.
				if _, execErr := tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$3,restore_running=true,waiting_capacity=false,suspended_at=$4,updated_at=now()
WHERE id=$1 AND instance_id=$2 AND state IN ('reserved','running')`, tg.id, tg.instance, stateSuspended, now); execErr != nil {
					return execErr
				}
			}
			if cancelled {
				// A cancelled build's VM is left for the cleanup sweep of released placements.
				_, execErr := tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,updated_at=now() WHERE org_id=$1 AND kind='build' AND state IN ('reserved','running')`, orgID, stateReleased)
				return execErr
			}
			return nil
		})
		errs = append(errs, err)
	}
	return effectsFailed(ctx, "suspend", orgID, errs...)
}

// restoreWork starts what the organization's suspension stopped, if it has no
// reason left. A placement that lost its VM to a reclaim is placed again; if
// no host has room, or the placement is refused for another reason, it keeps
// waiting and RetryRestores comes back to it. Starting happens under the
// tenant lock after a fresh look at the placement, so two restores running
// together start it once.
func (s *Service) restoreWork(ctx context.Context, orgID string) error {
	waiting, err := queryPlacements(ctx, s.store.db, `org_id=$1 AND kind='service' AND state='suspended' AND restore_running`, orgID)
	if err != nil {
		return effectsFailed(ctx, "restore", orgID, err)
	}
	var errs []error
	for i := range waiting {
		p := waiting[i]
		if p.HostID == nil {
			placed, placeErr := s.placeAgain(ctx, p)
			switch {
			case errors.Is(placeErr, ErrSuspended), errors.Is(placeErr, ErrNotEnabled):
				return effectsFailed(ctx, "restore", orgID, errs...)
			case placeErr != nil:
				if !errors.Is(placeErr, ErrNoCapacity) {
					slog.ErrorContext(ctx, "platform cloud could not place a restore again", "org", orgID, "placement", p.ID, "error", placeErr)
				}
				s.markWaiting(ctx, p)
				continue
			case placed == nil:
				continue
			}
			p = *placed
		}
		if startErr := s.startPlacement(ctx, orgID, p.ID); startErr != nil {
			if errors.Is(startErr, ErrNotEnabled) {
				return effectsFailed(ctx, "restore", orgID, errs...)
			}
			errs = append(errs, fmt.Errorf("start placement %s: %w", p.ID, startErr))
		}
	}
	return effectsFailed(ctx, "restore", orgID, errs...)
}

// placeAgain gives a suspended placement that has no host a new one, in one
// transaction under the tenant lock: it re-reads the old row, places the
// service through the same code as Reserve and inserts the new row already
// suspended and remembered, releasing the old. Either all of that happens or
// none, so a crash or a concurrent suspend never loses the service. The
// result is nil when the old row is no longer waiting for a host.
func (s *Service) placeAgain(ctx context.Context, p Placement) (*Placement, error) {
	var region string
	if err := s.store.db.QueryRow(ctx, `SELECT name FROM loomplane_platform_regions WHERE id=$1`, p.RegionID).Scan(&region); err != nil {
		return nil, storeError(err)
	}
	req := Request{OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, Resource: p.Resource, Region: region, Kind: p.Kind,
		VCPUs: p.VCPUs, MemoryMiB: p.MemoryMiB, DiskMiB: p.DiskMiB}
	defaults, err := s.defaultLimits(ctx)
	if err != nil {
		return nil, err
	}
	var out *Placement
	err = s.store.withTenantRow(ctx, p.OrgID, func(tx driverTx) error {
		t, lockErr := lockTenantTx(ctx, tx, p.OrgID)
		if lockErr != nil {
			return lockErr
		}
		if t.Status != tenantEnabled {
			return ErrNotEnabled
		}
		old, readErr := scanPlacement(tx.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2 FOR UPDATE`, p.ID, p.OrgID))
		if errors.Is(readErr, ErrNotFound) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if old.State != stateSuspended || !old.RestoreRunning || old.HostID != nil {
			return nil
		}
		np, placeErr := s.reserveTx(ctx, tx, t, req, defaults, old)
		out = np
		return placeErr
	})
	if err != nil {
		return nil, workspaceSafe(ctx, "restore", req, err)
	}
	return out, nil
}

// markWaiting records that a placement can't come back yet, and says so once.
func (s *Service) markWaiting(ctx context.Context, p Placement) {
	res, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET waiting_capacity=true,updated_at=now() WHERE id=$1 AND state=$2 AND NOT waiting_capacity`, p.ID, stateSuspended)
	if err != nil {
		slog.ErrorContext(ctx, "platform cloud could not mark a restore waiting", "org", p.OrgID, "placement", p.ID, "error", err)
		return
	}
	if n, nErr := res.RowsAffected(); nErr != nil || n != 1 {
		return
	}
	subject := p.ID
	if p.SuspendedAt != nil {
		subject += fmt.Sprintf(":%d", p.SuspendedAt.Unix())
	}
	s.notifyOrg(ctx, p.OrgID, NoticeWaitingCapacity, subject, "Loomplane Cloud work is waiting for room",
		"Loomplane Cloud can't start your work again yet. It starts as soon as it can.")
}

// startPlacement starts one remembered placement under the tenant lock, if
// the organization is enabled and not suspended and the placement still waits
// for it. The start has a time limit.
func (s *Service) startPlacement(ctx context.Context, orgID, placementID string) error {
	return s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
		t, err := lockTenantTx(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if t.Status != tenantEnabled {
			return ErrNotEnabled
		}
		if len(t.SuspendReasons) > 0 {
			return nil
		}
		p, err := scanPlacement(tx.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2 FOR UPDATE`, placementID, orgID))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.State != stateSuspended || !p.RestoreRunning || p.HostID == nil {
			return nil
		}
		if p.InstanceID != "" {
			if err = callInstances(ctx, func(c context.Context) error { return s.instances.Start(c, p.InstanceID) }); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,restore_running=false,waiting_capacity=false,suspended_at=NULL,running_since=$3,updated_at=now() WHERE id=$1`, p.ID, stateRunning, s.now().UTC())
		return err
	})
}

// Reclaim first stops again whatever a suspended organization still has
// running (a stop that failed, a deploy that finished late), then takes the VM
// off its host for every platform service that has been suspended for
// ReclaimAfter. The placement stays suspended and still counts toward the
// workspace's use; only the host's room is freed. Run it from cron.
func (s *Service) Reclaim(ctx context.Context, now time.Time) error {
	if s.instances == nil {
		return errors.New("platformcloud: no instance control to reclaim with")
	}
	errs := []error{s.resweep(ctx)}
	due, err := queryPlacements(ctx, s.store.db, `kind='service' AND state='suspended' AND host_id IS NOT NULL AND suspended_at<=$1
AND org_id IN (SELECT org_id FROM loomplane_platform_tenants WHERE cardinality(suspend_reasons)>0)`, now.Add(-ReclaimAfter).UTC())
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, p := range due {
		errs = append(errs, s.reclaim(ctx, p.OrgID, p.ID, now))
	}
	return errors.Join(errs...)
}

// resweep runs stopWork for every suspended organization that has work
// reserved or running.
func (s *Service) resweep(ctx context.Context) error {
	orgs, err := s.orgsWhere(ctx, `SELECT DISTINCT org_id FROM loomplane_platform_placements WHERE state IN ('reserved','running')
AND org_id IN (SELECT org_id FROM loomplane_platform_tenants WHERE cardinality(suspend_reasons)>0) ORDER BY org_id`)
	if err != nil {
		return err
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, s.stopWork(ctx, org))
	}
	return errors.Join(errs...)
}

// reclaim removes one placement's VM under the tenant lock, the way Start
// runs: it checks the placement is still the due suspended one, removes the VM
// with a time limit and clears the host in the same transaction. A restore
// that clears the last reason waits for the lock, so the VM is either removed
// and the row stays suspended and remembered, or the restore ran first and
// the reclaim finds nothing due. A placement is never left running without
// its VM.
func (s *Service) reclaim(ctx context.Context, orgID, placementID string, now time.Time) error {
	err := s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
		t, err := lockTenantTx(ctx, tx, orgID)
		if err != nil || len(t.SuspendReasons) == 0 {
			return err
		}
		p, err := scanPlacement(tx.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2 FOR UPDATE`, placementID, orgID))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.State != stateSuspended || p.HostID == nil || p.SuspendedAt == nil || p.SuspendedAt.After(now.Add(-ReclaimAfter)) {
			return nil
		}
		if err = callInstances(ctx, func(c context.Context) error { return s.instances.RemoveVM(c, *p) }); err != nil {
			return fmt.Errorf("remove placement %s: %w", p.ID, err)
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET host_id=NULL,updated_at=now() WHERE id=$1`, p.ID)
		return err
	})
	return effectsFailed(ctx, "reclaim", orgID, err)
}

// RetryRestores brings back work that should be running but isn't: a restore
// that had no room, or one that failed or was cut short. It looks at every
// organization with no suspend reason left and a placement still remembered
// as running, so it also covers a tenant the platform owner enabled again.
// Run it from cron.
func (s *Service) RetryRestores(ctx context.Context) error {
	if s.instances == nil {
		return errors.New("platformcloud: no instance control to restore with")
	}
	orgs, err := s.orgsWhere(ctx, `SELECT DISTINCT p.org_id FROM loomplane_platform_placements p
JOIN loomplane_platform_tenants t ON t.org_id=p.org_id
WHERE p.kind='service' AND p.state='suspended' AND p.restore_running AND t.status='enabled' AND cardinality(t.suspend_reasons)=0
ORDER BY p.org_id`)
	if err != nil {
		return err
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, s.restoreWork(ctx, org))
	}
	return errors.Join(errs...)
}

func (s *Service) orgsWhere(ctx context.Context, query string) ([]string, error) {
	rows, err := s.store.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var orgs []string
	for rows.Next() {
		var org string
		if scanErr := rows.Scan(&org); scanErr != nil {
			return nil, scanErr
		}
		orgs = append(orgs, org)
	}
	return orgs, rows.Err()
}

// queryPlacements reads the placements matching where, in creation order.
// where may end in FOR UPDATE; the order is then left out.
func queryPlacements(ctx context.Context, q querier, where string, args ...any) ([]Placement, error) {
	order := ` ORDER BY created_at,id`
	if i := strings.LastIndex(where, " FOR UPDATE"); i >= 0 {
		where, order = where[:i], ` ORDER BY created_at,id FOR UPDATE`
	}
	rows, err := q.Query(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE `+where+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []Placement{}
	for rows.Next() {
		p, scanErr := scanPlacement(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

var reasonText = map[SuspendReason]string{
	ReasonSpendingLimit: "the spending limit was reached",
	ReasonPastDue:       "a payment is past due",
	ReasonPlatformOwner: "the instance owner stopped it",
}

// suspendMessage says why an organization's platform work is stopped and when
// it starts again.
func suspendMessage(reasons []SuspendReason) string {
	switch len(reasons) {
	case 0:
		return ""
	case 1:
		if reasons[0] == ReasonPlatformOwner {
			return "Loomplane Cloud work is stopped by the instance owner. It starts again when they restore it."
		}
		return "Loomplane Cloud work is stopped because " + reasonText[reasons[0]] + ". It starts again when it is resolved."
	}
	parts := make([]string, len(reasons))
	for i, r := range reasons {
		parts[i] = reasonText[r]
	}
	all := "all are"
	if len(reasons) == 2 {
		all = "both are"
	}
	return "Loomplane Cloud work is stopped because " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1] + ". It starts again when " + all + " resolved."
}

// notifyOrg tells the organization's owners and admins. A notice that can't
// be queued is logged: it never undoes the change it reports.
func (s *Service) notifyOrg(ctx context.Context, orgID, kind, subject, title, body string) {
	admins, err := notifications.Admins(ctx, s.store.db, orgID)
	if err == nil && len(admins) > 0 {
		err = notifications.Enqueue(ctx, s.store.db, notifications.Notice{Kind: kind, OrgID: orgID, SubjectID: subject, Recipients: admins,
			Title: title, Body: body, Path: noticePath})
	}
	if err != nil {
		slog.ErrorContext(ctx, "platform cloud could not queue a notice", "org", orgID, "kind", kind, "error", err)
	}
}
