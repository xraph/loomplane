package currentauthority

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/security"
)

type fleetKeyRow struct {
	Key           string     `json:"key"`
	App           string     `json:"app"`
	Org           string     `json:"org"`
	OrgApp        string     `json:"org_app"`
	Creator       string     `json:"creator"`
	State         string     `json:"state"`
	Access        string     `json:"access"`
	Kind          string     `json:"kind"`
	Project       string     `json:"project"`
	Fleet         string     `json:"fleet"`
	Revoked       bool       `json:"revoked"`
	Policy        bool       `json:"policy"`
	Scopes        bool       `json:"scopes"`
	MetadataValid bool       `json:"metadata_valid"`
	Until         *time.Time `json:"until"`
}

func (a *Attempt) readFleetFacts(ctx context.Context) (fleetFacts, error) {
	var facts fleetFacts
	table := func(name string) string { return pgx.Identifier{a.Schema(), name}.Sanitize() }
	err := a.ordinaryRead(ctx, func(ctx context.Context, db *pgdriver.PgDB) error {
		q := `SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object(
   'key', k.id, 'app', k.app_id, 'org', k.tenant_id, 'org_app', o.app_id,
   'creator', coalesce(k.created_by,''), 'state', k.state,
   'access', coalesce(k.metadata OPERATOR(pg_catalog.->>) 'access',''),
   'kind', coalesce(k.metadata OPERATOR(pg_catalog.->>) 'kind',''),
   'project', coalesce(k.metadata OPERATOR(pg_catalog.->>) 'project_id',''),
   'fleet', coalesce(k.metadata OPERATOR(pg_catalog.->>) 'fleet_id',''),
   'revoked', k.revoked_at IS NOT NULL, 'policy', k.policy_id IS NOT NULL,
   'scopes', EXISTS(SELECT 1 FROM ` + table("keysmith_key_scopes") + ` s WHERE s.key_id OPERATOR(pg_catalog.=) k.id),
   'metadata_valid', coalesce(
    pg_catalog.jsonb_typeof(k.metadata OPERATOR(pg_catalog.->) 'access') OPERATOR(pg_catalog.=) 'string'
    AND pg_catalog.jsonb_typeof(k.metadata OPERATOR(pg_catalog.->) 'kind') OPERATOR(pg_catalog.=) 'string'
    AND pg_catalog.jsonb_typeof(k.metadata OPERATOR(pg_catalog.->) 'project_id') OPERATOR(pg_catalog.=) 'string'
    AND pg_catalog.jsonb_typeof(k.metadata OPERATOR(pg_catalog.->) 'fleet_id') OPERATOR(pg_catalog.=) 'string',false),
   'until', k.expires_at)), '[]'::pg_catalog.jsonb)
   FROM ` + table("keysmith_keys") + ` k
   JOIN ` + table("authsome_organizations") + ` o ON o.id OPERATOR(pg_catalog.=) k.tenant_id
   WHERE k.id OPERATOR(pg_catalog.=) $1`
		var encoded []byte
		if err := db.QueryRow(ctx, q, a.binding.key).Scan(&encoded); err != nil {
			return err
		}
		var rows []fleetKeyRow
		if err := json.Unmarshal(encoded, &rows); err != nil {
			return ErrUnavailable
		}
		if len(rows) == 0 {
			return security.ErrForbidden
		}
		if len(rows) != 1 {
			return ErrUnavailable
		}
		r := rows[0]
		if !r.MetadataValid {
			return security.ErrForbidden
		}
		facts = fleetFacts{key: r.Key, app: r.App, org: r.Org, orgApp: r.OrgApp, creator: r.Creator, state: r.State, access: r.Access, kind: r.Kind, project: r.Project, fleet: r.Fleet, revoked: r.Revoked, policy: r.Policy, scopes: r.Scopes, until: instant(r.Until)}
		return nil
	})
	return facts, err
}
