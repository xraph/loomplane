package currentauthority

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/security"
)

// ordinaryRead admits no fallback path and never returns the native reader.
func (a *Attempt) ordinaryRead(ctx context.Context, read func(context.Context, *pgdriver.PgDB) error) (err error) {
	if _, scoped, scopeErr := a.owner.readerScope(ctx); scopeErr != nil || !scoped {
		if scopeErr == nil {
			scopeErr = ErrBinding
		}
		return a.fail(scopeErr)
	}
	backend, ok := a.owner.backend.(*nativeBackend)
	if !ok || backend.reader == nil {
		return a.fail(ErrUnavailable)
	}
	call, _, err := a.beginReader(ctx)
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			call.finishReader(ErrPoisoned)
		} else {
			call.finishReader(err)
		}
	}()
	err = read(call.ctx, backend.reader)
	completed = true
	return err
}

type personRow struct {
	User       string     `json:"user"`
	App        string     `json:"app"`
	Env        string     `json:"env"`
	Org        string     `json:"org"`
	OrgApp     string     `json:"org_app"`
	Member     string     `json:"member"`
	MemberRole string     `json:"member_role"`
	Verified   bool       `json:"verified"`
	Banned     bool       `json:"banned"`
	Deleted    bool       `json:"deleted"`
	BanUntil   *time.Time `json:"ban_until"`
}

type keyRow struct {
	ID      string     `json:"id"`
	App     string     `json:"app"`
	Org     string     `json:"org"`
	Creator string     `json:"creator"`
	State   string     `json:"state"`
	Access  string     `json:"access"`
	Revoked bool       `json:"revoked"`
	Policy  bool       `json:"policy"`
	Kind    bool       `json:"kind"`
	Scopes  bool       `json:"scopes"`
	Until   *time.Time `json:"until"`
}

func (a *Attempt) readOrdinaryFacts(ctx context.Context) (ordinaryFacts, []security.ScopedRole, error) {
	var facts ordinaryFacts
	var grants []security.ScopedRole
	table := func(n string) string { return pgx.Identifier{a.owner.schema, n}.Sanitize() }
	err := a.ordinaryRead(ctx, func(ctx context.Context, db *pgdriver.PgDB) error {
		var encoded []byte
		q := `SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('user',u.id,'app',u.app_id,'env',u.env_id,
   'verified',u.email_verified,'banned',u.banned,'deleted',u.deleted_at IS NOT NULL,'ban_until',u.ban_expires,
   'org',o.id,'org_app',o.app_id,'member',m.id,'member_role',m.role)),'[]'::pg_catalog.jsonb)
   FROM ` + table("authsome_users") + ` u JOIN ` + table("authsome_organizations") + ` o ON o.id OPERATOR(pg_catalog.=) $2
   JOIN ` + table("authsome_members") + ` m ON m.user_id OPERATOR(pg_catalog.=) u.id AND m.org_id OPERATOR(pg_catalog.=) o.id WHERE u.id OPERATOR(pg_catalog.=) $1`
		if err := db.QueryRow(ctx, q, a.binding.user, a.binding.org).Scan(&encoded); err != nil {
			return err
		}
		var rows []personRow
		if err := json.Unmarshal(encoded, &rows); err != nil {
			return fmt.Errorf("%w: invalid person record", ErrUnavailable)
		}
		if len(rows) == 0 {
			return security.ErrForbidden
		}
		if len(rows) != 1 {
			return ErrUnavailable
		}
		r := rows[0]
		facts = ordinaryFacts{user: r.User, app: r.App, env: r.Env, org: r.Org, orgApp: r.OrgApp, member: r.Member, memberRole: r.MemberRole, verified: r.Verified, banned: r.Banned, deleted: r.Deleted, banUntil: instant(r.BanUntil)}
		if a.binding.key != "" {
			q = `SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('id',k.id,'app',k.app_id,'org',k.tenant_id,'creator',coalesce(k.created_by,''),
    'state',k.state,'access',coalesce(k.metadata OPERATOR(pg_catalog.->>) 'access',''),'revoked',k.revoked_at IS NOT NULL,'policy',k.policy_id IS NOT NULL,
    'kind',coalesce(k.metadata OPERATOR(pg_catalog.?) 'kind',false),'until',k.expires_at,
    'scopes',EXISTS(SELECT 1 FROM ` + table("keysmith_key_scopes") + ` ks WHERE ks.key_id OPERATOR(pg_catalog.=) k.id))),'[]'::pg_catalog.jsonb)
    FROM ` + table("keysmith_keys") + ` k WHERE k.id OPERATOR(pg_catalog.=) $1`
			if err := db.QueryRow(ctx, q, a.binding.key).Scan(&encoded); err != nil {
				return err
			}
			var keys []keyRow
			if err := json.Unmarshal(encoded, &keys); err != nil {
				return ErrUnavailable
			}
			if len(keys) == 0 {
				return security.ErrForbidden
			}
			if len(keys) != 1 {
				return ErrUnavailable
			}
			k := keys[0]
			facts.keyID = k.ID
			facts.keyApp = k.App
			facts.keyOrg = k.Org
			facts.keyCreator = k.Creator
			facts.keyState = k.State
			facts.keyAccess = k.Access
			facts.keyRevoked = k.Revoked
			facts.keyPolicy = k.Policy
			facts.keyKind = k.Kind
			facts.keyScopes = k.Scopes
			facts.keyUntil = instant(k.Until)
		}
		q = `SELECT coalesce(pg_catalog.jsonb_agg(pg_catalog.jsonb_build_object('Role',g.role,'ProjectID',coalesce(g.project_id,''),'TeamID',g.team_id)
   ORDER BY g.project_id NULLS FIRST,g.team_id),'[]'::pg_catalog.jsonb)
   FROM ` + table("loomplane_team_members") + ` tm JOIN ` + table("authsome_members") + ` am
   ON am.id OPERATOR(pg_catalog.=) tm.member_id AND am.org_id OPERATOR(pg_catalog.=) tm.org_id AND am.user_id OPERATOR(pg_catalog.=) tm.user_id
   JOIN ` + table("loomplane_teams") + ` t ON t.id OPERATOR(pg_catalog.=) tm.team_id AND t.org_id OPERATOR(pg_catalog.=) tm.org_id
   JOIN ` + table("loomplane_team_grants") + ` g ON g.team_id OPERATOR(pg_catalog.=) t.id AND g.org_id OPERATOR(pg_catalog.=) t.org_id
   LEFT JOIN ` + table("loomplane_projects") + ` p ON p.id OPERATOR(pg_catalog.=) g.project_id
   WHERE tm.org_id OPERATOR(pg_catalog.=) $1 AND tm.user_id OPERATOR(pg_catalog.=) $2 AND (g.project_id IS NULL OR p.org_id OPERATOR(pg_catalog.=) g.org_id)`
		if err := db.QueryRow(ctx, q, a.binding.org, a.binding.user).Scan(&encoded); err != nil {
			return err
		}
		if err := json.Unmarshal(encoded, &grants); err != nil {
			return ErrUnavailable
		}
		return nil
	})
	return facts, grants, err
}
