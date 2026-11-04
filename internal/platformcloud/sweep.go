package platformcloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

// The sweeps below put right what a failed step left behind: a VM a move
// left on its old host, a replacement whose deploy never finished, a
// placement whose host lost its VM, a hostname whose
// service is gone, a tenant whose organization is gone, and host evidence
// that is getting old. Each one is safe to run again and again, and one
// failure never stops the rest. The cron jobs run them.

// sweepGrace is how long a row must have stayed as it is before a sweep
// touches it, so a sweep never races the deploy or move that is still
// writing it.
const sweepGrace = 10 * time.Minute

// sweepBatch bounds how many rows one sweep takes on.
const sweepBatch = 200

// Redeployer puts an instance on its placement's host again, through the
// normal deploy path. The InstanceControl the service is given may also be
// one; RepairMissingVMs then repairs what it finds instead of only logging.
type Redeployer interface {
	Redeploy(ctx context.Context, instanceID string) error
}

// Sync is the minute job: it brings projections up to date, checks hosts
// whose evidence is getting old, gives up on replacements that never ran,
// removes what moves left behind, retries hostnames a failed unpublish kept
// and cleans up after organizations that are gone.
func (s *Service) Sync(ctx context.Context) error {
	return errors.Join(
		s.SyncProjections(ctx),
		s.RecheckHosts(ctx),
		s.SweepStalledReplacements(ctx),
		s.SweepLeftovers(ctx),
		s.SweepHostnames(ctx),
		s.CleanupGoneTenants(ctx),
	)
}

// RecheckHosts checks again every host that isn't retired and whose last
// check is older than half of EvidenceMaxAge, so a healthy host never ages
// out of taking placements. A host that fails its check goes pending with
// the reason, as CheckHost records it.
func (s *Service) RecheckHosts(ctx context.Context) error {
	hosts, err := s.store.ListHosts(ctx, "")
	if err != nil {
		return err
	}
	due := s.now().Add(-EvidenceMaxAge / 2)
	var errs []error
	for _, h := range hosts {
		if h.Status == hostRetired || h.Evidence.CheckedAt.After(due) {
			continue
		}
		if _, checkErr := s.CheckHost(ctx, "", h.ID); checkErr != nil {
			slog.WarnContext(ctx, "platform cloud host re-check failed", "host", h.ID, "error", checkErr)
			errs = append(errs, fmt.Errorf("check host %s: %w", h.ID, checkErr))
		}
	}
	return errors.Join(errs...)
}

// replaceGrace is how long a replacement may wait to run before the sweep
// gives up on it: longer than any deploy or move is let run.
const replaceGrace = time.Hour

// SweepStalledReplacements releases every replacement that has waited to
// run, unchanged, for replaceGrace: a deploy or move that failed without
// saying so, or whose process died. The placement it would have replaced
// runs on, and billed, as before. The released row keeps its host and
// instance, so SweepLeftovers removes what its deploy left on its host.
func (s *Service) SweepStalledReplacements(ctx context.Context) error {
	_, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$1,updated_at=now()
WHERE state='reserved' AND replaces<>'' AND updated_at<=$2`, stateReleased, s.now().Add(-replaceGrace).UTC())
	return storeError(err)
}

// SweepLeftovers removes the VMs that released placements may have left on
// their hosts: a move's old VM, a replacement's that never ran, a cancelled
// build's. A leftover whose service's current placement is still waiting to
// run (reserved) is kept: its VM is likely the one still serving, and it
// goes once that placement runs. A leftover that is the very VM a live
// placement of its service runs, or is being deployed as, same host and
// instance, is only forgotten. One on a retired host is forgotten too: that
// host is out of service and its credential is gone.
func (s *Service) SweepLeftovers(ctx context.Context) error {
	leftovers, err := queryPlacements(ctx, s.store.db, `state='released' AND host_id IS NOT NULL AND instance_id<>'' AND updated_at<=$1`,
		s.now().Add(-sweepGrace).UTC())
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range leftovers {
		if leftErr := s.sweepLeftover(ctx, l); leftErr != nil {
			slog.WarnContext(ctx, "platform cloud could not remove a leftover VM", "org", l.OrgID, "placement", l.ID, "error", leftErr)
			errs = append(errs, fmt.Errorf("leftover %s: %w", l.ID, leftErr))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) sweepLeftover(ctx context.Context, l Placement) error {
	live, err := livePlacements(ctx, s.store.db, Request{OrgID: l.OrgID, EnvironmentID: l.EnvironmentID, Resource: l.Resource})
	if err != nil {
		return err
	}
	if live.current != nil && live.current.State == stateReserved {
		return nil
	}
	for _, p := range []*Placement{live.current, live.pending} {
		if p != nil && p.HostID != nil && *p.HostID == *l.HostID && p.InstanceID == l.InstanceID {
			return s.Removed(ctx, l.OrgID, l.ID)
		}
	}
	h, err := s.store.GetHost(ctx, *l.HostID)
	if err != nil {
		return err
	}
	if h.Status != hostRetired {
		if s.instances == nil {
			return errors.New("no instance control")
		}
		if err = callInstances(ctx, func(c context.Context) error { return s.instances.RemoveVM(c, l) }); err != nil {
			return err
		}
	}
	return s.Removed(ctx, l.OrgID, l.ID)
}

// inspectOn reads what runs under scope on the host. Tests replace it.
func (s *Service) inspectOn(ctx context.Context, hostID string, scope fc.Scope) (fc.Record, error) {
	if s.inspect != nil {
		return s.inspect(ctx, hostID, scope)
	}
	c, err := s.client(ctx, hostID)
	if err != nil {
		return fc.Record{}, err
	}
	defer c.Close()
	return c.Inspect(ctx, scope)
}

// RepairMissingVMs looks on its host for the VM of every service placement
// that should have one, unchanged for a while: a running one, and a reserved
// one a move made, whose apply failed and left it without one. What it finds
// missing is logged by placement and, when the instance control can
// redeploy, put back through the normal deploy path. A host it can't reach
// is skipped: it says nothing about the VM.
func (s *Service) RepairMissingVMs(ctx context.Context) error {
	// A reserved placement counts only when a move left an older VM of the
	// service behind: one with none is a deploy that failed, which its
	// deployment reports and which is not for the platform to retry. A
	// replacement still waiting to run doesn't count, and neither does the
	// placement it replaces: a deploy or move is under way, and a move stops
	// the old VM before it starts the new one.
	live, err := queryPlacements(ctx, s.store.db, `kind='service' AND host_id IS NOT NULL AND instance_id<>'' AND updated_at<=$1 AND replaces=''
AND NOT EXISTS(SELECT 1 FROM loomplane_platform_placements r WHERE r.replaces=loomplane_platform_placements.id AND r.state<>'released')
AND (state='running' OR (state='reserved' AND EXISTS(SELECT 1 FROM loomplane_platform_placements l
WHERE l.org_id=loomplane_platform_placements.org_id AND l.environment_id=loomplane_platform_placements.environment_id
AND l.resource=loomplane_platform_placements.resource AND l.state='released' AND l.host_id IS NOT NULL AND l.instance_id<>'')))`,
		s.now().Add(-sweepGrace).UTC())
	if err != nil {
		return err
	}
	redeploy, _ := s.instances.(Redeployer)
	var errs []error
	for _, p := range live {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		r, inspectErr := s.inspectOn(c, *p.HostID, p.Scope())
		cancel()
		missing := errors.Is(inspectErr, fc.ErrMissing) || (inspectErr == nil && (r.State == "removed" || r.State == "stopped" || (r.Current == nil && r.Pending == nil)))
		if inspectErr != nil && !missing {
			slog.DebugContext(ctx, "platform cloud could not inspect a placement's VM", "placement", p.ID, "error", inspectErr)
			continue
		}
		if !missing {
			continue
		}
		slog.WarnContext(ctx, "platform cloud placement has no VM on its host", "org", p.OrgID, "placement", p.ID, "state", p.State)
		if redeploy == nil {
			continue
		}
		if repairErr := callStart(withVMGone(ctx, p.ID), func(c context.Context) error { return redeploy.Redeploy(c, p.InstanceID) }); repairErr != nil {
			slog.ErrorContext(ctx, "platform cloud could not redeploy a placement that lost its VM", "org", p.OrgID, "placement", p.ID, "error", repairErr)
			errs = append(errs, fmt.Errorf("redeploy placement %s: %w", p.ID, repairErr))
		}
	}
	return errors.Join(errs...)
}

// SweepHostnames deletes the generated hostnames, and their records, of
// services that have no live placement any more: what a failed unpublish
// kept. A failure is left for the next run.
func (s *Service) SweepHostnames(ctx context.Context) error {
	rows, err := s.store.db.Query(ctx, `SELECT h.org_id,h.environment_id,h.resource FROM loomplane_platform_hostnames h
WHERE h.updated_at<=$1 AND NOT EXISTS(SELECT 1 FROM loomplane_platform_placements p
WHERE p.org_id=h.org_id AND p.environment_id=h.environment_id AND p.resource=h.resource AND p.kind='service' AND p.state<>'released')
ORDER BY h.org_id,h.environment_id,h.resource LIMIT `+fmt.Sprint(sweepBatch), s.now().Add(-sweepGrace).UTC())
	if err != nil {
		return err
	}
	type key struct{ org, env, resource string }
	var orphans []key
	for rows.Next() {
		var k key
		if scanErr := rows.Scan(&k.org, &k.env, &k.resource); scanErr != nil {
			rows.Close()
			return scanErr
		}
		orphans = append(orphans, k)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var errs []error
	for _, k := range orphans {
		if _, unErr := s.unpublish(ctx, k.org, k.env, k.resource); unErr != nil {
			slog.WarnContext(ctx, "platform cloud could not remove a hostname", "org", k.org, "environment", k.env, "error", unErr)
			errs = append(errs, fmt.Errorf("hostname of %s/%s: %w", k.org, k.env, unErr))
		}
	}
	return errors.Join(errs...)
}

// OrgExists reports whether an organization still exists on the instance.
type OrgExists func(ctx context.Context, orgID string) (bool, error)

// WithOrgExists sets how the cleanup checks, right before it cleans up a
// tenant, that the tenant's organization is really gone, and returns the
// service. Without it no tenant is cleaned up.
func (s *Service) WithOrgExists(exists OrgExists) *Service {
	s.orgExists = exists
	return s
}

// errCleanupBlocked is a deleted organization's projection that something
// still references, such as one of its environments still assigned to it.
var errCleanupBlocked = errors.New("platformcloud: the projection is still referenced")

// cleanupBackoff is how long a tenant whose cleanup a reference blocked
// waits before the next try.
const cleanupBackoff = time.Hour

// CleanupGoneTenants takes everything of a tenant whose organization no
// longer exists off the platform: its placements are released and their VMs
// removed, its hostnames deleted, then its projection and the tenant itself.
// Its images stay in the platform registry, which has no way to delete a
// repository prefix, and that is logged for the platform owner once the
// tenant is gone. Each organization is checked again on its own right before
// its cleanup, and an organization list that comes back empty is not
// believed: an instance always has one. A tenant whose projection is still
// referenced (an environment of the deleted organization still assigned to
// it) is marked blocked, with the reason, and left alone for an hour; any
// other failure is tried again next run.
func (s *Service) CleanupGoneTenants(ctx context.Context) error {
	if s.orgs == nil || s.orgExists == nil {
		return nil
	}
	orgs, err := s.orgs(ctx)
	if err != nil {
		return err
	}
	if len(orgs) == 0 {
		return errors.New("platformcloud: the organization list is empty; no tenant is cleaned up")
	}
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		return err
	}
	blocked, err := s.cleanupBlocks(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(orgs))
	for _, o := range orgs {
		known[o] = struct{}{}
	}
	var errs []error
	for _, t := range tenants {
		if _, ok := known[t.OrgID]; ok {
			continue
		}
		if b, ok := blocked[t.OrgID]; ok && b.Until.After(s.now()) {
			continue
		}
		exists, existsErr := s.orgExists(ctx, t.OrgID)
		if existsErr != nil {
			errs = append(errs, existsErr)
			continue
		}
		if exists {
			continue
		}
		cleanErr := s.cleanupTenant(ctx, t.OrgID)
		if errors.Is(cleanErr, errCleanupBlocked) {
			until := s.now().Add(cleanupBackoff).UTC()
			if _, markErr := s.store.db.Exec(ctx, `UPDATE loomplane_platform_tenants SET cleanup_blocked_until=$2,cleanup_error=$3 WHERE org_id=$1`,
				t.OrgID, until, "the organization's Loomplane Cloud is still assigned to one of its environments"); markErr != nil {
				errs = append(errs, markErr)
			}
		}
		if cleanErr != nil {
			slog.WarnContext(ctx, "platform cloud could not clean up after a deleted organization", "org", t.OrgID, "error", cleanErr)
			errs = append(errs, fmt.Errorf("clean up %s: %w", t.OrgID, cleanErr))
		}
	}
	return errors.Join(errs...)
}

// CleanupBlock says a deleted organization's tenant can't be cleaned up yet,
// until when the cleanup waits, and why.
type CleanupBlock struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// cleanupBlocks is every tenant whose cleanup is marked blocked.
func (s *Service) cleanupBlocks(ctx context.Context) (map[string]CleanupBlock, error) {
	rows, err := s.store.db.Query(ctx, `SELECT org_id,cleanup_blocked_until,cleanup_error FROM loomplane_platform_tenants WHERE cleanup_blocked_until IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := map[string]CleanupBlock{}
	for rows.Next() {
		var org string
		var b CleanupBlock
		if err = rows.Scan(&org, &b.Until, &b.Reason); err != nil {
			return nil, err
		}
		out[org] = b
	}
	return out, rows.Err()
}

func (s *Service) cleanupTenant(ctx context.Context, orgID string) error {
	if _, err := s.store.db.Exec(ctx, `UPDATE loomplane_platform_placements SET state=$2,updated_at=now() WHERE org_id=$1 AND state<>$2`, orgID, stateReleased); err != nil {
		return storeError(err)
	}
	left, err := queryPlacements(ctx, s.store.db, `org_id=$1 AND state='released' AND host_id IS NOT NULL AND instance_id<>''`, orgID)
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range left {
		errs = append(errs, s.sweepLeftover(ctx, l))
	}
	if err = errors.Join(errs...); err != nil {
		return err
	}
	rows, err := s.store.db.Query(ctx, `SELECT environment_id,resource FROM loomplane_platform_hostnames WHERE org_id=$1`, orgID)
	if err != nil {
		return err
	}
	var names [][2]string
	for rows.Next() {
		var env, resource string
		if scanErr := rows.Scan(&env, &resource); scanErr != nil {
			rows.Close()
			return scanErr
		}
		names = append(names, [2]string{env, resource})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, n := range names {
		if _, unErr := s.unpublish(ctx, orgID, n[0], n[1]); unErr != nil {
			return unErr
		}
	}
	err = s.store.WithPlatformWrite(ctx, func(tx driverTx) error {
		if _, execErr := tx.Exec(ctx, `DELETE FROM loomplane_cloud_profiles WHERE org_id=$1 AND platform_owned`, orgID); execErr != nil {
			var pg *pgconn.PgError
			if errors.As(execErr, &pg) && pg.Code == "23503" {
				return fmt.Errorf("%w: %s", errCleanupBlocked, pg.Message)
			}
			return fmt.Errorf("delete the projection: %w", storeError(execErr))
		}
		if _, execErr := tx.Exec(ctx, `DELETE FROM loomplane_platform_egress_counters WHERE org_id=$1`, orgID); execErr != nil {
			return storeError(execErr)
		}
		_, execErr := tx.Exec(ctx, `DELETE FROM loomplane_platform_tenants WHERE org_id=$1`, orgID)
		return storeError(execErr)
	})
	if err != nil {
		return err
	}
	if cfg, cfgErr := s.settings.PlatformCloud(ctx); cfgErr == nil && cfg.Registry.Endpoint != "" && strings.Trim(cfg.Registry.Repository, "/") != "" {
		slog.WarnContext(ctx, "platform cloud: a deleted organization's images stay in the platform registry; delete its repository by hand",
			"org", orgID, "repository", strings.TrimRight(cfg.Registry.Repository, "/")+"/ws-"+orgID)
	}
	return nil
}
