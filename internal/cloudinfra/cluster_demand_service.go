package cloudinfra

import "context"

type ClusterDemandPlacement struct {
	ResourceID, TargetID, DemandID, Mode, PoolID string
	ReviewDecrease                               bool
}
type ClusterDemandResolver interface {
	ReconcileClusterDemand(context.Context, string, string, string) (CapacityDecision, error)
}

func (s *Service) ReconcileClusterDemand(ctx context.Context, org, cloud, demandID string) (CapacityDecision, error) {
	if err := s.clusterScalingEnabled(); err != nil {
		return CapacityDecision{}, err
	}
	a, ok := s.CapacityDemandAuthority.(ClusterDemandResolver)
	if !ok {
		return CapacityDecision{}, ErrInvalid
	}
	decision, err := a.ReconcileClusterDemand(ctx, org, cloud, demandID)
	if err != nil || decision.ClusterPlacement == nil || decision.ClusterPlacement.PoolID == "" {
		return decision, err
	}
	if store, ok := s.Clusters.(interface {
		CurrentClusterPoolAdvice(context.Context, string, string, string, string) (bool, error)
	}); ok {
		decision.ClusterPlacement.ReviewDecrease, err = store.CurrentClusterPoolAdvice(ctx, org, cloud, decision.ClusterPlacement.ResourceID, decision.ClusterPlacement.PoolID)
	}
	return decision, err
}

type ClusterDemandWaitError struct {
	Cohort    string
	Admission *CapacityAdmissionError
}

func (w *ClusterDemandWaitError) Error() string { return w.Admission.Error() }
func (w *ClusterDemandWaitError) Unwrap() error { return w.Admission }
