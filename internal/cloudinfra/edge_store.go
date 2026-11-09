package cloudinfra

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

var _ EdgeStore = (*PgStore)(nil)

func (s *PgStore) Edge(ctx context.Context, org, route string) (*EdgeState, error) {
	var raw, principal []byte
	if checkErr := s.db.QueryRow(ctx, `SELECT body,principal FROM loomplane_route_edge WHERE org_id=$1 AND route_id=$2`, org, route).Scan(&raw, &principal); checkErr != nil {
		return nil, storeError(checkErr)
	}
	var state EdgeState
	if checkErr := json.Unmarshal(raw, &state); checkErr != nil {
		return nil, checkErr
	}
	return &state, json.Unmarshal(principal, &state.Principal)
}

func (s *PgStore) AcceptEdge(ctx context.Context, route *Route, state *EdgeState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, route.OrgID); checkErr != nil {
		return checkErr
	}
	if checkErr := guardRouteAttachment(ctx, tx, route, "edge"); checkErr != nil {
		return checkErr
	}
	var revision int64
	if checkErr := tx.QueryRow(ctx, `SELECT revision FROM loomplane_domain_routes WHERE org_id=$1 AND project_id=$2 AND id=$3 AND status='ready' FOR UPDATE`, route.OrgID, route.ProjectID, route.ID).Scan(&revision); checkErr != nil {
		return storeError(checkErr)
	}
	if revision != route.Revision {
		return ErrConflict
	}
	var busy bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_route_operations WHERE org_id=$1 AND route_id=$2 AND status IN ('pending','running','waiting','failed')) OR EXISTS(SELECT 1 FROM loomplane_route_handoffs WHERE org_id=$1 AND route_id=$2 AND NOT released)`, route.OrgID, route.ID).Scan(&busy); checkErr != nil {
		return checkErr
	}
	if busy {
		return ErrBusy
	}
	next := *state
	next.Revision++
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	principal, err := json.Marshal(next.Principal)
	if err != nil {
		return err
	}
	if state.Revision == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_route_edge(org_id,route_id,project_id,revision,body,principal) VALUES($1,$2,$3,$4,$5,$6)`, route.OrgID, route.ID, route.ProjectID, next.Revision, raw, principal)
	} else {
		err = tx.QueryRow(ctx, `UPDATE loomplane_route_edge SET revision=$4,body=$5,principal=$6 WHERE org_id=$1 AND route_id=$2 AND revision=$3 RETURNING revision`, route.OrgID, route.ID, state.Revision, next.Revision, raw, principal).Scan(&revision)
	}
	if err != nil {
		return storeError(err)
	}
	if err = tx.Commit(); err == nil {
		*state = next
	}
	return err
}

func (s *PgStore) SaveEdge(ctx context.Context, state *EdgeState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var revision int64
	err = s.db.QueryRow(ctx, `UPDATE loomplane_route_edge SET body=$4 WHERE org_id=$1 AND route_id=$2 AND revision=$3 RETURNING revision`, state.OrgID, state.RouteID, state.Revision, raw).Scan(&revision)
	return storeError(err)
}

func (s *PgStore) PublishEdge(ctx context.Context, route *Route, state *EdgeState) error {
	if state.Kind == "purge" {
		return s.SaveEdge(ctx, state)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, route.OrgID); checkErr != nil {
		return checkErr
	}
	next := *route
	next.Revision++
	next.UpdatedAt = state.LastPurgeAt
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	var revision int64
	if checkErr := tx.QueryRow(ctx, `UPDATE loomplane_domain_routes SET body=$4,revision=$5 WHERE org_id=$1 AND project_id=$2 AND id=$3 AND revision=$6 RETURNING revision`, route.OrgID, route.ProjectID, route.ID, raw, next.Revision, route.Revision).Scan(&revision); checkErr != nil {
		return storeError(checkErr)
	}
	raw, err = json.Marshal(state)
	if err != nil {
		return err
	}
	if checkErr := tx.QueryRow(ctx, `UPDATE loomplane_route_edge SET body=$4 WHERE org_id=$1 AND route_id=$2 AND revision=$3 RETURNING revision`, state.OrgID, state.RouteID, state.Revision, raw).Scan(&revision); checkErr != nil {
		return storeError(checkErr)
	}
	if err = tx.Commit(); err == nil {
		*route = next
	}
	return err
}

func (s *PgStore) PendingEdges(ctx context.Context) ([]*EdgeState, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_route_edge WHERE body->>'status'='pending' ORDER BY body->>'requested_at' LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := []*EdgeState{}
	for rows.Next() {
		var raw []byte
		var state EdgeState
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &state); checkErr != nil {
			return nil, checkErr
		}
		states = append(states, &state)
	}
	return states, rows.Err()
}

// Route changes share the organization lock with edge acceptance. A failed
// edge operation may already have changed Cloudflare, so it must settle first.
func guardEdgeRoute(ctx context.Context, tx driver.Tx, route *Route, kind string) error {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_route_edge WHERE org_id=$1 AND route_id=$2 AND (body->>'status' IN ('pending','failed') OR ($3='remove' AND body->'policy'->>'enabled'='true')))`, route.OrgID, route.ID, kind).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("%w: finish the CDN operation, and disable CDN before removing this hostname", ErrBusy)
	}
	return nil
}

func (s *PgStore) QueueEdgeInvalidations(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `INSERT INTO loomplane_edge_invalidations(org_id,route_id,project_id,deployment_id)
 SELECT e.org_id,e.route_id,e.project_id,d.id FROM loomplane_route_edge e
 JOIN loomplane_domain_routes r ON r.org_id=e.org_id AND r.id=e.route_id AND r.status='ready'
 JOIN loomplane_deployments d ON d.org_id=e.org_id AND d.project_id=e.project_id AND d.environment_id=r.body->'input'->>'environment_id'
 WHERE e.body->>'status'='ready' AND e.body->'policy'->>'enabled'='true' AND e.body->'policy'->>'invalidate_on_deploy'='true'
 AND d.status='succeeded' AND d.finished_at>(e.body->>'applied_at')::timestamptz
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(d.steps::jsonb) step WHERE step->>'name'=r.body->'input'->>'service' AND step->>'subject'='service' AND step->>'status'='succeeded' AND step->>'op'<>'delete')
 ON CONFLICT DO NOTHING`)
	return err
}

func (s *PgStore) EdgeInvalidations(ctx context.Context) ([]EdgeInvalidation, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id,project_id,route_id,deployment_id FROM loomplane_edge_invalidations WHERE NOT done AND next_attempt<=now() ORDER BY next_attempt LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EdgeInvalidation{}
	for rows.Next() {
		var job EdgeInvalidation
		if checkErr := rows.Scan(&job.Org, &job.Project, &job.Route, &job.Deployment); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (s *PgStore) FinishEdgeInvalidation(ctx context.Context, job EdgeInvalidation, message string) error {
	_, err := s.db.Exec(ctx, `UPDATE loomplane_edge_invalidations SET done=($4=''),message=$4,attempts=attempts+1,next_attempt=now()+least(3600,30*power(2,least(attempts,7))) * interval '1 second' WHERE org_id=$1 AND route_id=$2 AND deployment_id=$3`, job.Org, job.Route, job.Deployment, message)
	return err
}
