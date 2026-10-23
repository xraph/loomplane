package cloudinfra

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

// ClaimCompensationHandoff restores only an accepted retained compensation's
// closed handoff. Callers first drain both ingress generations under their locks.
func (s *PgStore) ClaimCompensationHandoff(ctx context.Context, prepared *RouteOperation, current *Route, cutover *RouteOperation) error {
	attempt, ok := WorkloadAttemptFrom(ctx)
	if !ok || prepared.Source == nil || prepared.Route.OrgID != attempt.Org || prepared.Route.ProjectID != attempt.Project || prepared.Route.Input.EnvironmentID != attempt.Environment || prepared.Route.Input.Service != attempt.Service {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, attempt.Org); checkErr != nil {
		return checkErr
	}
	if checkErr := LockRouteWorkload(ctx, tx, attempt.Org, attempt.Environment, attempt.Service); checkErr != nil {
		return checkErr
	}
	if checkErr := GuardPoolWrite(ctx, tx); checkErr != nil {
		return checkErr
	}
	var released bool
	var claimed string
	if err = tx.QueryRow(ctx, `SELECT released,compensation_plan_id FROM loomplane_route_handoffs WHERE org_id=$1 AND id=$2 AND route_id=$3 AND source_connection_id=$4 AND destination_connection_id=$5 FOR UPDATE`, attempt.Org, prepared.ID, current.ID, prepared.Source.Binding.ConnectionID, prepared.Route.Binding.ConnectionID).Scan(&released, &claimed); err != nil {
		return ErrConflict
	}
	if !released && claimed == "" {
		return tx.Commit()
	}
	var planID string
	var actionRaw []byte
	err = tx.QueryRow(ctx, `SELECT n.id,a FROM loomplane_plans n JOIN loomplane_cloud_syncs c ON c.org_id=n.org_id AND c.id=n.result->>'cloud_sync_id' CROSS JOIN LATERAL jsonb_array_elements(n.result->'actions') a WHERE n.org_id=$1 AND n.project_id=$2 AND n.environment_id=$3 AND c.status='running' AND a->>'name'=$4 AND a->'compensation'->>'plan_id'=$5 AND loomplane_retained_compensation(n,a)`, attempt.Org, attempt.Project, attempt.Environment, attempt.Service, attempt.Plan).Scan(&planID, &actionRaw)
	if err != nil || claimed != "" && claimed != planID {
		return ErrConflict
	}
	var action struct {
		Compensation struct {
			PlanID   string  `json:"plan_id"`
			ActionID string  `json:"action_id"`
			Routes   []Route `json:"routes"`
			Source   struct {
				ExternalID   string `json:"external_id"`
				ConnectionID string `json:"connection_id"`
			} `json:"source"`
			Candidate struct {
				ExternalID   string `json:"external_id"`
				ConnectionID string `json:"connection_id"`
			} `json:"candidate"`
		} `json:"compensation"`
	}
	if json.Unmarshal(actionRaw, &action) != nil {
		return ErrConflict
	}
	if prepared.Source.Binding.InstanceID != action.Compensation.Source.ExternalID || prepared.Source.Binding.ConnectionID != action.Compensation.Source.ConnectionID || prepared.Route.Binding.InstanceID != action.Compensation.Candidate.ExternalID || prepared.Route.Binding.ConnectionID != action.Compensation.Candidate.ConnectionID {
		return ErrConflict
	}
	if !reflect.DeepEqual(current.Binding, prepared.Source.Binding) && !reflect.DeepEqual(current.Binding, prepared.Route.Binding) {
		return ErrConflict
	}
	if prepared.ID != releasePrepareID(action.Compensation.PlanID+"/"+action.Compensation.ActionID, current.ID) {
		return ErrConflict
	}
	var sourceMatches bool
	for _, source := range action.Compensation.Routes {
		if reflect.DeepEqual(source, *prepared.Source) {
			sourceMatches = true
		}
	}
	if !sourceMatches {
		return ErrConflict
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return err
	}
	var matches bool
	if err = tx.QueryRow(ctx, `SELECT body=$4::jsonb FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, attempt.Org, attempt.Project, current.ID, raw).Scan(&matches); err != nil || !matches {
		return ErrConflict
	}
	preparedRaw, _, _, err := routeBytes(prepared)
	if err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT revision=$3 AND body=$4::jsonb FROM loomplane_route_operations WHERE org_id=$1 AND id=$2 FOR UPDATE`, attempt.Org, prepared.ID, prepared.Revision, preparedRaw).Scan(&matches); err != nil || !matches {
		return ErrConflict
	}
	var latest string
	if checkErr := tx.QueryRow(ctx, `SELECT coalesce((SELECT id FROM loomplane_route_operations WHERE org_id=$1 AND body->>'handoff_id'=$2 AND body->>'kind'='cutover' ORDER BY body->>'created_at' DESC,id DESC LIMIT 1),'')`, attempt.Org, prepared.ID).Scan(&latest); checkErr != nil {
		return checkErr
	}
	if cutover == nil {
		if latest != "" {
			return ErrConflict
		}
	} else {
		if cutover.ID != latest || cutover.DNSAttempted && cutover.Receipt == nil {
			return ErrConflict
		}
		cutoverRaw, _, _, encodeErr := routeBytes(cutover)
		if encodeErr != nil {
			return encodeErr
		}
		if err = tx.QueryRow(ctx, `SELECT revision=$3 AND body=$4::jsonb FROM loomplane_route_operations WHERE org_id=$1 AND id=$2 FOR UPDATE`, attempt.Org, cutover.ID, cutover.Revision, cutoverRaw).Scan(&matches); err != nil || !matches {
			return ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_route_handoffs SET released=false,compensation_plan_id=$3 WHERE org_id=$1 AND id=$2`, attempt.Org, prepared.ID, planID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES($1,$2,'route_handoff',$4),($1,$3,'route_handoff',$4) ON CONFLICT DO NOTHING`, attempt.Org, prepared.Source.Binding.ConnectionID, prepared.Route.Binding.ConnectionID, prepared.ID); err != nil {
		return err
	}
	return tx.Commit()
}
