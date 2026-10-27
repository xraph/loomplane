package deployments

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/cloudrun"
	"github.com/xraph/loomplane/internal/clouds/ecs"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// Measure reads a service's requests and latency over w from the cloud
// that runs it, finding it the way Observe does. The executors whose
// clouds report these implement it; the usage API reads with them.

// Measure reads the Cloud Run service from Cloud Monitoring.
func (c *CloudRun) Measure(ctx context.Context, _ *connections.Connection, credential []byte, b *planning.Binding, w metrics.Window) (*metrics.Measurement, error) {
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("the binding names no instance: %w", err)
	}
	key, err := gcp.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	s, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.ServiceMetrics(ctx, b.Region, cloudrun.ServiceName(iid), w)
}

// Measure reads the ECS service from CloudWatch, through its target group.
func (e *ECS) Measure(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding, w metrics.Window) (*metrics.Measurement, error) {
	iid, err := id.Parse(b.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("the binding names no instance: %w", err)
	}
	key, err := aws.ParseKey(credential)
	if err != nil {
		return nil, err
	}
	return e.AWS.Session(key, conn.Details["region"], conn.Account).ServiceMetrics(ctx, ecsCluster, ecs.ServiceName(iid), w)
}

// Measure reads the container app from Azure Monitor.
func (a *Azure) Measure(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding, w metrics.Window) (*metrics.Measurement, error) {
	s, err := azureSession(ctx, a.Client, conn, credential)
	if err != nil {
		return nil, err
	}
	return s.AppMetrics(ctx, b.ExternalID, w)
}

// Measure reads the service from Render's metrics API. A datastore serves
// no HTTP, so it has nothing to report.
func (r *Render) Measure(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding, w metrics.Window) (*metrics.Measurement, error) {
	if _, _, ok := datastoreID(b.ExternalID); ok {
		return nil, metrics.Unavailablef("%s is a datastore, which serves no HTTP requests.", b.Name)
	}
	key := render.Key{Value: strings.TrimSpace(string(credential))}
	if conn != nil {
		key.OwnerID = conn.Details["owner_id"]
	}
	return r.Client.ServiceMetrics(ctx, key, b.ExternalID, w)
}
