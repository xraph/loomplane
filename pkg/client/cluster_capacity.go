package client

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterResourceMetrics struct {
	CPUMillis             *int64           `json:"cpu_millis"`
	MemoryBytes           *int64           `json:"memory_bytes"`
	EphemeralStorageBytes *int64           `json:"ephemeral_storage_bytes"`
	Pods                  *int64           `json:"pods"`
	Extended              map[string]int64 `json:"extended"`
}
type ClusterNodeCapacity struct {
	PoolID        string                   `json:"pool_id"`
	Zone          string                   `json:"zone"`
	Architecture  string                   `json:"architecture"`
	Ready         bool                     `json:"ready"`
	Unschedulable bool                     `json:"unschedulable"`
	Allocatable   ClusterResourceMetrics   `json:"allocatable"`
	Requests      ClusterResourceMetrics   `json:"requests"`
	Taints        []provider.NodePoolTaint `json:"taints"`
}
type ClusterCapacity struct {
	ResourceID          string                      `json:"resource_id" forge:"id"`
	ObservationRevision int64                       `json:"observation_revision"`
	CheckedAt           time.Time                   `json:"checked_at"`
	Complete            bool                        `json:"complete"`
	Fresh               bool                        `json:"fresh"`
	WorkloadsComplete   bool                        `json:"workloads_complete"`
	Nodes               []ClusterNodeCapacity       `json:"nodes"`
	Conditions          []provider.ClusterCondition `json:"conditions"`
}

func (c *Client) CloudClusterCapacity(ctx context.Context, cloud, id string) (*ClusterCapacity, error) {
	var out ClusterCapacity
	err := c.do(ctx, http.MethodGet, infrastructurePath(cloud)+"/clusters/"+esc(id)+"/capacity", nil, &out)
	return &out, err
}
func (c *Client) RefreshCloudClusterCapacity(ctx context.Context, cloud, id string) (*ClusterCapacity, error) {
	var out ClusterCapacity
	err := c.do(ctx, http.MethodPost, infrastructurePath(cloud)+"/clusters/"+esc(id)+"/capacity/refresh", nil, &out)
	return &out, err
}
