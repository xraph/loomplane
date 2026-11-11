package fleets

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/currentauthority"
	"github.com/xraph/loomplane/pkg/fleet"
)

// LockedInstanceAuthority retains database facts read under the operation's
// owned transaction. ValidateAt uses the identity decision's exact instant.
type LockedInstanceAuthority struct {
	authority       OperationAuthority
	grant           fleet.Grant
	authorityExpiry time.Time
	grantExpiry     time.Time
}

func (p *LockedInstanceAuthority) ValidateAt(at time.Time) error {
	if p == nil || at.IsZero() || !p.authority.ExpiresAt.After(at) || !p.authorityExpiry.After(at) || !p.grantExpiry.After(at) {
		return ErrAuthorityRevoked
	}
	return acceptGrantAt(&p.grant, p.authority.Principal, p.authority.Request, at)
}

// LockInstanceAuthority follows the source and identity fences and Cloud
// organization lock. It finishes all waiting reads before EvaluateFleet.
// Snapshot rendering is deterministic and resolves no credentials here.
func LockInstanceAuthority(ctx context.Context, attempt *currentauthority.Attempt, expected OperationAuthority, snapshot InstanceSnapshot) (*LockedInstanceAuthority, error) {
	if expected.Request.Kind != "create" || attempt == nil || expected.Principal.Fleet == nil || expected.Scope.OrgID == "" || expected.Scope.ProjectID == "" || expected.Scope.FleetID == "" || expected.Principal.OrgID != expected.Scope.OrgID || expected.Principal.Fleet.ProjectID != expected.Scope.ProjectID || expected.Principal.Fleet.FleetID != expected.Scope.FleetID {
		return nil, ErrInvalid
	}
	table := func(name string) string { return pgx.Identifier{attempt.Schema(), name}.Sanitize() }
	tx := attempt.Tx()
	scope := expected.Scope
	var status, projectMode string
	if err := tx.QueryRow(ctx, `SELECT status FROM `+table("loomplane_fleets")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND project_id OPERATOR(pg_catalog.=) $2 AND id OPERATOR(pg_catalog.=) $3 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.FleetID).Scan(&status); err != nil {
		return nil, storeError(err)
	}
	if err := tx.QueryRow(ctx, `SELECT deployment_mode FROM `+table("loomplane_projects")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND id OPERATOR(pg_catalog.=) $2 FOR SHARE`, scope.OrgID, scope.ProjectID).Scan(&projectMode); err != nil {
		return nil, storeError(err)
	}
	if status != "active" || projectMode != "fleet" {
		return nil, ErrAuthorityRevoked
	}
	var stageRevision int64
	if err := tx.QueryRow(ctx, `SELECT policy_revision FROM `+table("loomplane_fleet_stages")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND project_id OPERATOR(pg_catalog.=) $2 AND fleet_id OPERATOR(pg_catalog.=) $3 AND id OPERATOR(pg_catalog.=) $4 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.FleetID, expected.Request.StageID).Scan(&stageRevision); err != nil {
		return nil, storeError(err)
	}
	if stageRevision != expected.StageRevision {
		return nil, ErrAuthorityRevoked
	}
	var cloud string
	if err := tx.QueryRow(ctx, `SELECT cloud_id FROM `+table("loomplane_fleet_stage_clouds")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND fleet_id OPERATOR(pg_catalog.=) $2 AND stage_id OPERATOR(pg_catalog.=) $3 AND cloud_id OPERATOR(pg_catalog.=) $4 FOR SHARE`, scope.OrgID, scope.FleetID, expected.Request.StageID, expected.Request.CloudID).Scan(&cloud); err != nil {
		return nil, storeError(err)
	}
	grant, err := scanGrant(tx.QueryRow(ctx, `SELECT `+grantColumns+` FROM `+table("loomplane_fleet_grants")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND project_id OPERATOR(pg_catalog.=) $2 AND fleet_id OPERATOR(pg_catalog.=) $3 AND id OPERATOR(pg_catalog.=) $4 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.FleetID, expected.Request.GrantID))
	if err != nil {
		return nil, err
	}
	var raw, hash, credential, kind, externalRef, idempotency, grantID string
	var grantRevision int64
	var released *time.Time
	proof := &LockedInstanceAuthority{grant: *grant}
	var demand OperationDemand
	if err = tx.QueryRow(ctx, `SELECT snapshot,request_hash,credential_id,kind,external_ref,idempotency_key,grant_id,grant_revision,cpu_millis,memory_mib,storage_gib,hourly_cost_micros,expires_at,released_at FROM `+table("loomplane_fleet_authorities")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND project_id OPERATOR(pg_catalog.=) $2 AND fleet_id OPERATOR(pg_catalog.=) $3 AND id OPERATOR(pg_catalog.=) $4 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.FleetID, expected.ID).Scan(&raw, &hash, &credential, &kind, &externalRef, &idempotency, &grantID, &grantRevision, &demand.CPUMillis, &demand.MemoryMiB, &demand.StorageGiB, &demand.HourlyCostMicros, &proof.authorityExpiry, &released); err != nil {
		return nil, storeError(err)
	}
	if authorityDecodeErr := json.Unmarshal([]byte(raw), &proof.authority); authorityDecodeErr != nil {
		return nil, authorityDecodeErr
	}
	// Compare the persisted representation. time.Time's process-local monotonic
	// component is not retained by JSON and is not an authority fact.
	actualAuthority, err := json.Marshal(proof.authority)
	if err != nil {
		return nil, err
	}
	expectedAuthority, err := json.Marshal(expected)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(actualAuthority, expectedAuthority) || released != nil {
		return nil, ErrAuthorityRevoked
	}
	a := proof.authority
	expectedHash, err := authorityRequestHash(&a)
	if err != nil {
		return nil, err
	}
	request := a.Request
	if hash != expectedHash || credential != a.Principal.KeyID || kind != request.Kind || externalRef != request.ExternalRef || idempotency != request.IdempotencyKey || grantID != request.GrantID || grantRevision != a.GrantRevision || grant.Revision != a.GrantRevision || grant.FleetID != scope.FleetID || demand.CPUMillis != request.Demand.CPUMillis || demand.MemoryMiB != request.Demand.MemoryMiB || demand.StorageGiB != request.Demand.StorageGiB || demand.HourlyCostMicros != request.Demand.HourlyCostMicros {
		return nil, ErrConflict
	}
	var grantCredential, grantStage string
	if grantReadErr := tx.QueryRow(ctx, `SELECT credential_id,stage_id,expires_at FROM `+table("loomplane_fleet_grants")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND id OPERATOR(pg_catalog.=) $2`, scope.OrgID, grant.ID).Scan(&grantCredential, &grantStage, &proof.grantExpiry); grantReadErr != nil {
		return nil, grantReadErr
	}
	if grantCredential != grant.Input.CredentialID || grantStage != grant.Input.StageID {
		return nil, ErrConflict
	}
	instance, err := scanInstance(tx.QueryRow(ctx, `SELECT `+instanceColumns+` FROM `+table("loomplane_fleet_instances")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND project_id OPERATOR(pg_catalog.=) $2 AND fleet_id OPERATOR(pg_catalog.=) $3 AND id OPERATOR(pg_catalog.=) $4 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.FleetID, snapshot.InstanceID))
	if err != nil {
		return nil, err
	}
	if instance.Status == "retired" || instance.Status == "deleted" || instance.ExternalRef != request.ExternalRef || instance.StageID != request.StageID || instance.DesiredReleaseID != request.ReleaseID || instance.ProfileID != request.ProfileID || instance.EnvironmentID != snapshot.EnvironmentID || instance.DesiredGeneration != snapshot.Generation || instance.OwnershipEpoch != snapshot.OwnershipEpoch {
		return nil, ErrConflict
	}
	var environment string
	if err = tx.QueryRow(ctx, `SELECT id FROM `+table("loomplane_environments")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND project_id OPERATOR(pg_catalog.=) $2 AND id OPERATOR(pg_catalog.=) $3 FOR SHARE`, scope.OrgID, scope.ProjectID, instance.EnvironmentID).Scan(&environment); err != nil {
		return nil, storeError(err)
	}
	release, err := scanRelease(tx.QueryRow(ctx, `SELECT id,fleet_id,digest,snapshot,created_at FROM `+table("loomplane_fleet_releases")+` WHERE org_id OPERATOR(pg_catalog.=) $1 AND project_id OPERATOR(pg_catalog.=) $2 AND fleet_id OPERATOR(pg_catalog.=) $3 AND id OPERATOR(pg_catalog.=) $4 FOR SHARE`, scope.OrgID, scope.ProjectID, scope.FleetID, request.ReleaseID))
	if err != nil {
		return nil, err
	}
	rendered, err := RenderInstance(*release, *instance, fleet.CreateInstanceInput{StageID: instance.StageID, ReleaseID: release.ID, ProfileID: instance.ProfileID, Parameters: snapshot.Parameters, SecretRefs: snapshot.SecretRefs})
	if err != nil {
		return nil, err
	}
	if rendered.Digest != request.SnapshotDigest || !reflect.DeepEqual(*rendered, snapshot) {
		return nil, ErrConflict
	}
	return proof, nil
}
