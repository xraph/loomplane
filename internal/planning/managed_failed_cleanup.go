package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

var ErrManagedFailedCleanupConflict = errors.New("managed failed cleanup requires a current review")
var ErrManagedFailedCleanupUnavailable = errors.New("managed failed cleanup execution is unavailable")

const ManagedCleanupAbandonFailed ManagedCleanupKind = "abandon_failed_release"

// ManagedFailedCleanup retains each accepted actor and exact target set.
type ManagedFailedCleanup struct {
	Blockers     []string               `json:"blockers,omitempty"`
	ID           string                 `json:"review_id"`
	Org          string                 `json:"-"`
	Deployment   string                 `json:"deployment_id"`
	Sequence     int64                  `json:"review_sequence"`
	Predecessor  string                 `json:"predecessor,omitempty"`
	Digest       string                 `json:"revision"`
	Status       string                 `json:"status"`
	Attempt      int                    `json:"attempt"`
	Error        string                 `json:"error,omitempty"`
	Actor        security.Principal     `json:"-"`
	Targets      []ManagedCleanupTarget `json:"targets"`
	RevokedAt    *time.Time             `json:"-"`
	SupersededAt *time.Time             `json:"-"`
}

func scanManagedFailed(row pgx.Row) (*ManagedFailedCleanup, error) {
	var out ManagedFailedCleanup
	var actor, targets []byte
	err := row.Scan(&out.ID, &out.Org, &out.Deployment, &out.Sequence, &out.Predecessor, &out.Digest, &out.Status, &out.Attempt, &out.Error, &actor, &targets, &out.RevokedAt, &out.SupersededAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if checkErr := json.Unmarshal(actor, &out.Actor); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := json.Unmarshal(targets, &out.Targets); checkErr != nil {
		return nil, checkErr
	}
	return &out, nil
}

const managedFailedColumns = `id,org_id,deployment_id,sequence,coalesce(predecessor_id,''),review_digest,status,attempt,error,actor,targets,revoked_at,superseded_at`

func (s *ManagedCleanupStore) CurrentFailedCleanup(ctx context.Context, org, deployment string) (*ManagedFailedCleanup, error) {
	return scanManagedFailed(s.db.QueryRow(ctx, `SELECT `+managedFailedColumns+` FROM loomplane_managed_failed_cleanups WHERE org_id=$1 AND deployment_id=$2 AND superseded_at IS NULL`, org, deployment))
}

func (s *ManagedCleanupStore) failedReview(ctx context.Context, tx driver.Tx, org, id string) (*ManagedFailedCleanup, error) {
	return scanManagedFailed(tx.QueryRow(ctx, `SELECT `+managedFailedColumns+` FROM loomplane_managed_failed_cleanups WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, id))
}

func (s *ManagedCleanupStore) previewFailed(ctx context.Context, tx driver.Tx, p security.Principal, deployment string) (*ManagedFailedCleanup, error) {
	caller, ok := security.PrincipalFrom(ctx)
	if !ok || caller != p {
		return nil, security.ErrForbidden
	}
	var project string
	var retries int
	if err := tx.QueryRow(ctx, `SELECT project_id,retries FROM loomplane_deployments WHERE org_id=$1 AND id=$2 AND status IN ('failed','cancelled') FOR UPDATE`, p.OrgID, deployment).Scan(&project, &retries); err != nil {
		return nil, ErrManagedFailedCleanupConflict
	}
	if err := s.actor(ctx, p, p.OrgID, project); err != nil {
		return nil, err
	}
	out := &ManagedFailedCleanup{Org: p.OrgID, Deployment: deployment, Sequence: 1}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT loomplane_managed_failed_cleanup_targets($1,$2)`, p.OrgID, deployment).Scan(&raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out.Targets); err != nil {
		return nil, err
	}
	if len(out.Targets) == 0 {
		return nil, ErrNotFound
	}
	var predecessor, predecessorStatus string
	var sequence int64
	err := tx.QueryRow(ctx, `SELECT id,sequence,status FROM loomplane_managed_failed_cleanups WHERE org_id=$1 AND deployment_id=$2 AND superseded_at IS NULL FOR UPDATE`, p.OrgID, deployment).Scan(&predecessor, &sequence, &predecessorStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		out.Predecessor = predecessor
		out.Sequence = sequence + 1
		if predecessorStatus == "pending" {
			out.Blockers = append(out.Blockers, "managed_cleanup_review_in_progress")
		}
	}
	body, err := json.Marshal(struct {
		Deployment, Predecessor string
		Sequence                int64
		Retries                 int
		Targets                 []ManagedCleanupTarget
	}{deployment, out.Predecessor, out.Sequence, retries, out.Targets})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}

func (s *ManagedCleanupStore) PreviewFailedCleanup(ctx context.Context, p security.Principal, deployment string) (_ *ManagedFailedCleanup, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, p.OrgID); checkErr != nil {
		return nil, checkErr
	}
	out, err := s.previewFailed(ctx, tx, p, deployment)
	if err != nil {
		return nil, err
	}
	for _, target := range out.Targets {
		switch {
		case !target.IdentityCurrent:
			out.Blockers = append(out.Blockers, "managed_cleanup_identity_changed")
		case len(target.Unresolved) > 0:
			out.Blockers = append(out.Blockers, "managed_cleanup_writes_unresolved")
		default:
			if checkErr := s.failedTargetCurrent(ctx, tx, target, target.AuthorityRevision); checkErr != nil {
				out.Blockers = append(out.Blockers, "managed_cleanup_dependencies_blocked")
			}
		}
	}
	return out, tx.Commit()
}

// failedTargetCurrent refuses serving resources and competing live attempts.
func (s *ManagedCleanupStore) failedTargetCurrent(ctx context.Context, tx driver.Tx, target ManagedCleanupTarget, revision int64) error {
	if target.Kind != ManagedCleanupAbandonFailed || !target.IdentityCurrent || len(target.Unresolved) > 0 || len(target.Accepted) == 0 {
		return ErrManagedFailedCleanupConflict
	}
	if err := s.guard(ctx, tx, target, revision); err != nil {
		return err
	}
	scope := target.Scope
	var safe bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=$1 AND ((b.connection_id=$2 AND b.external_id=$3) OR (b.environment_id=$4 AND b.subject='service' AND b.name=$5)))
 AND NOT EXISTS(SELECT 1 FROM loomplane_workload_attempts w WHERE w.org_id=$1 AND w.environment_id=$4 AND w.service=$5 AND w.active AND NOT w.settled AND w.deployment_id<>$6)
 AND NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=$1 AND a.release_id<>$7 AND a.body->>'ClusterCA'=$8 AND a.body->>'NamespaceUID'=$9 AND a.body->>'Instance'=$3)
 AND NOT EXISTS(SELECT 1 FROM loomplane_workload_controllers c WHERE c.org_id=$1 AND c.instance_id=$3 AND (c.connection_id=$2 OR (c.body->>'cluster_ca'=$8 AND c.body->>'namespace_uid'=$9)) AND c.body->>'release_id' IS DISTINCT FROM $7)`, scope.Org, scope.Connection, scope.Instance, scope.Environment, scope.Service, scope.Deployment, scope.Release, scope.ClusterCA, scope.NamespaceUID).Scan(&safe)
	if err != nil || !safe {
		return errors.Join(ErrManagedFailedCleanupConflict, err)
	}
	return nil
}

func (s *ManagedCleanupStore) AcceptFailedCleanup(ctx context.Context, p security.Principal, deployment, digest string) (_ *ManagedFailedCleanup, err error) {
	if caller, ok := security.PrincipalFrom(ctx); !ok || caller != p {
		return nil, security.ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = rollback(tx, err) }()
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, p.OrgID); checkErr != nil {
		return nil, checkErr
	}
	// Exact acceptance retries retain the original actor and review.
	existing, e := scanManagedFailed(tx.QueryRow(ctx, `SELECT `+managedFailedColumns+` FROM loomplane_managed_failed_cleanups WHERE org_id=$1 AND deployment_id=$2 AND superseded_at IS NULL FOR UPDATE`, p.OrgID, deployment))
	if e == nil && existing.Digest == digest {
		if existing.Actor != p || existing.RevokedAt != nil {
			return nil, ErrManagedFailedCleanupConflict
		}
		if checkErr := s.actor(ctx, p, p.OrgID, existing.Targets[0].Scope.Project); checkErr != nil {
			return nil, checkErr
		}
		return existing, tx.Commit()
	}
	if e != nil && !errors.Is(e, ErrNotFound) {
		return nil, e
	}
	out, err := s.previewFailed(ctx, tx, p, deployment)
	if err != nil {
		return nil, err
	}
	if digest == "" || out.Digest != digest {
		return nil, ErrManagedFailedCleanupConflict
	}
	if e == nil {
		if existing.Status == "pending" {
			return nil, ErrManagedFailedCleanupConflict
		}
		var drained bool
		if checkErr := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w JOIN loomplane_managed_cleanup_grants g ON g.org_id=w.org_id AND g.id=w.cleanup_grant_id WHERE g.org_id=$1 AND g.failed_review_id=$2 AND w.outcome IS NULL)`, p.OrgID, existing.ID).Scan(&drained); checkErr != nil {
			return nil, checkErr
		}
		if !drained {
			return nil, ErrManagedFailedCleanupConflict
		}
	}
	for _, target := range out.Targets {
		if checkErr := s.failedTargetCurrent(ctx, tx, target, target.AuthorityRevision); checkErr != nil {
			return nil, checkErr
		}
	}
	if e == nil {
		if _, err = tx.Exec(ctx, `UPDATE loomplane_managed_failed_cleanups SET superseded_at=clock_timestamp(),revoked_at=coalesce(revoked_at,clock_timestamp()),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, p.OrgID, existing.ID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE loomplane_managed_cleanup_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()),revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND failed_review_id=$2`, p.OrgID, existing.ID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_deployments SET status='cancelled',finished_at=coalesce(finished_at,clock_timestamp()) WHERE org_id=$1 AND id=$2`, p.OrgID, deployment); err != nil {
		return nil, err
	}
	actor, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	targets, err := json.Marshal(out.Targets)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('loomplane.capacity_actor',$1,true)`, string(actor)); err != nil {
		return nil, err
	}
	id, err := typeid.Generate("cleanupreview")
	if err != nil {
		return nil, err
	}
	out.ID = id.String()
	out.Actor = p
	out.Status = "pending"
	out.Attempt = 1
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_failed_cleanups(id,org_id,deployment_id,sequence,predecessor_id,review_digest,actor,targets) VALUES($1,$2,$3,$4,nullif($5,''),$6,$7,$8)`, out.ID, p.OrgID, deployment, out.Sequence, out.Predecessor, out.Digest, actor, targets); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// HasFailedCleanupEvidence selects the managed branch before SQL-specific checks.
func (s *ManagedCleanupStore) HasFailedCleanupEvidence(ctx context.Context, org, deployment string) (bool, error) {
	var found bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_workload_authority WHERE org_id=$1 AND body->>'Deployment'=$2) OR EXISTS(SELECT 1 FROM loomplane_managed_failed_cleanups WHERE org_id=$1 AND deployment_id=$2)`, org, deployment).Scan(&found)
	return found, err
}
