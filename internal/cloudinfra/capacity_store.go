package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/provider"
)

// CapacityPolicyStore keeps policy persistence separate from runtime admission.
type CapacityPolicyStore interface {
	GetCapacityPolicy(context.Context, string, string) (*CapacityPolicy, error)
	SaveCapacityPolicy(context.Context, *CapacityPolicy, int64) error
}

var _ CapacityPolicyStore = (*PgStore)(nil)

// A Cloud with no policy retains legacy behavior and revision zero.
func (s *PgStore) GetCapacityPolicy(ctx context.Context, org, cloud string) (*CapacityPolicy, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT p.body FROM loomplane_cloud_profiles c LEFT JOIN loomplane_cloud_capacity_policies p ON p.org_id=c.org_id AND p.cloud_id=c.id WHERE c.org_id=$1 AND c.id=$2`, org, cloud).Scan(&raw)
	if err != nil {
		return nil, storeError(err)
	}
	if len(raw) == 0 {
		return &CapacityPolicy{OrgID: org, CloudID: cloud}, nil
	}
	var out CapacityPolicy
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *PgStore) SaveCapacityPolicy(ctx context.Context, policy *CapacityPolicy, previous int64) error {
	if previous < 0 || policy.Revision != previous+1 {
		return ErrInvalid
	}
	if err := validateCapacityPolicyShape(*policy); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, policy.OrgID, policy.CloudID); checkErr != nil {
		return checkErr
	}
	var revision int64
	err = tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_capacity_policies WHERE org_id=$1 AND cloud_id=$2`, policy.OrgID, policy.CloudID).Scan(&revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if revision != previous {
		return ErrConflict
	}
	if checkErr := capacityPolicyScopes(ctx, tx, policy); checkErr != nil {
		return checkErr
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	authority, err := json.Marshal(policy.authority)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_capacity_policies(org_id,cloud_id,revision,enabled,require_expansion_approval,servers,cpu_millis,memory_bytes,storage_bytes,body,authority) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(org_id,cloud_id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,require_expansion_approval=excluded.require_expansion_approval,servers=excluded.servers,cpu_millis=excluded.cpu_millis,memory_bytes=excluded.memory_bytes,storage_bytes=excluded.storage_bytes,body=excluded.body,authority=excluded.authority,updated_at=now()`, policy.OrgID, policy.CloudID, policy.Revision, policy.Enabled, policy.RequireExpansionApproval, policy.Limits.Servers, policy.Limits.CPUMillis, policy.Limits.MemoryBytes, policy.Limits.StorageBytes, raw, authority)
	if err != nil {
		return storeError(err)
	}
	return tx.Commit()
}

func capacityPolicyScopes(ctx context.Context, tx driver.Tx, policy *CapacityPolicy) error {
	for _, pool := range policy.Pools {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM loomplane_server_pools WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR SHARE`, policy.OrgID, policy.CloudID, pool.PoolID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalid
			}
			return err
		}
		if pool.ConnectionID != "" {
			if err := tx.QueryRow(ctx, `SELECT id FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR SHARE`, policy.OrgID, pool.ConnectionID).Scan(&id); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrInvalid
				}
				return err
			}
		}
	}
	frontendProviders := make(map[string]bool)
	if len(policy.FrontendLimits) > 0 {
		for _, candidate := range providers.Catalog() {
			for _, run := range candidate.Runs {
				if run.Workload == provider.WebApp || run.Workload == provider.StaticSite {
					frontendProviders[candidate.Name] = true
				}
			}
		}
	}
	for _, limit := range policy.FrontendLimits {
		var targetProvider string
		if err := tx.QueryRow(ctx, `SELECT provider FROM loomplane_cloud_targets WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND runtime='managed' FOR SHARE`, policy.OrgID, policy.CloudID, limit.TargetID).Scan(&targetProvider); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalid
			}
			return err
		}
		if !frontendProviders[targetProvider] {
			return ErrInvalid
		}
	}
	return nil
}
