package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/security"
)

// ReleaseRouteRequest connects a reviewed release to its exact route snapshots.
// InstanceID is the newly bound destination, never a pre-binding candidate.
type ReleaseRouteRequest struct {
	Key, ProjectID, EnvironmentID, Service, CloudID, InstanceID string
	Routes                                                      []Route
	DrainSeconds                                                int
	CleanupOnly                                                 bool
	Checkpoint                                                  func(cloudprofiles.SyncRoute) error
}

func releasePrepareID(key, route string) string {
	sum := sha256.Sum256([]byte(key + "/" + route))
	return "routeop_" + hex.EncodeToString(sum[:16])
}

// ReconcileReleaseRoutes resumes each accepted operation without creating a new
// DNS intent after a lost response. Route workers own all DNS and ingress effects.
func (s *Service) ReconcileReleaseRoutes(ctx context.Context, p security.Principal, in ReleaseRouteRequest) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	for _, source := range in.Routes {
		progress := cloudprofiles.SyncRoute{RouteID: source.ID, Hostname: source.Input.Hostname, PrepareID: releasePrepareID(in.Key, source.ID)}
		reconcile := func(op *RouteOperation) error {
			return s.reconcileReleaseProgress(ctx, p, op, &progress, in.Checkpoint)
		}

		drain, drainErr := ReviewedRouteDrain(source, in.DrainSeconds)
		if drainErr != nil {
			return drainErr
		}
		if source.OrgID != p.OrgID || source.ProjectID != in.ProjectID || source.Input.EnvironmentID != in.EnvironmentID || source.Input.Service != in.Service {
			return ErrInvalid
		}
		preparedID := releasePrepareID(in.Key, source.ID)
		prepared, readErr := st.RouteOperation(ctx, p.OrgID, preparedID)
		if errors.Is(readErr, ErrNotFound) {
			if in.CleanupOnly {
				return ErrConflict
			}
			current, getErr := st.Route(ctx, p.OrgID, in.ProjectID, source.ID)
			if getErr != nil {
				return getErr
			}
			if !reflect.DeepEqual(*current, source) {
				return fmt.Errorf("%w: route changed since release review", ErrConflict)
			}
			prepared, err = s.PrepareRouteHandoff(ctx, p, in.ProjectID, source.ID, HandoffInput{Revision: source.Revision, DestinationCloudID: in.CloudID, operationID: preparedID})
		} else {
			err = readErr
		}
		if err != nil {
			return err
		}
		if prepared.Source == nil || !reflect.DeepEqual(*prepared.Source, source) || prepared.Route.Binding.InstanceID != in.InstanceID {
			return ErrConflict
		}
		if in.CleanupOnly && prepared.Status != "succeeded" {
			return ErrConflict
		}
		if reconcileErr := reconcile(prepared); reconcileErr != nil {
			return reconcileErr
		}
		operations, listErr := st.RouteOperations(ctx, p.OrgID, in.ProjectID, source.ID)
		if listErr != nil {
			return listErr
		}
		var cutover, release *RouteOperation
		for _, op := range operations {
			if op.HandoffID == preparedID {
				switch op.Kind {
				case "cutover":
					cutover = op
				case "release":
					release = op
				case "rollback":
					return fmt.Errorf("%w: release route was rolled back", ErrConflict)
				}
			}
		}
		if cutover == nil {
			if in.CleanupOnly {
				return ErrConflict
			}
			cutover, err = s.CutoverRoute(ctx, p, in.ProjectID, preparedID)
			if err != nil {
				return err
			}
		}
		if in.CleanupOnly && cutover.Status != "succeeded" {
			return ErrConflict
		}
		if reconcileErr := reconcile(cutover); reconcileErr != nil {
			return reconcileErr
		}
		if release == nil {
			if in.CleanupOnly {
				return ErrConflict
			}
			release, err = s.ReleaseRouteHandoff(ctx, p, in.ProjectID, preparedID, drain)
			if err != nil {
				return err
			}
		}
		if reconcileErr := reconcile(release); reconcileErr != nil {
			return reconcileErr
		}
		// A completed checkpoint is not proof that DNS has stayed put since restart.
		if verifyErr := s.verifyReleasedDestination(ctx, release); verifyErr != nil {
			return verifyErr
		}
	}
	return nil
}

func (s *Service) reconcileReleaseOperation(ctx context.Context, p security.Principal, op *RouteOperation) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	if op.Status == "failed" || op.Status == "waiting" {
		retried, retryErr := s.RetryRoute(ctx, p, op.Route.ProjectID, op.ID)
		if retryErr != nil {
			return retryErr
		}
		*op = *retried
	}
	reconcileErr := s.ReconcileRoute(ctx, p.OrgID, op.ID, nil)
	current, err := st.RouteOperation(ctx, p.OrgID, op.ID)
	if err != nil {
		return errors.Join(reconcileErr, err)
	}
	*op = *current
	if reconcileErr != nil {
		return reconcileErr
	}
	if current.Status != "succeeded" {
		return fmt.Errorf("%w: route %s is %s", ErrPending, op.Route.Input.Hostname, current.Status)
	}
	*op = *current
	return nil
}

func (s *Service) verifyReleasedDestination(ctx context.Context, op *RouteOperation) error {
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	return s.Store.WithOperationLock(ctx, "ingress/"+op.Route.OrgID+"/"+op.Route.Binding.ServerID, func() error {
		current, readErr := st.Route(ctx, op.Route.OrgID, op.Route.ProjectID, op.Route.ID)
		if readErr != nil {
			return readErr
		}
		if current.Revision != op.Route.Revision || !reflect.DeepEqual(current.Binding, op.Route.Binding) || !reflect.DeepEqual(current.Receipt, op.Route.Receipt) {
			return ErrConflict
		}
		if checkErr := st.ValidateRouteCleanup(ctx, *current); checkErr != nil {
			return checkErr
		}
		state, readErr := st.BeginServerIngress(ctx, current.OrgID, current.Binding.ServerID)
		if readErr != nil {
			return readErr
		}
		client, readErr := s.routeCleanupClient(ctx, *current)
		if readErr != nil {
			return readErr
		}
		defer client.Close()
		fence := server.EffectFence{Operation: current.OrgID + "/ingress/" + current.Binding.ServerID, Generation: state.Generation}
		ctx = server.WithEffectFence(ctx, fence)
		if fenceErr := client.ArmEffectFence(ctx, fence); fenceErr != nil {
			return fenceErr
		}
		v := s.verifyRouteDestination(ctx, client, *current, state, false)
		if !v.DNS || !v.TLS || !v.Backend {
			return ErrPending
		}
		return nil
	})
}

// ValidateRuntimeRemoval refuses both public and retained route references.
func (s *Service) ValidateRuntimeRemoval(ctx context.Context, org, project, instance string) error {
	st, ok := s.Store.(*PgStore)
	if !ok {
		return ErrInvalid
	}
	var referenced bool
	err := st.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 AND status<>'removed' AND body->'binding'->>'instance_id'=$3) OR EXISTS(SELECT 1 FROM loomplane_route_handoffs h JOIN loomplane_route_operations o ON o.id=h.id WHERE h.org_id=$1 AND o.project_id=$2 AND NOT h.released AND (o.body->'source'->'binding'->>'instance_id'=$3 OR o.body->'route'->'binding'->>'instance_id'=$3))`, org, project, instance).Scan(&referenced)
	if err != nil {
		return err
	}
	if referenced {
		return fmt.Errorf("remove the domain route and finish its retained handoff before deleting runtime %s", instance)
	}
	return nil
}

// ReviewedRouteDrain bounds reviewed input without inferring an external record's TTL.
// ReleaseRouteHandoff independently extends the deadline using trusted provider receipts.
func ReviewedRouteDrain(source Route, minimum int) (int, error) {
	bound := source.Input.ExternalTTLSeconds
	if bound != 0 && (bound < 60 || bound > 86400) {
		return 0, ErrInvalid
	}
	if (source.Receipt == nil || source.Receipt.TTL <= 0) && bound == 0 {
		return 0, fmt.Errorf("%w: review the external DNS TTL using the route TTL update, then plan again before replacing its runtime", ErrConflict)
	}
	drain := max(60, minimum, bound)
	if drain > 86400 {
		return 0, fmt.Errorf("%w: reviewed drain exceeds the supported 86400 second bound", ErrInvalid)
	}
	return drain, nil
}

func (s *Service) reconcileReleaseProgress(ctx context.Context, p security.Principal, op *RouteOperation, progress *cloudprofiles.SyncRoute, checkpoint func(cloudprofiles.SyncRoute) error) error {
	err := s.reconcileReleaseOperation(ctx, p, op)
	progress.Status, progress.Message = op.Status, op.Message
	switch op.Kind {
	case "cutover":
		progress.CutoverID = op.ID
	case "release":
		progress.ReleaseID, progress.NotBefore = op.ID, op.NotBefore
	}
	if op.Kind != "release" {
		progress.RecordType, progress.Value, progress.TTL = op.DesiredRecord.Type, op.DesiredRecord.Value, op.DesiredRecord.TTL
	}
	if checkpoint != nil {
		err = errors.Join(err, checkpoint(*progress))
	}
	return err
}
