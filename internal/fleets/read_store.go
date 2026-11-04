package fleets

import (
	"context"

	"github.com/xraph/loomplane/pkg/fleet"
)

var _ ReadStore = (*PgStore)(nil)

func (s *PgStore) GetInstance(ctx context.Context, org, id string) (*fleet.Instance, error) {
	return scanInstance(s.db.QueryRow(ctx, `SELECT `+instanceColumns+` FROM loomplane_fleet_instances WHERE org_id=$1 AND id=$2`, org, id))
}

func (s *PgStore) GetOperation(ctx context.Context, org, id string) (*fleet.Operation, error) {
	var op fleet.Operation
	err := s.db.QueryRow(ctx, `SELECT id,instance_id,kind,status,request_hash,generation,error_code,created_at FROM loomplane_fleet_operations WHERE org_id=$1 AND id=$2`, org, id).Scan(&op.ID, &op.InstanceID, &op.Kind, &op.Status, &op.RequestHash, &op.Generation, &op.ErrorCode, &op.CreatedAt)
	if err != nil {
		return nil, storeError(err)
	}
	op.CreatedAt = op.CreatedAt.UTC()
	return &op, nil
}

func (s *PgStore) ListInstances(ctx context.Context, scope FleetScope, in fleet.ListInstancesInput) (*fleet.InstancePage, error) {
	in, err := instancePageInput(in)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+instanceColumns+` FROM loomplane_fleet_instances WHERE org_id=$1 AND project_id=$2 AND fleet_id=$3 AND ($4='' OR id>$4) AND ($5='' OR status=$5) AND ($6='' OR external_ref=$6) ORDER BY id LIMIT $7`, scope.OrgID, scope.ProjectID, scope.FleetID, in.After, in.Status, in.ExternalRef, in.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &fleet.InstancePage{Items: make([]fleet.Instance, 0, in.Limit)}
	for rows.Next() {
		i, scanErr := scanInstance(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if len(page.Items) == in.Limit {
			page.NextCursor = page.Items[len(page.Items)-1].ID
			break
		}
		page.Items = append(page.Items, *i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return page, nil
}
