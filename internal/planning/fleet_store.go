package planning

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/projects"
)

// checkPlanOwnership serializes plan creation with environment ownership changes.
func checkPlanOwnership(ctx context.Context, tx driver.Tx, p *Plan) ([]byte, error) {
	var environment string
	if err := tx.QueryRow(ctx, `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, p.OrgID, p.ProjectID, p.EnvironmentID).Scan(&environment); err != nil {
		return nil, err
	}
	var instance, fleet, release, profile string
	var generation, epoch int64
	err := tx.QueryRow(ctx, `SELECT id,fleet_id,desired_release_id,profile_id,desired_generation,ownership_epoch FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 FOR SHARE`, p.OrgID, p.ProjectID, p.EnvironmentID).Scan(&instance, &fleet, &release, &profile, &generation, &epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		if p.Kind == KindInstance || p.FleetInput != nil {
			return nil, ErrInvalid
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in := p.FleetInput
	if p.Kind != KindInstance || in == nil {
		return nil, projects.ErrFleetManaged
	}
	if in.Authority.Scope.OrgID != p.OrgID || in.Authority.Scope.ProjectID != p.ProjectID || in.Authority.Scope.FleetID != fleet || in.Snapshot.InstanceID != instance || in.Snapshot.EnvironmentID != environment || in.Snapshot.Generation != generation || in.Snapshot.OwnershipEpoch != epoch || in.Snapshot.ReleaseID != release || in.Snapshot.ProfileID != profile {
		return nil, ErrInvalid
	}
	p.ManagedInstanceID = instance
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(raw) > 2097152 {
		return nil, ErrInvalid
	}
	return raw, nil
}
func (s *PgStore) withFleetInput(ctx context.Context, p *Plan) error {
	if p.Kind != KindInstance {
		return nil
	}
	var raw string
	if err := s.db.QueryRow(ctx, `SELECT snapshot FROM loomplane_fleet_plan_inputs WHERE org_id=$1 AND plan_id=$2 AND instance_id=$3`, p.OrgID, p.ID, p.ManagedInstanceID).Scan(&raw); err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), &p.FleetInput)
}
