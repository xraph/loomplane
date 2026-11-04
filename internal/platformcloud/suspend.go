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
// service. Without it Suspend and Restore refuse to run, and disabling a
// tenant records the reason but stops nothing.
func (s *Service) WithInstances(ic InstanceControl) *Service {
	s.instances = ic
	return s
}

func knownReason(r SuspendReason) bool {
	return r == ReasonSpendingLimit || r == ReasonPastDue || r == ReasonPlatformOwner
}

// withTenantRow is WithTenantLock for a reason change: it also passes a
// disabled tenant, since the platform owner's disable is itself a reason.
// A missing tenant is ErrNotEnabled.
func (s *PgStore) withTenantRow(ctx context.Context, orgID string, fn func(driverTx) error) error {
	return s.withTx(ctx, func(tx driverTx) error {
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
// stops new platform work; every call stops whatever still runs and
// remembers it for the restore. Calling it again changes and stops nothing.
func (s *Service) Suspend(ctx context.Context, orgID string, reason SuspendReason) error {
	if orgID == "" || !knownReason(reason) {
		return fmt.Errorf("platformcloud: suspend needs an organization and a known reason, got %q", reason)
	}
	if s.instances == nil {
		return errors.New("platformcloud: no instance control to suspend with")
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
// and one with no room waits for RetryRestores.
func (s *Service) Restore(ctx context.Context, orgID string, reason SuspendReason) error {
	if orgID == "" || !knownReason(reason) {
		return fmt.Errorf("platformcloud: restore needs an organization and a known reason, got %q", reason)
	}
	if s.instances == nil {
		return errors.New("platformcloud: no instance control to restore with")
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

// stopWork stops the organization's running platform services and cancels its
// builds, if the organization is suspended. Each service it stops is marked
// suspended and, if it was running, remembered for the restore. A service
// the user stopped is left stopped. A failure on one service doesn't keep the
// others from stopping, and every step is safe to repeat, so a job that
// crashed or failed can run it again.
func (s *Service) stopWork(ctx context.Context, orgID string) error {
	var errs []error
	err := s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
		t, err := lockTenantTx(ctx, tx, orgID)
		if err != nil || len(t.SuspendReasons) == 0 {
			return err
		}
		live, err := queryPlacements(ctx, tx, `org_id=$1 AND state IN ('reserved','running') FOR UPDATE`, orgID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		builds := false
		for i := range live {
			p := &live[i]
			if p.Kind == kindBuild {
				builds = true
				continue
			}
			if p.State == stateReserved && p.InstanceID == "" {
				// Nothing was ever started: new work, which suspension blocks.
				if _, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,updated_at=now() WHERE id=$1`, p.ID, stateReleased); err != nil {
					return err
				}
				continue
			}
			if p.InstanceID != "" {
				if stopErr := s.instances.Stop(ctx, p.InstanceID); stopErr != nil {
					errs = append(errs, fmt.Errorf("stop placement %s: %w", p.ID, stopErr))
					continue
				}
			}
			if _, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,restore_running=true,waiting_capacity=false,suspended_at=$3,updated_at=now() WHERE id=$1`,
				p.ID, stateSuspended, now); err != nil {
				return err
			}
		}
		if builds {
			if cancelErr := s.instances.CancelBuilds(ctx, orgID); cancelErr != nil {
				errs = append(errs, fmt.Errorf("cancel builds: %w", cancelErr))
			} else if _, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,updated_at=now() WHERE org_id=$1 AND kind='build' AND state IN ('reserved','running')`, orgID, stateReleased); err != nil {
				// A cancelled build's VM is left for the cleanup sweep of released placements.
				return err
			}
		}
		return nil
	})
	return errors.Join(append(errs, err)...)
}

// restoreWork starts what the organization's suspension stopped, if it has no
// reason left. A placement that lost its VM to a reclaim is placed again; if
// no host has room it keeps waiting and RetryRestores comes back to it.
// Starting happens under the tenant lock after a fresh look at the placement,
// so two restores running together start it once.
func (s *Service) restoreWork(ctx context.Context, orgID string) error {
	waiting, err := queryPlacements(ctx, s.store.db, `org_id=$1 AND kind='service' AND state='suspended' AND restore_running`, orgID)
	if err != nil {
		return err
	}
	var errs []error
	for i := range waiting {
		p := waiting[i]
		if p.HostID == nil {
			placed, placeErr := s.placeAgain(ctx, p)
			switch {
			case errors.Is(placeErr, ErrSuspended), errors.Is(placeErr, ErrNotEnabled):
				return errors.Join(errs...)
			case errors.Is(placeErr, ErrNoCapacity):
				s.markWaiting(ctx, p)
				continue
			case placeErr != nil:
				errs = append(errs, placeErr)
				continue
			case placed == nil:
				continue
			}
			p = *placed
		}
		if startErr := s.startPlacement(ctx, orgID, p.ID); startErr != nil {
			if errors.Is(startErr, ErrNotEnabled) {
				return errors.Join(errs...)
			}
			errs = append(errs, fmt.Errorf("start placement %s: %w", p.ID, startErr))
		}
	}
	return errors.Join(errs...)
}

// placeAgain gives a suspended placement that has no host a new one through
// Reserve and carries what a restore needs over to it. The result is nil when
// another restore got there first.
func (s *Service) placeAgain(ctx context.Context, p Placement) (*Placement, error) {
	var region string
	if err := s.store.db.QueryRow(ctx, `SELECT name FROM loomplane_platform_regions WHERE id=$1`, p.RegionID).Scan(&region); err != nil {
		return nil, storeError(err)
	}
	np, err := s.Reserve(ctx, Request{OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID, Resource: p.Resource, Region: region, Kind: p.Kind,
		VCPUs: p.VCPUs, MemoryMiB: p.MemoryMiB, DiskMiB: p.DiskMiB})
	if err != nil {
		return nil, err
	}
	// Keep the new placement suspended and remembered, so a crash before the
	// start leaves something the next restore resumes. Its clock starts now.
	now := s.now().UTC()
	out, err := scanPlacement(s.store.db.QueryRow(ctx, `UPDATE loomplane_platform_placements
SET state=$2,restore_running=true,waiting_capacity=false,instance_id=$3,generation=$4,suspended_at=$5,updated_at=now() WHERE id=$1 AND state=$6
RETURNING `+placementColumns, np.ID, stateSuspended, p.InstanceID, p.Generation, now, stateReserved))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return out, err
}

// markWaiting records that a placement has no room to come back to, and says
// so once.
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
		"Loomplane Cloud has no room to start your work again yet. It starts as soon as there is room.")
}

// startPlacement starts one remembered placement under the tenant lock, if
// the organization is not suspended and the placement still waits for it.
func (s *Service) startPlacement(ctx context.Context, orgID, placementID string) error {
	return s.store.WithTenantLock(ctx, orgID, func(tx driverTx) error {
		t, err := lockTenantTx(ctx, tx, orgID)
		if err != nil || len(t.SuspendReasons) > 0 {
			return err
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
			if err = s.instances.Start(ctx, p.InstanceID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,restore_running=false,waiting_capacity=false,suspended_at=NULL,updated_at=now() WHERE id=$1`, p.ID, stateRunning)
		return err
	})
}

// Reclaim takes the VM off its host for every platform service that has been
// suspended for ReclaimAfter. The placement stays suspended and still counts
// toward the workspace's use; only the host's room is freed. Run it from cron.
func (s *Service) Reclaim(ctx context.Context, now time.Time) error {
	if s.instances == nil {
		return errors.New("platformcloud: no instance control to reclaim with")
	}
	due, err := queryPlacements(ctx, s.store.db, `kind='service' AND state='suspended' AND host_id IS NOT NULL AND suspended_at<=$1
AND org_id IN (SELECT org_id FROM loomplane_platform_tenants WHERE cardinality(suspend_reasons)>0)`, now.Add(-ReclaimAfter).UTC())
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range due {
		if reclaimErr := s.reclaim(ctx, p.OrgID, p.ID, now); reclaimErr != nil {
			errs = append(errs, fmt.Errorf("reclaim placement %s: %w", p.ID, reclaimErr))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) reclaim(ctx context.Context, orgID, placementID string, now time.Time) error {
	return s.store.withTenantRow(ctx, orgID, func(tx driverTx) error {
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
		if p.State != stateSuspended || p.HostID == nil || p.SuspendedAt == nil || now.Sub(*p.SuspendedAt) < ReclaimAfter {
			return nil
		}
		if err = s.instances.RemoveVM(ctx, *p); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_platform_placements SET host_id=NULL,updated_at=now() WHERE id=$1`, p.ID)
		return err
	})
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
	rows, err := s.store.db.Query(ctx, `SELECT DISTINCT p.org_id FROM loomplane_platform_placements p
JOIN loomplane_platform_tenants t ON t.org_id=p.org_id
WHERE p.kind='service' AND p.state='suspended' AND p.restore_running AND t.status='enabled' AND cardinality(t.suspend_reasons)=0
ORDER BY p.org_id`)
	if err != nil {
		return err
	}
	var orgs []string
	for rows.Next() {
		var org string
		if scanErr := rows.Scan(&org); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		orgs = append(orgs, org)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	var errs []error
	for _, org := range orgs {
		if restoreErr := s.restoreWork(ctx, org); restoreErr != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", org, restoreErr))
		}
	}
	return errors.Join(errs...)
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
