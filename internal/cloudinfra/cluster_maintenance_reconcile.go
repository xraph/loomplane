package cloudinfra

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) reconcileClusterMaintenance(ctx context.Context, o *ClusterOperation, reader provider.ClusterReader, access provider.AccountAccess) error {
	if len(o.Steps) != 2 || o.Input.Maintenance == nil {
		return ErrInvalid
	}
	ctx = s.withClusterMaintenanceFence(ctx, o)
	m := o.Input.Maintenance
	if o.Steps[0].State == "pending" {
		if o.CancelRequested {
			return s.clusterAttention(ctx, o, "Maintenance was cancelled before the provider effect.")
		}
		if m.Window != nil {
			if !s.now().Before(m.Window.EndUTC) {
				return s.clusterAttention(ctx, o, "The maintenance window has ended. Review again.")
			}
			if s.now().Before(m.Window.StartUTC) {
				o.Status = "waiting_provider"
				o.NextAttemptAt = m.Window.StartUTC
				return s.saveCluster(ctx, o)
			}
		}
		capacity, cost, _, prepareErr := s.prepareMaintenance(ctx, o.Principal, o.CloudID, &o.Input, reader, access)
		if prepareErr != nil {
			return s.clusterAttention(ctx, o, "Maintenance preflight changed. Review again.")
		}
		if capacity != o.CapacityTarget || cost != o.EstimatedNodeHourlyMicroUSD {
			return s.clusterAttention(ctx, o, "Maintenance capacity changed. Review again.")
		}
		if capacity.Servers > 0 {
			if checkErr := s.refreshClusterAccount(ctx, o, reader, access); checkErr != nil {
				return s.clusterWait(ctx, o, checkErr)
			}
		}
		store, ok := s.Clusters.(ClusterLifecycleStore)
		if !ok {
			return ErrInvalid
		}
		if checkErr := store.AttemptClusterCreate(ctx, o); checkErr != nil {
			return checkErr
		}
		access.BeforeEffect = func(effectCtx context.Context) error {
			effectCapacity, effectCost, _, effectErr := s.prepareMaintenance(effectCtx, o.Principal, o.CloudID, &o.Input, reader, access)
			if effectErr != nil {
				return effectErr
			}
			if effectCapacity != o.CapacityTarget || effectCost != o.EstimatedNodeHourlyMicroUSD {
				return ErrConflict
			}
			if _, _, checkErr := s.clusterAuthority(effectCtx, o.Principal, o.CloudID, o.Input); checkErr != nil {
				return checkErr
			}
			if _, checkErr := s.maintenanceResource(effectCtx, o.Principal, o.CloudID, o.Input); checkErr != nil {
				return checkErr
			}
			if m.Window != nil && (s.now().Before(m.Window.StartUTC) || !s.now().Before(m.Window.EndUTC)) {
				return ErrConflict
			}
			if o.LeaseExpiresAt == nil || !o.LeaseExpiresAt.After(s.now().Add(5*time.Second)) {
				return ErrConflict
			}
			if validator, ok := s.Clusters.(interface {
				ValidateClusterMaintenanceEffect(context.Context, *ClusterOperation) error
			}); ok {
				if checkErr := validator.ValidateClusterMaintenanceEffect(effectCtx, o); checkErr != nil {
					return checkErr
				}
			} else {
				return ErrInvalid
			}
			return s.saveCluster(effectCtx, o)
		}
		receipt, err := executeClusterMaintenance(ctx, reader, access, o.Input)
		s.recordCluster(ctx, "cloud.cluster.maintenance_effect_attempted", o)
		if err != nil {
			return s.clusterWait(ctx, o, err)
		}
		if receipt.Ref != o.Input.Ref || receipt.Kind != o.Kind || o.Kind == "node-pool-create" && (receipt.PoolID == "" || receipt.OperationID != o.ID) || (o.Kind == "node-pool-update" || o.Kind == "node-pool-delete") && receipt.PoolID != m.PoolID {
			return s.clusterAttention(ctx, o, "Maintenance receipt identity changed.")
		}
		receipt.OperationID = o.ID
		receipt.RequestDigest = o.Digest
		o.Receipts = []provider.ClusterMutation{receipt}
		o.Steps[0].State = "observed"
		o.Steps[0].RemoteResourceID = receipt.Ref.ID
		o.Steps[0].RemoteOperationID = o.ID
		if checkErr := s.saveCluster(ctx, o); checkErr != nil {
			return checkErr
		}
	}
	observation, err := reader.GetCluster(ctx, access, o.Input.Ref)
	if err != nil {
		return s.clusterWait(ctx, o, err)
	}
	if observation.Ref != o.Input.Ref {
		return s.clusterAttention(ctx, o, "Observed maintenance identity changed.")
	}
	if !maintenanceConverged(o, observation) {
		return s.clusterWait(ctx, o, ErrPending)
	}
	// A missing response never authorizes another mutation. Convergence proves
	// desired state only; it does not fabricate a provider acceptance receipt.
	r, err := s.clusterObservedResource(ctx, o, observation)
	if err != nil {
		return err
	}
	if o.Kind != "node-pool-delete" {
		capability := string(maintenanceCapability(o.Kind))
		if !slices.Contains(r.LifecycleAuthority, capability) {
			r.LifecycleAuthority = append(r.LifecycleAuthority, capability)
		}
	}
	o.ResourceID = r.ID
	o.Status = "succeeded"
	if o.CancelRequested {
		o.Status = "cancelled"
	}
	now := s.now().UTC()
	o.Steps[0].State = "completed"
	o.Steps[0].CompletedAt = &now
	o.Steps[1].State = "completed"
	o.Steps[1].CompletedAt = &now
	old := o.Revision
	o.Revision++
	o.UpdatedAt = now
	store, ok := s.Clusters.(ClusterLifecycleStore)
	if !ok {
		return ErrInvalid
	}
	return store.CheckpointClusterResource(ctx, o, r, old)
}
func executeClusterMaintenance(ctx context.Context, reader provider.ClusterReader, access provider.AccountAccess, in ClusterInput) (provider.ClusterMutation, error) {
	m := in.Maintenance
	if m == nil {
		return provider.ClusterMutation{}, ErrInvalid
	}
	switch in.Kind {
	case "cluster-upgrade", "cluster-configure":
		adapter, ok := reader.(provider.ClusterMaintainer)
		if !ok {
			return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
		}
		if in.Kind == "cluster-upgrade" {
			return adapter.UpgradeCluster(ctx, access, in.Ref, m.Version)
		}
		return adapter.UpdateCluster(ctx, access, in.Ref, *m.Update)
	default:
		adapter, ok := reader.(provider.NodePoolManager)
		if !ok {
			return provider.ClusterMutation{}, provider.ErrInfrastructureUnsupported
		}
		switch in.Kind {
		case "node-pool-create":
			return adapter.CreateNodePool(ctx, access, in.Ref, *m.Pool)
		case "node-pool-update":
			return adapter.UpdateNodePool(ctx, access, in.Ref, m.PoolID, *m.Pool)
		case "node-pool-delete":
			return adapter.DeleteNodePool(ctx, access, in.Ref, m.PoolID)
		}
	}
	return provider.ClusterMutation{}, ErrInvalid
}
func maintenanceConverged(o *ClusterOperation, observation provider.ClusterObservation) bool {
	if observation.State != "running" {
		return false
	}
	m := o.Input.Maintenance
	switch o.Kind {
	case "cluster-upgrade":
		return observation.Version == m.Version
	case "cluster-configure":
		return observation.Maintenance == m.Update.Maintenance
	case "node-pool-delete":
		for _, pool := range observation.Pools {
			if pool.ID == m.PoolID {
				return false
			}
		}
		return true
	default:
		matches := 0
		for _, pool := range observation.Pools {
			if o.Kind == "node-pool-create" {
				if pool.OperationID != o.ID || pool.RequestDigest != m.Pool.RequestDigest {
					continue
				}
			} else if pool.ID != m.PoolID {
				continue
			}
			if pool.ID == "" || pool.Name != m.Pool.Name || pool.Count != m.Pool.Count || pool.Size != m.Pool.Size || pool.Autoscale != m.Pool.Autoscale || !maps.Equal(pool.Labels, m.Pool.Labels) || !slices.Equal(pool.Taints, m.Pool.Taints) {
				return false
			}
			matches++
		}
		return matches == 1
	}
}
