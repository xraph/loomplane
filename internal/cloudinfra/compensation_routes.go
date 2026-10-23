package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

// PrepareCompensationRoutes drains accepted ingress work before the source
// binding is restored. An unknown DNS result keeps both runtimes retained.
func (s *Service) PrepareCompensationRoutes(ctx context.Context, p security.Principal, in ReleaseRouteRequest) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	for _, source := range in.Routes {
		if source.OrgID != p.OrgID || source.ProjectID != in.ProjectID || source.Input.EnvironmentID != in.EnvironmentID || source.Input.Service != in.Service {
			return ErrInvalid
		}
		prepared, readErr := st.RouteOperation(ctx, p.OrgID, releasePrepareID(in.Key, source.ID))
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		if prepared.Source == nil || !reflect.DeepEqual(*prepared.Source, source) {
			return ErrConflict
		}
		ops, readErr := st.RouteOperations(ctx, p.OrgID, in.ProjectID, source.ID)
		if readErr != nil {
			return readErr
		}
		for _, op := range ops {
			if op.HandoffID == prepared.ID && op.Kind == "cutover" && op.DNSAttempted && op.Receipt == nil {
				if err = s.reconcileReleaseOperation(ctx, p, op); err != nil {
					return fmt.Errorf("recover the accepted cutover receipt before restoring its source: %w", err)
				}
			}
		}
		if lockErr := s.withRouteIngressLocks(ctx, prepared, func() error {
			if scopeErr := s.domainScope(ctx, p, in.ProjectID, true); scopeErr != nil {
				return scopeErr
			}
			if retainedErr := st.ValidateRetainedRoute(ctx, source); retainedErr != nil {
				return retainedErr
			}
			for _, route := range []Route{source, prepared.Route} {
				state, stateErr := st.BeginServerIngress(ctx, p.OrgID, route.Binding.ServerID)
				if stateErr != nil {
					return stateErr
				}
				client, err := s.routeCleanupClient(ctx, route)
				if err != nil {
					return err
				}
				err = client.ArmEffectFence(ctx, server.EffectFence{Operation: p.OrgID + "/ingress/" + route.Binding.ServerID, Generation: state.Generation})
				client.Close()
				if err != nil {
					return err
				}
			}
			current, err := st.RouteOperations(ctx, p.OrgID, in.ProjectID, source.ID)
			if err != nil {
				return err
			}
			var latest *RouteOperation
			for _, operation := range current {
				if operation.HandoffID == prepared.ID && operation.Kind == "cutover" {
					latest = operation
					break
				}
			}
			live, readErr := st.Route(ctx, p.OrgID, in.ProjectID, source.ID)
			if readErr != nil {
				return readErr
			}
			claim, available := st.(interface {
				ClaimCompensationHandoff(context.Context, *RouteOperation, *Route, *RouteOperation) error
			})
			if !available {
				return errors.New("reviewed handoff recovery is unavailable")
			}
			if err := claim.ClaimCompensationHandoff(ctx, prepared, live, latest); err != nil {
				return err
			}
			var cancelled []*RouteOperation
			for _, op := range current {
				if op.ID != prepared.ID && op.HandoffID != prepared.ID {
					continue
				}
				if op.Kind == "rollback" || op.Status == "succeeded" || op.Status == "cancelled" {
					continue
				}
				if op.DNSAttempted && op.Receipt == nil {
					return fmt.Errorf("%w: recover the accepted DNS receipt before compensation", ErrConflict)
				}
				cancelled = append(cancelled, op)
			}
			if len(cancelled) == 0 {
				return nil
			}
			cancellation, ok := st.(interface {
				CancelCompensationOperations(context.Context, []*RouteOperation) error
			})
			if !ok {
				return errors.New("atomic compensation cancellation is unavailable")
			}
			return cancellation.CancelCompensationOperations(ctx, cancelled)
		}); lockErr != nil {
			return lockErr
		}
	}
	return nil
}

func (s *Service) withRouteIngressLocks(ctx context.Context, op *RouteOperation, run func() error) error {
	locks := []string{routeLockKey(op.Route)}
	if op.Source != nil {
		locks = append(locks, routeLockKey(*op.Source))
	}
	slices.Sort(locks)
	locks = slices.Compact(locks)
	var lock func(int) error
	lock = func(i int) error {
		if i == len(locks) {
			return run()
		}
		return s.Store.WithOperationLock(ctx, locks[i], func() error { return lock(i + 1) })
	}
	return lock(0)
}

// ReconcileCompensationRoutes uses the original handoff and latest cutover
// receipt, then verifies the restored source before releasing the candidate.
func (s *Service) ReconcileCompensationRoutes(ctx context.Context, p security.Principal, in ReleaseRouteRequest) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	for _, source := range in.Routes {
		// A Kubernetes route was never handed off, as in ReconcileReleaseRoutes.
		if source.Binding.BackendName() == RouteBackendKubernetes {
			continue
		}
		if source.OrgID != p.OrgID || source.ProjectID != in.ProjectID || source.Input.EnvironmentID != in.EnvironmentID || source.Input.Service != in.Service {
			return ErrInvalid
		}
		preparedID := releasePrepareID(in.Key, source.ID)
		progress := cloudprofiles.SyncRoute{RouteID: source.ID, Hostname: source.Input.Hostname, PrepareID: preparedID}
		prepared, readErr := st.RouteOperation(ctx, p.OrgID, preparedID)
		if errors.Is(readErr, ErrNotFound) {
			current, e := st.Route(ctx, p.OrgID, in.ProjectID, source.ID)
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(*current, source) {
				return ErrConflict
			}
			if checkErr := s.verifyReleasedDestination(ctx, &RouteOperation{Route: *current}); checkErr != nil {
				return checkErr
			}
			continue
		}
		if readErr != nil {
			return readErr
		}
		if prepared.Source == nil || !reflect.DeepEqual(*prepared.Source, source) || prepared.Source.Binding.InstanceID != in.InstanceID {
			return ErrConflict
		}
		ops, e := st.RouteOperations(ctx, p.OrgID, in.ProjectID, source.ID)
		if e != nil {
			return e
		}
		var cutover, rollback, release *RouteOperation
		for _, op := range ops {
			if op.HandoffID != preparedID {
				continue
			}
			switch op.Kind {
			case "cutover":
				if cutover == nil {
					cutover = op
				}
			case "rollback":
				if rollback == nil {
					rollback = op
				}
			case "release":
				if op.Status != "cancelled" && reflect.DeepEqual(op.Route.Binding, source.Binding) {
					release = op
				}
			}
		}
		current, e := st.Route(ctx, p.OrgID, in.ProjectID, source.ID)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(current.Binding, source.Binding) && !reflect.DeepEqual(current.Binding, prepared.Route.Binding) {
			return ErrConflict
		}
		// The ingress backend can change before the route row is published, even
		// when both instances share an address. A recorded cutover effect needs
		// its own rollback; DNS address equality cannot prove backend identity.
		needsRollback := !reflect.DeepEqual(current.Binding, source.Binding) || cutover != nil && (cutover.DNSAttempted || cutover.DesiredConfig != nil || cutover.AppliedConfig != nil)
		if rollback == nil && needsRollback {
			if cutover == nil {
				return ErrConflict
			}
			if _, e = ReviewedRouteDrain(source, in.DrainSeconds); e != nil {
				return fmt.Errorf("preserve both runtimes: the accepted source has no trustworthy external TTL bound: %w", e)
			}
			rollback, e = s.RollbackRoute(ctx, p, in.ProjectID, cutover.ID)
			if e != nil {
				return e
			}
		}
		if cutover != nil {
			progress.CutoverID = cutover.ID
		}
		if rollback != nil {
			if checkErr := s.reconcileReleaseProgress(ctx, p, rollback, &progress, in.Checkpoint); checkErr != nil {
				return checkErr
			}
		}
		if release == nil {
			if in.CleanupOnly {
				return ErrConflict
			}
			drain := max(60, in.DrainSeconds)
			release, e = s.ReleaseRouteHandoff(ctx, p, in.ProjectID, preparedID, drain)
			if e != nil {
				return e
			}
		}
		if checkErr := s.reconcileReleaseProgress(ctx, p, release, &progress, in.Checkpoint); checkErr != nil {
			return checkErr
		}
		if checkErr := s.verifyReleasedDestination(ctx, release); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

func (s *PgStore) CancelCompensationOperations(ctx context.Context, operations []*RouteOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, original := range operations {
		op := *original
		op.Status = "cancelled"
		op.Revision++
		body, _, checkpoint, encodeErr := routeBytes(&op)
		if encodeErr != nil {
			return encodeErr
		}
		var id string
		if err = tx.QueryRow(ctx, `UPDATE loomplane_route_operations SET status='cancelled',revision=$3,body=$4,checkpoint=$5 WHERE org_id=$1 AND id=$2 AND revision=$6 AND status NOT IN ('succeeded','cancelled','preview') RETURNING id`, op.Route.OrgID, op.ID, op.Revision, body, checkpoint, original.Revision).Scan(&id); err != nil {
			return ErrConflict
		}
	}
	return tx.Commit()
}
