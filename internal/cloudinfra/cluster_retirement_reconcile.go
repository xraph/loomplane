package cloudinfra

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func (s *Service) retirementEffectGuard(ctx context.Context, o *ClusterOperation) error {
	if checkErr := s.clusterEnabled(); checkErr != nil {
		return checkErr
	}
	if _, _, err := s.clusterAuthority(ctx, o.Principal, o.CloudID, o.Input); err != nil {
		return err
	}
	if _, err := s.retirementOperationResource(ctx, o); err != nil {
		return err
	}
	store, ok := s.Clusters.(ClusterRetirementStore)
	if !ok {
		return ErrInvalid
	}
	return store.ValidateClusterRetirementEffect(ctx, o)
}

func (s *Service) reconcileClusterRetirement(ctx context.Context, o *ClusterOperation, reader provider.ClusterReader, access provider.AccountAccess) error {
	if o.Input.Retirement == nil {
		return ErrInvalid
	}
	if o.Kind == "detach-runtime" {
		return s.reconcileRuntimeDetach(ctx, o, reader, access)
	}
	if len(o.Steps) != 2 {
		return ErrInvalid
	}
	store, ok := s.Clusters.(ClusterLifecycleStore)
	if !ok {
		return ErrInvalid
	}
	if o.Steps[0].State == "pending" {
		review, err := s.prepareClusterRetirement(ctx, o.Principal, o.CloudID, o.Input, reader, access, o.ID)
		if err != nil {
			return s.clusterAttention(ctx, o, "Retirement preflight is unavailable.")
		}
		if len(review.Blockers) > 0 || review.Digest != o.Input.Retirement.ReviewDigest {
			return s.clusterAttention(ctx, o, "Retirement review changed. Review again.")
		}
		if checkErr := s.Adapters.Require(o.Input.Provider, provider.ClusterDeletion); checkErr != nil {
			return s.clusterAttention(ctx, o, "Provider retirement is not qualified.")
		}
		if checkErr := s.retirementEffectGuard(ctx, o); checkErr != nil {
			return s.clusterAttention(ctx, o, "Retirement authority changed.")
		}
		if checkErr := store.AttemptClusterCreate(ctx, o); checkErr != nil {
			return checkErr
		}
		access.BeforeEffect = func(effectCtx context.Context) error {
			if checkErr := s.Adapters.Require(o.Input.Provider, provider.ClusterDeletion); checkErr != nil {
				return checkErr
			}
			review, reviewErr := s.prepareClusterRetirement(effectCtx, o.Principal, o.CloudID, o.Input, reader, access, o.ID)
			if reviewErr != nil {
				return reviewErr
			}
			if len(review.Blockers) > 0 || review.Digest != o.Input.Retirement.ReviewDigest {
				return ErrConflict
			}
			return s.retirementEffectGuard(effectCtx, o)
		}
		retirer, ok := reader.(provider.ClusterRetirer)
		if !ok {
			return ErrInvalid
		}
		receipt, err := retirer.DeleteCluster(ctx, access, o.Input.Ref, o.Input.Ref.Incarnation)
		if err != nil {
			return s.clusterWait(ctx, o, err)
		}
		if receipt.Ref != o.Input.Ref || receipt.Kind != "retire-cluster" {
			return s.clusterAttention(ctx, o, "Retirement receipt identity changed.")
		}
		receipt.OperationID = o.ID
		receipt.RequestDigest = o.Digest
		o.Receipts = []provider.ClusterMutation{receipt}
		o.Steps[0].State = "observed"
		o.Steps[0].RemoteResourceID = receipt.Ref.ID
		if checkErr := s.saveCluster(ctx, o); checkErr != nil {
			return checkErr
		}
	}
	observation, err := reader.GetCluster(ctx, access, o.Input.Ref)
	if err == nil {
		if observation.Ref != o.Input.Ref {
			return s.clusterAttention(ctx, o, "Cluster incarnation changed during retirement.")
		}
		return s.clusterWait(ctx, o, ErrPending)
	}
	if !errors.Is(err, provider.ErrInfrastructureNotFound) {
		return s.clusterWait(ctx, o, err)
	}
	// Absence after an ambiguous response cannot establish an accepted owned delete.
	if len(o.Receipts) != 1 || o.Receipts[0].Ref != o.Input.Ref || o.Receipts[0].Kind != "retire-cluster" {
		return s.clusterAttention(ctx, o, "Cluster is absent, but provider deletion acceptance is unconfirmed.")
	}
	r, err := s.retirementOperationResource(ctx, o)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	r.DeletedAt = &now
	r.Status = "deleted"
	r.Revision++
	r.ObservedRevision++
	r.UpdatedAt = now
	r.RetainedResources = nil
	r.RetainedPriceCheckedAt = o.Input.Retirement.Inventory.ObservedAt
	for _, item := range o.Input.Retirement.Inventory.Resources {
		if item.SurvivesClusterDeletion {
			r.RetainedResources = append(r.RetainedResources, item)
		}
	}
	for i := range o.Steps {
		o.Steps[i].State = "completed"
		o.Steps[i].CompletedAt = &now
	}
	o.Status = "succeeded"
	o.Error = ""
	old := o.Revision
	o.Revision++
	o.UpdatedAt = now
	o.NextAttemptAt = time.Time{}
	return store.CheckpointClusterResource(ctx, o, r, old)
}
