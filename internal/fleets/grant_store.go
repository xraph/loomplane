package fleets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/pkg/fleet"
)

var _ GrantStore = (*PgStore)(nil)

func (s *PgStore) CreateGrant(ctx context.Context, scope FleetScope, g *fleet.Grant) (err error) {
	raw, err := json.Marshal(g.Input)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	for _, cloud := range g.Input.CloudIDs {
		var allowed bool
		if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_stage_clouds WHERE org_id=$1 AND fleet_id=$2 AND stage_id=$3 AND cloud_id=$4)`, scope.OrgID, scope.FleetID, g.Input.StageID, cloud).Scan(&allowed); queryErr != nil {
			return queryErr
		}
		if !allowed {
			return ErrNotFound
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_grants(id,org_id,project_id,fleet_id,stage_id,credential_id,revision,status,snapshot,expires_at,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, g.ID, scope.OrgID, scope.ProjectID, scope.FleetID, g.Input.StageID, g.Input.CredentialID, g.Revision, g.Status, string(raw), g.Input.ExpiresAt, g.CreatedBy, g.CreatedAt); err != nil {
		return storeError(err)
	}
	return tx.Commit()
}

const grantColumns = `id,fleet_id,revision,status,snapshot,created_by,created_at`

func scanGrant(row scanner) (*fleet.Grant, error) {
	var g fleet.Grant
	var raw string
	if err := row.Scan(&g.ID, &g.FleetID, &g.Revision, &g.Status, &raw, &g.CreatedBy, &g.CreatedAt); err != nil {
		return nil, storeError(err)
	}
	if err := json.Unmarshal([]byte(raw), &g.Input); err != nil {
		return nil, err
	}
	g.CreatedAt = g.CreatedAt.UTC()
	return &g, nil
}
func (s *PgStore) GetGrant(ctx context.Context, scope FleetScope, id string) (*fleet.Grant, error) {
	return scanGrant(s.db.QueryRow(ctx, `SELECT `+grantColumns+` FROM loomplane_fleet_grants WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4`, scope.OrgID, scope.ProjectID, scope.FleetID, id))
}
func (s *PgStore) RevokeGrant(ctx context.Context, scope FleetScope, id string, revision int64) error {
	if revision < 1 {
		return ErrInvalid
	}
	result, err := s.db.Exec(ctx, `UPDATE loomplane_fleet_grants SET status='revoked',revision=revision+1 WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4 AND revision=$5 AND status='active'`, scope.OrgID, scope.ProjectID, scope.FleetID, id, revision)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}
func authorityRequestHash(a *OperationAuthority) (string, error) {
	raw, err := json.Marshal(struct {
		Request    OperationRequest
		Credential any
	}{a.Request, a.Principal})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (s *PgStore) ReserveAuthority(ctx context.Context, a *OperationAuthority) (out *OperationAuthority, err error) {
	hash, err := authorityRequestHash(a)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	scope := a.Scope
	g, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` FROM loomplane_fleet_grants WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4 FOR UPDATE`, scope.OrgID, scope.ProjectID, scope.FleetID, a.Request.GrantID))
	if err != nil {
		return nil, err
	}
	if g.Revision != a.GrantRevision {
		return nil, ErrAuthorityRevoked
	}
	if acceptErr := acceptGrant(g, a.Principal, a.Request); acceptErr != nil {
		return nil, acceptErr
	}
	var storedHash, storedRaw string
	err = tx.QueryRow(ctx, `SELECT request_hash,snapshot FROM loomplane_fleet_authorities WHERE org_id=$1 AND fleet_id=$2 AND credential_id=$3 AND kind=$4 AND idempotency_key=$5`, scope.OrgID, scope.FleetID, a.Principal.KeyID, a.Request.Kind, a.Request.IdempotencyKey).Scan(&storedHash, &storedRaw)
	if err == nil {
		if hash != storedHash {
			return nil, ErrConflict
		}
		var replay OperationAuthority
		if decodeErr := json.Unmarshal([]byte(storedRaw), &replay); decodeErr != nil {
			return nil, decodeErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return &replay, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = nil
	var count int
	var cpu, memory, storage, cost int64
	if countErr := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(cpu_millis),0),coalesce(sum(memory_mib),0),coalesce(sum(storage_gib),0),coalesce(sum(hourly_cost_micros),0) FROM loomplane_fleet_authorities WHERE org_id=$1 AND fleet_id=$2 AND grant_id=$3 AND released_at IS NULL`, scope.OrgID, scope.FleetID, g.ID).Scan(&count, &cpu, &memory, &storage, &cost); countErr != nil {
		return nil, countErr
	}
	l := g.Input.Limits
	d := a.Request.Demand
	if count >= l.MaxInstances || d.CPUMillis > l.CPUMillis-cpu || d.MemoryMiB > l.MemoryMiB-memory || d.StorageGiB > l.StorageGiB-storage || d.HourlyCostMicros > l.HourlyCostMicros-cost {
		return nil, ErrCapacity
	}
	var stageRevision int64
	if policyErr := tx.QueryRow(ctx, `SELECT s.policy_revision FROM loomplane_fleet_stages s JOIN loomplane_fleets f ON f.org_id=s.org_id AND f.id=s.fleet_id WHERE s.org_id=$1 AND s.project_id=$2 AND s.fleet_id=$3 AND s.id=$4 AND f.status='active' AND EXISTS(SELECT 1 FROM loomplane_fleet_stage_clouds c WHERE c.org_id=s.org_id AND c.stage_id=s.id AND c.cloud_id=$5) FOR SHARE OF s,f`, scope.OrgID, scope.ProjectID, scope.FleetID, a.Request.StageID, a.Request.CloudID).Scan(&stageRevision); policyErr != nil {
		return nil, storeError(policyErr)
	}
	a.StageRevision = stageRevision
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_authorities(id,org_id,project_id,fleet_id,grant_id,grant_revision,credential_id,kind,external_ref,idempotency_key,request_hash,snapshot,cpu_millis,memory_mib,storage_gib,hourly_cost_micros,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, a.ID, scope.OrgID, scope.ProjectID, scope.FleetID, g.ID, g.Revision, a.Principal.KeyID, a.Request.Kind, a.Request.ExternalRef, a.Request.IdempotencyKey, hash, string(raw), d.CPUMillis, d.MemoryMiB, d.StorageGiB, d.HourlyCostMicros, a.ExpiresAt); err != nil {
		return nil, storeError(err)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return a, nil
}
func (s *PgStore) GetAuthority(ctx context.Context, scope FleetScope, id string) (*OperationAuthority, error) {
	var raw string
	if err := s.db.QueryRow(ctx, `SELECT snapshot FROM loomplane_fleet_authorities WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4`, scope.OrgID, scope.ProjectID, scope.FleetID, id).Scan(&raw); err != nil {
		return nil, storeError(err)
	}
	var a OperationAuthority
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, err
	}
	return &a, nil
}
func (s *PgStore) CheckAuthorityPolicy(ctx context.Context, a OperationAuthority) error {
	var current bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_stages s JOIN loomplane_fleets f ON f.org_id=s.org_id AND f.id=s.fleet_id WHERE s.org_id=$1 AND s.project_id=$2 AND s.fleet_id=$3 AND s.id=$4 AND s.policy_revision=$5 AND f.status='active' AND EXISTS(SELECT 1 FROM loomplane_fleet_stage_clouds c WHERE c.org_id=s.org_id AND c.stage_id=s.id AND c.cloud_id=$6)) AND EXISTS(SELECT 1 FROM loomplane_fleet_authorities WHERE org_id=$1 AND id=$7 AND released_at IS NULL)`, a.Scope.OrgID, a.Scope.ProjectID, a.Scope.FleetID, a.Request.StageID, a.StageRevision, a.Request.CloudID, a.ID).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		return ErrAuthorityRevoked
	}
	return nil
}
