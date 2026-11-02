package fleets

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/pkg/fleet"
)

var _ ResourceBindingStore = (*PgStore)(nil)

func (s *PgStore) ListInstanceResourceBindings(ctx context.Context, scope FleetScope, instance string) ([]ResourceBinding, error) {
	rows, err := s.db.Query(ctx, `SELECT b.body FROM loomplane_fleet_resource_bindings b JOIN loomplane_fleet_instances i ON i.org_id=b.org_id AND i.id=b.instance_id AND i.environment_id=b.environment_id AND i.desired_generation=b.generation AND i.ownership_epoch=b.ownership_epoch WHERE b.org_id=$1 AND b.project_id=$2 AND b.fleet_id=$3 AND b.instance_id=$4 ORDER BY b.name`, scope.OrgID, scope.ProjectID, scope.FleetID, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResourceBinding{}
	for rows.Next() {
		var raw string
		var b ResourceBinding
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal([]byte(raw), &b); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveInstanceResourceBinding records pins only. Runtime access still requires a
// live provider scope check through ResolveResourceBindings.
func (s *PgStore) SaveInstanceResourceBinding(ctx context.Context, scope FleetScope, snapshot InstanceSnapshot, b ResourceBinding) (err error) {
	if !bindingMatchesSnapshot(b, scope, snapshot) {
		return ErrInvalid
	}
	policy, ok := snapshot.Template.Resources[b.Name]
	if !ok || b.Ownership != policy.Ownership || b.AttachmentID != policy.AttachmentID || b.AttachmentVersion != policy.Version {
		return ErrInvalid
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if len(raw) > 16384 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0))`); err != nil {
		return err
	}
	var environment string
	if err = tx.QueryRow(ctx, `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, scope.OrgID, scope.ProjectID, snapshot.EnvironmentID).Scan(&environment); err != nil {
		return storeError(err)
	}
	instance, err := scanInstance(tx.QueryRow(ctx, `SELECT `+instanceColumns+` FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4 AND status NOT IN ('retired','deleted') FOR SHARE`, scope.OrgID, scope.ProjectID, scope.FleetID, snapshot.InstanceID))
	if err != nil {
		return err
	}
	var release fleet.TemplateRelease
	var template string
	if err = tx.QueryRow(ctx, `SELECT id,fleet_id,digest,snapshot,created_at FROM loomplane_fleet_releases WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND id=$4`, scope.OrgID, scope.ProjectID, scope.FleetID, snapshot.ReleaseID).Scan(&release.ID, &release.FleetID, &release.Digest, &template, &release.CreatedAt); err != nil {
		return storeError(err)
	}
	if checkErr := json.Unmarshal([]byte(template), &release.Template); checkErr != nil {
		return checkErr
	}
	rebuilt, err := RenderInstance(release, *instance, fleet.CreateInstanceInput{StageID: snapshot.StageID, ReleaseID: snapshot.ReleaseID, ProfileID: snapshot.ProfileID, Parameters: snapshot.Parameters, SecretRefs: snapshot.SecretRefs})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*rebuilt, snapshot) {
		return ErrConflict
	}
	var valid bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_catalog_endpoints e JOIN loomplane_cloud_connections c ON c.org_id=e.org_id WHERE e.org_id=$1 AND e.id=$2 AND e.revision=$3 AND c.id=$4 AND c.generation=$5 AND c.status='ready')`, scope.OrgID, b.Credential.EndpointID, b.Credential.Version, b.ConnectionID, b.ConnectionGeneration).Scan(&valid); checkErr != nil {
		return checkErr
	}
	if !valid {
		return ErrConflict
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT body FROM loomplane_fleet_resource_bindings WHERE org_id=$1 AND instance_id=$2 AND generation=$3 AND name=$4`, scope.OrgID, b.InstanceID, b.Generation, b.Name).Scan(&existing)
	if err == nil {
		if existing != string(raw) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	err = nil
	// The source gate serializes claims, including different instance rows.
	var ownershipConflict bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_resource_bindings WHERE org_id=$1 AND resource_id=$2 AND incarnation=$3 AND (ownership='owned' OR $4='owned') AND (instance_id<>$5 OR ownership<>$4))`, scope.OrgID, b.ResourceID, b.Incarnation, b.Ownership, b.InstanceID).Scan(&ownershipConflict); checkErr != nil {
		return checkErr
	}
	if ownershipConflict {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_resource_principals(org_id,fleet_id,instance_id,resource_id,incarnation,principal_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, scope.OrgID, scope.FleetID, b.InstanceID, b.ResourceID, b.Incarnation, b.PrincipalID); err != nil {
		return storeError(err)
	}
	var owner string
	if checkErr := tx.QueryRow(ctx, `SELECT instance_id FROM loomplane_fleet_resource_principals WHERE org_id=$1 AND resource_id=$2 AND incarnation=$3 AND principal_id=$4`, scope.OrgID, b.ResourceID, b.Incarnation, b.PrincipalID).Scan(&owner); checkErr != nil {
		return checkErr
	}
	if owner != b.InstanceID {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_fleet_resource_bindings(id,org_id,project_id,fleet_id,instance_id,environment_id,name,generation,ownership_epoch,ownership,resource_id,incarnation,principal_id,endpoint_id,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, b.ID, scope.OrgID, scope.ProjectID, scope.FleetID, b.InstanceID, b.EnvironmentID, b.Name, b.Generation, b.OwnershipEpoch, b.Ownership, b.ResourceID, b.Incarnation, b.PrincipalID, b.Credential.EndpointID, string(raw))
	if err != nil {
		return storeError(err)
	}
	return tx.Commit()
}
