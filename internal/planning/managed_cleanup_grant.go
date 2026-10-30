package planning

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/security"
)

var ErrManagedCleanupUnavailable = errors.New("managed cleanup descendant ownership verification is unavailable")

// ManagedCleanupSelection selects an immutable approval, never a supplied target.
type ManagedCleanupSelection struct {
	Org, Plan, Action, Deployment string
	TargetIndex                   int
	FailedReviewID                string
	ExecutionAttempt              int
}

// ManagedCleanupGrant remains available after its binding and instance are gone.
type ManagedCleanupGrant struct {
	ID                          string
	Selection                   ManagedCleanupSelection
	Target                      ManagedCleanupTarget
	SchemaVersion               int
	Revision, AuthorityRevision int64
	CreatedAt, UpdatedAt        time.Time
	RevokedAt                   *time.Time
}

// ManagedCleanupStore rechecks the current approval actor for every issuance.
// Runtime deletion and descendant observation are separate from this store.
type ManagedCleanupStore struct {
	db         *pgdriver.PgDB
	authz      *security.Authorizer
	principals security.PrincipalVerifier
	journal    *kube.ControllerStore
}

func NewManagedCleanupStore(db *pgdriver.PgDB, authz *security.Authorizer, principals security.PrincipalVerifier) *ManagedCleanupStore {
	return &ManagedCleanupStore{db: db, authz: authz, principals: principals, journal: kube.NewControllerStore(db)}
}

func (s *ManagedCleanupStore) actor(ctx context.Context, p security.Principal, org, project string) error {
	if s.authz == nil || p.OrgID != org || p.UserID == "" || p.ReadOnly {
		return kube.ErrManagedAuthority
	}
	if caller, ok := security.PrincipalFrom(ctx); ok && caller != p {
		return security.ErrForbidden
	}
	if p.KeyID != "" {
		if s.principals == nil {
			return security.ErrForbidden
		}
		if checkErr := s.principals.ValidatePrincipal(ctx, p); checkErr != nil {
			return checkErr
		}
	}
	if checkErr := s.authz.RequireCurrentMembership(ctx, p); checkErr != nil {
		return checkErr
	}
	return s.authz.Require(ctx, p, security.DeploymentApprove, project)
}

func (s *ManagedCleanupStore) loadTarget(ctx context.Context, tx driver.Tx, sel ManagedCleanupSelection) (ManagedCleanupTarget, error) {
	if sel.FailedReviewID != "" {
		return s.loadFailedTarget(ctx, tx, sel)
	}
	var target ManagedCleanupTarget
	if sel.Org == "" || sel.Plan == "" || sel.Action == "" || sel.Deployment == "" || sel.TargetIndex < 0 {
		return target, kube.ErrManagedAuthority
	}
	var raw, actor []byte
	var project, environment string
	err := tx.QueryRow(ctx, `SELECT i.body->$5::integer,i.actor,p.project_id,p.environment_id FROM loomplane_managed_cleanup_intents i
 JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.revision=i.plan_revision AND p.status='approved'
 JOIN loomplane_deployments d ON d.org_id=p.org_id AND d.plan_id=p.id AND d.id=$4 AND d.project_id=p.project_id AND d.environment_id=p.environment_id AND d.status='running'
 WHERE i.org_id=$1 AND i.plan_id=$2 AND i.action_id=$3 AND i.schema_version=1
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') a WHERE a->>'id'=i.action_id AND a->>'subject'='service' AND a->>'op' IN ('delete','replace','update') AND a->'cleanup_targets'=i.body)
 FOR SHARE OF i,p,d`, sel.Org, sel.Plan, sel.Action, sel.Deployment, sel.TargetIndex).Scan(&raw, &actor, &project, &environment)
	if err != nil {
		return target, errors.Join(kube.ErrManagedAuthority, err)
	}
	var principal security.Principal
	if json.Unmarshal(raw, &target) != nil || json.Unmarshal(actor, &principal) != nil || target.SchemaVersion != 1 || target.Scope.Org != sel.Org || target.Scope.Project != project || target.Scope.Environment != environment || target.Scope.Epoch != target.AuthorityEpoch || !target.IdentityCurrent || len(target.Unresolved) != 0 {
		return target, kube.ErrManagedAuthority
	}
	if checkErr := s.actor(ctx, principal, sel.Org, project); checkErr != nil {
		return target, checkErr
	}
	return target, nil
}

// Open revokes application execution before creating independently revocable
// cleanup authority. Retrying Open never reopens a revoked cleanup grant.
func (s *ManagedCleanupStore) Open(ctx context.Context, sel ManagedCleanupSelection) (_ *ManagedCleanupGrant, err error) {
	if sel.FailedReviewID != "" {
		return s.openFailedGrant(ctx, sel)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, sel.Org); checkErr != nil {
		return nil, checkErr
	}
	target, err := s.loadTarget(ctx, tx, sel)
	if err != nil {
		return nil, err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT id FROM loomplane_managed_cleanup_grants WHERE org_id=$1 AND plan_id=$2 AND action_id=$3 AND deployment_id=$4 AND target_index=$5 AND failed_review_id IS NULL`, sel.Org, sel.Plan, sel.Action, sel.Deployment, sel.TargetIndex).Scan(&existing)
	if err == nil {
		grant, e := s.grant(ctx, tx, sel.Org, existing)
		if e != nil {
			return nil, e
		}
		if grant.RevokedAt != nil {
			return nil, kube.ErrManagedAuthority
		}
		if e := s.guard(ctx, tx, grant.Target, grant.AuthorityRevision); e != nil {
			return nil, e
		}
		return grant, tx.Commit()
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if checkErr := s.guard(ctx, tx, target, target.AuthorityRevision); checkErr != nil {
		return nil, checkErr
	}
	var revision int64
	err = tx.QueryRow(ctx, `UPDATE loomplane_managed_workload_authority SET revoked_at=coalesce(revoked_at,clock_timestamp()),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND release_id=$2 AND revision=$3 RETURNING revision`, sel.Org, target.Scope.Release, target.AuthorityRevision).Scan(&revision)
	if err != nil {
		return nil, err
	}
	id, err := typeid.Generate("cleanupgrant")
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_cleanup_grants(id,org_id,plan_id,action_id,deployment_id,target_index,release_id,authority_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id.String(), sel.Org, sel.Plan, sel.Action, sel.Deployment, sel.TargetIndex, target.Scope.Release, revision)
	if err != nil {
		return nil, err
	}
	grant, err := s.grant(ctx, tx, sel.Org, id.String())
	if err != nil {
		return nil, err
	}
	return grant, tx.Commit()
}

func (s *ManagedCleanupStore) grant(ctx context.Context, tx driver.Tx, org, id string) (*ManagedCleanupGrant, error) {
	var failed bool
	if err := tx.QueryRow(ctx, `SELECT failed_review_id IS NOT NULL FROM loomplane_managed_cleanup_grants WHERE org_id=$1 AND id=$2`, org, id).Scan(&failed); err != nil {
		return nil, err
	}
	if failed {
		return s.failedGrant(ctx, tx, org, id)
	}
	out := &ManagedCleanupGrant{ID: id}
	out.Selection.Org = org
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT g.plan_id,g.action_id,g.deployment_id,g.target_index,g.schema_version,g.revision,g.authority_revision,g.created_at,g.updated_at,g.revoked_at,i.body->g.target_index
 FROM loomplane_managed_cleanup_grants g JOIN loomplane_managed_cleanup_intents i ON i.org_id=g.org_id AND i.plan_id=g.plan_id AND i.action_id=g.action_id WHERE g.org_id=$1 AND g.id=$2 FOR UPDATE OF g`, org, id).Scan(&out.Selection.Plan, &out.Selection.Action, &out.Selection.Deployment, &out.Selection.TargetIndex, &out.SchemaVersion, &out.Revision, &out.AuthorityRevision, &out.CreatedAt, &out.UpdatedAt, &out.RevokedAt, &raw)
	if err != nil {
		return nil, errors.Join(kube.ErrManagedAuthority, err)
	}
	if json.Unmarshal(raw, &out.Target) != nil || out.SchemaVersion != 1 {
		return nil, kube.ErrManagedAuthority
	}
	return out, nil
}

// Revoke denies future issuance. Historical outcome resolution remains available.
func (s *ManagedCleanupStore) Revoke(ctx context.Context, org, id string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, org); checkErr != nil {
		return checkErr
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_managed_failed_cleanups SET revoked_at=coalesce(revoked_at,clock_timestamp()),status=CASE WHEN status='pending' THEN 'failed' ELSE status END,revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id IN (SELECT failed_review_id FROM loomplane_managed_cleanup_grants WHERE org_id=$1 AND id=$2)`, org, id); err != nil {
		return err
	}
	var saved string
	err = tx.QueryRow(ctx, `UPDATE loomplane_managed_cleanup_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2 RETURNING id`, org, id).Scan(&saved)
	if err != nil {
		return err
	}
	return tx.Commit()
}
