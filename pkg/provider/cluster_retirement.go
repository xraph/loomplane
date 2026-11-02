package provider

import (
	"context"
	"time"
)

// AssociatedResource records an exact identity. Association alone grants no ownership.
type AssociatedResource struct {
	ID                      string `json:"id"`
	Kind                    string `json:"kind"`
	Incarnation             string `json:"incarnation,omitempty"`
	Ownership               string `json:"ownership"`
	SurvivesClusterDeletion bool   `json:"survives_cluster_deletion"`
	EstimatedHourlyMicroUSD *int64 `json:"estimated_hourly_micro_usd,omitempty"`
}

type AssociatedResourceInventory struct {
	Ref       ClusterRef           `json:"ref"`
	Resources []AssociatedResource `json:"resources"`
	Complete  bool                 `json:"complete"`
	// ChildrenQualified requires evidence for children destroyed with the parent.
	ChildrenQualified bool      `json:"children_qualified"`
	ObservedAt        time.Time `json:"observed_at"`
}

type ClusterRetirer interface {
	AssociatedResources(context.Context, AccountAccess, ClusterRef) (AssociatedResourceInventory, error)
	DeleteCluster(context.Context, AccountAccess, ClusterRef, string) (ClusterMutation, error)
}
