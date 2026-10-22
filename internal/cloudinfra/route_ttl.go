package cloudinfra

import (
	"context"
	"encoding/json"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

type RouteTTLInput struct {
	Revision           int64 `json:"revision"`
	ExternalTTLSeconds int   `json:"external_ttl_seconds"`
}

// UpdateRouteTTL records the maximum previously served external DNS TTL.
func (s *Service) UpdateRouteTTL(ctx context.Context, p security.Principal, project, id string, in RouteTTLInput) (*Route, error) {
	if err := s.domainScope(ctx, p, project, true); err != nil {
		return nil, err
	}
	if project == "" || in.ExternalTTLSeconds < 60 || in.ExternalTTLSeconds > 86400 {
		return nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	route, err := st.Route(ctx, p.OrgID, project, id)
	if err != nil {
		return nil, err
	}
	if route.Revision != in.Revision || route.Status != "ready" {
		return nil, ErrConflict
	}
	zone, err := st.Zone(ctx, p.OrgID, route.Input.ZoneID)
	if err != nil {
		return nil, err
	}
	if zone.Mode != "external" {
		return nil, ErrInvalid
	}
	writer, ok := st.(interface {
		UpdateRouteTTL(context.Context, *Route, int) error
	})
	if !ok {
		return nil, ErrInvalid
	}
	if checkErr := writer.UpdateRouteTTL(ctx, route, in.ExternalTTLSeconds); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.route.ttl", route.ID, project)
	return route, nil
}

func (s *PgStore) UpdateRouteTTL(ctx context.Context, route *Route, seconds int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, route.OrgID); checkErr != nil {
		return checkErr
	}
	if checkErr := guardRouteAttachment(ctx, tx, route, "ttl"); checkErr != nil {
		return checkErr
	}
	var busy bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_route_handoffs WHERE org_id=$1 AND route_id=$2 AND NOT released) OR EXISTS(SELECT 1 FROM loomplane_route_operations WHERE org_id=$1 AND route_id=$2 AND status IN ('pending','running','waiting','failed'))`, route.OrgID, route.ID).Scan(&busy); checkErr != nil {
		return checkErr
	}
	if busy {
		return ErrBusy
	}
	next := *route
	next.Input.ExternalTTLSeconds = seconds
	next.Revision++
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	var id string
	if checkErr := tx.QueryRow(ctx, `UPDATE loomplane_domain_routes SET revision=$4,body=$5 WHERE org_id=$1 AND project_id=$2 AND id=$3 AND revision=$6 AND status='ready' RETURNING id`, route.OrgID, route.ProjectID, route.ID, next.Revision, raw, route.Revision).Scan(&id); checkErr != nil {
		return storeError(checkErr)
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return checkErr
	}
	*route = next
	return nil
}
