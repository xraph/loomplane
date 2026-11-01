package cloudinfra

import (
	"net/http"
	"time"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/client"
)

func publicClusterCapacity(v *infra.ClusterCapacitySnapshot) *client.ClusterCapacity {
	if v == nil {
		return nil
	}
	out := &client.ClusterCapacity{ResourceID: v.ResourceID, ObservationRevision: v.ObservationRevision, CheckedAt: v.CheckedAt, Complete: v.Complete, Fresh: v.Fresh(time.Now()), WorkloadsComplete: v.WorkloadsComplete, Nodes: []client.ClusterNodeCapacity{}, Conditions: v.Conditions}
	for _, n := range v.Nodes {
		out.Nodes = append(out.Nodes, client.ClusterNodeCapacity{PoolID: n.PoolID, Zone: n.Zone, Architecture: n.Architecture, Ready: n.Ready, Unschedulable: n.Unschedulable, Allocatable: client.ClusterResourceMetrics(n.Allocatable), Requests: client.ClusterResourceMetrics(n.Requests), Taints: n.Taints})
	}
	return out
}
func (e *Extension) clusterCapacity(c forge.Context) error { return e.clusterCapacityCall(c, false) }
func (e *Extension) refreshClusterCapacity(c forge.Context) error {
	return e.clusterCapacityCall(c, true)
}
func (e *Extension) clusterCapacityCall(c forge.Context, refresh bool) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	var v *infra.ClusterCapacitySnapshot
	if refresh {
		v, err = e.svc.ObserveClusterCapacity(c.Request().Context(), p, c.Param("cloudId"), c.Param("clusterId"))
	} else {
		v, err = e.svc.GetClusterCapacity(c.Request().Context(), p, c.Param("cloudId"), c.Param("clusterId"))
	}
	return clusterResult(c, http.StatusOK, publicClusterCapacity(v), err)
}
