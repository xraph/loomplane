package cloudinfra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"
)

type clusterGrowthCooldownError struct{ until time.Time }

func (e *clusterGrowthCooldownError) Error() string {
	return "cluster scale-up cooldown until " + e.until.Format(time.RFC3339Nano)
}
func (e *clusterGrowthCooldownError) Unwrap() error { return ErrPending }

func poolGrowthCooldown(o *ClusterOperation, r *ManagedResource) (int64, error) {
	pool := o.Input.Maintenance.PoolID
	old := r.ScalingPolicies[pool]
	sec := old.CooldownSeconds
	if sec == 0 {
		sec = 600
	}
	if o.Kind == "node-pool-update" {
		if old.ID == "" || o.CapacityTarget.Servers == 0 {
			return 0, nil
		}
	} else {
		next := o.Input.Scaling
		if next == nil || next.PoolID != pool {
			return 0, ErrInvalid
		}
		if next.Mode != "native" {
			return 0, nil
		}
		if old.Mode == "native" && !old.Revoked && next.MaxNodes <= old.MaxNodes && next.MinNodes <= old.MinNodes {
			return 0, nil
		}
		sec = next.CooldownSeconds
	}
	if sec < 1 || sec > 86400 {
		return 0, ErrInvalid
	}
	return sec, nil
}

// The worker records growth with its first attempt. An uncertain provider
// response keeps the cooldown. Observation and receipt recovery never extend it.
func recordClusterGrowthTx(ctx context.Context, tx driver.Tx, o *ClusterOperation) error {
	if o.Kind != "node-pool-update" && o.Kind != "node-pool-scaling" {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT body FROM loomplane_managed_resources WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, o.OrgID, o.CloudID, o.Input.ResourceID).Scan(&raw); err != nil {
		return err
	}
	var r ManagedResource
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	sec, err := poolGrowthCooldown(o, &r)
	if err != nil || sec == 0 {
		return err
	}
	var until *time.Time
	if err := tx.QueryRow(ctx, `SELECT loomplane_claim_pool_growth($1,$2,$3,$4,$5,$6)`, o.OrgID, o.CloudID, r.ID, o.Input.Maintenance.PoolID, o.ID, sec).Scan(&until); err != nil {
		return err
	}
	if until != nil {
		return &clusterGrowthCooldownError{until: *until}
	}
	return nil
}
