package cloudinfra

import (
	"context"
	"crypto/rand"
	"errors"
	mathbig "math/big"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) ReconcileClusterOperation(ctx context.Context, org, id string, heartbeat func([]byte) error) error {
	if s.Clusters == nil {
		return ErrInvalid
	}
	store, ok := s.Clusters.(ClusterLifecycleStore)
	if !ok {
		return ErrInvalid
	}
	o, err := store.LeaseClusterOperation(ctx, org, id, uuid.NewString(), 2*time.Minute)
	if errors.Is(err, ErrPending) {
		return nil
	}
	if err != nil {
		return err
	}
	if heartbeat != nil {
		if checkErr := heartbeat([]byte(`{"cluster_operation":true}`)); checkErr != nil {
			return checkErr
		}
	}
	call, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var reader provider.ClusterReader
	var access provider.AccountAccess
	switch {
	case clusterObservationOnly(o):
		reader, access, err = s.clusterRecoveryAccount(call, o)
	case !s.ManagedClusters.Enabled:
		return s.clusterAttention(ctx, o, "Managed cluster mutations are disabled.")
	default:
		reader, access, err = s.clusterAuthority(call, o.Principal, o.CloudID, o.Input)
	}
	if err != nil {
		return s.clusterAttention(ctx, o, "Current approval or account authority is unavailable.")
	}
	var observation provider.ClusterObservation
	if o.Kind == "create" {
		if len(o.Steps) != 2 {
			return ErrInvalid
		}
		if o.Steps[0].State == "pending" {
			creator, yes := reader.(provider.ClusterCreator)
			if !yes {
				return ErrInvalid
			}
			if o.EstimatedNodeHourlyMicroUSD > s.ManagedClusters.MaxNodeHourlyMicroUSD {
				return s.clusterAttention(ctx, o, "Qualification cost ceiling changed. Review is required.")
			}
			if o.CancelRequested {
				return s.clusterAttention(ctx, o, "Cancellation requires reconciliation.")
			}
			if err = s.clusterPurchaseGate(o.Principal, o.Input); err != nil {
				return s.clusterAttention(ctx, o, "Purchase qualification authority is unavailable.")
			}
			options, e := reader.Options(call, access, o.Input.Spec.Region)
			if e != nil {
				return s.clusterWait(ctx, o, e)
			}
			if options.Digest != o.Input.OptionsDigest || options.CheckedAt.IsZero() || s.now().Sub(options.CheckedAt) >= 5*time.Minute || options.CheckedAt.After(s.now()) {
				return s.clusterAttention(ctx, o, "Provider options changed. Review is required.")
			}
			if err = s.refreshClusterAccount(call, o, reader, access); err != nil {
				return s.clusterWait(ctx, o, err)
			}
			if checkErr := s.recheckClusterManagement(call, o); checkErr != nil {
				return s.clusterAttention(ctx, o, checkErr.Error())
			}
			// Revalidate authority after all live reads, including the management probe.
			if _, _, err = s.clusterAuthority(call, o.Principal, o.CloudID, o.Input); err != nil {
				return s.clusterAttention(ctx, o, "Current approval or account authority is unavailable.")
			}
			if checkErr := store.AttemptClusterCreate(ctx, o); checkErr != nil {
				return checkErr
			}
			spec := o.Input.Spec
			receipt, e := creator.CreateCluster(call, access, spec)
			if e != nil {
				return s.clusterWait(ctx, o, e)
			}
			if receipt.Ref.AccountID != o.Input.AccountID || receipt.Ref.Region != o.Input.Spec.Region || receipt.Ref.ID == "" || receipt.OperationID != o.ID {
				return s.clusterAttention(ctx, o, "Provider receipt identity did not match the reviewed request.")
			}
			o.Receipts = []provider.ClusterMutation{receipt}
			o.Steps[0].RemoteResourceID = receipt.Ref.ID
			o.Steps[0].RemoteOperationID = receipt.OperationID
			o.Steps[0].State = "observed"
			if checkErr := s.saveCluster(ctx, o); checkErr != nil {
				return checkErr
			}
		}
		receipt := provider.ClusterMutation{OperationID: o.ID, Kind: "create", RequestDigest: provider.ClusterRequestDigest(o.Input.Spec), Ref: provider.ClusterRef{AccountID: o.Input.AccountID, Region: o.Input.Spec.Region}}
		if len(o.Receipts) > 0 {
			receipt = o.Receipts[0]
		}
		observer, yes := reader.(provider.ClusterObserver)
		if !yes {
			return ErrInvalid
		}
		observation, err = observer.ObserveClusterMutation(call, access, receipt)
	} else {
		observation, err = reader.GetCluster(call, access, o.Input.Ref)
	}
	if err != nil {
		return s.clusterWait(ctx, o, err)
	}
	if observation.Ref.AccountID != o.Input.AccountID || observation.Ref.ID == "" || observation.Ref.Incarnation == "" {
		return s.clusterAttention(ctx, o, "Observed cluster identity is incomplete.")
	}
	if o.Kind != "create" && observation.Ref != o.Input.Ref {
		return s.clusterAttention(ctx, o, "Observed cluster identity changed.")
	}
	if o.Kind == "create" && len(o.Receipts) == 0 {
		o.Receipts = []provider.ClusterMutation{{OperationID: o.ID, Kind: "create", Ref: observation.Ref, RequestDigest: provider.ClusterRequestDigest(o.Input.Spec)}}
		o.Steps[0].RemoteOperationID = o.ID
	}
	r, err := s.clusterObservedResource(ctx, o, observation)
	if err != nil {
		return err
	}
	o.ResourceID = r.ID
	o.Steps[0].State = "completed"
	o.Steps[0].RemoteResourceID = observation.Ref.ID
	now := s.now().UTC()
	o.Steps[0].CompletedAt = &now
	switch {
	case o.CancelRequested:
		o.Status = "cancelled"
		o.Error = "Provider resource retained after cancellation. Cleanup requires a separate review."
	case observation.State == "error" || observation.State == "deleting":
		o.Status = "needs_attention"
		o.Error = "Provider cluster requires operator recovery."
	case observation.State != "running":
		o.Status = "waiting_provider"
		o.Attempt++
		o.NextAttemptAt = now.Add(time.Minute)
	case o.Input.Attachment != nil:
		o.Status = "needs_attention"
		o.Error = "Cluster identified. Runtime enrollment must be resumed before publishing a target."
	default:
		o.Status = "succeeded"
	}
	expected := o.Revision
	o.Revision++
	o.UpdatedAt = now
	return store.CheckpointClusterResource(ctx, o, r, expected)
}
func (s *Service) clusterObservedResource(ctx context.Context, o *ClusterOperation, observation provider.ClusterObservation) (*ManagedResource, error) {
	id := o.ResourceID
	if id == "" {
		id = o.Input.ResourceID
	}
	if id == "" {
		id = "cluster-" + o.ID
	}
	var r *ManagedResource
	existing, err := s.Clusters.GetManagedResource(ctx, o.OrgID, id)
	switch {
	case err == nil:
		r = existing
		if o.ResourceID == "" && r.Revision != o.Input.ResourceRevision {
			return nil, ErrConflict
		}
		if r.ExternalID != observation.Ref.ID || r.Incarnation != observation.Ref.Incarnation || r.AccountID != o.Input.AccountID {
			return nil, ErrConflict
		}
		r.Revision++
	case errors.Is(err, ErrNotFound):
		r = &ManagedResource{ID: id, OrgID: o.OrgID, CloudID: o.CloudID, Provider: o.Input.Provider, AccountID: o.Input.AccountID, ExternalID: observation.Ref.ID, Incarnation: observation.Ref.Incarnation, Kind: "cluster", Ownership: "external", Revision: 1, SchemaVersion: 1, CreatedAt: s.now().UTC()}
	default:
		return nil, err
	}
	r.CloudID = o.CloudID
	r.ConnectionID = o.Input.ConnectionID
	r.Status = observation.State
	r.Observation = &observation
	r.ObservedAt = observation.ObservedAt
	r.UpdatedAt = s.now().UTC()
	if o.Kind == "create" {
		r.Ownership = "managed"
		r.CreationReceipt = &provider.ClusterMutation{OperationID: o.ID, Kind: "create", Ref: observation.Ref, RequestDigest: provider.ClusterRequestDigest(o.Input.Spec)}
	}
	return r, nil
}
func (s *Service) saveCluster(ctx context.Context, o *ClusterOperation) error {
	old := o.Revision
	o.Revision++
	o.UpdatedAt = s.now().UTC()
	return s.Clusters.SaveClusterCheckpoint(ctx, o, old)
}
func (s *Service) clusterAttention(ctx context.Context, o *ClusterOperation, message string) error {
	o.Status = "needs_attention"
	o.Error = message
	return s.saveCluster(ctx, o)
}
func (s *Service) clusterWait(ctx context.Context, o *ClusterOperation, cause error) error {
	if errors.Is(cause, provider.ErrInfrastructureIdentity) || errors.Is(cause, provider.ErrInfrastructureDrift) {
		return s.clusterAttention(ctx, o, "Provider identity evidence conflicts. Operator recovery is required.")
	}
	o.Status = "waiting_provider"
	o.Error = "Provider outcome is pending reconciliation."
	o.Attempt++
	delay := time.Minute
	if jitter, err := rand.Int(rand.Reader, mathbig.NewInt(30)); err == nil {
		delay += time.Duration(jitter.Int64()) * time.Second
	}
	var api *provider.InfrastructureAPIError
	if errors.As(cause, &api) && api.RetryAfter > delay {
		delay = api.RetryAfter
	}
	o.NextAttemptAt = s.now().UTC().Add(delay)
	return s.saveCluster(ctx, o)
}
