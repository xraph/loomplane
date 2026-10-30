package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"
)

func managedScopeValid(s ManagedScope) bool {
	return s.Epoch > 0 && s.Release != "" && s.Release == WorkloadReleaseID(s.RuntimeRef, s.RuntimeRevision) &&
		s.Org != "" && s.Project != "" && s.Environment != "" && s.Service != "" && s.Plan != "" && s.Deployment != "" && s.Action != "" && s.Connection != "" && s.Instance != "" && s.Namespace != "" && s.NamespaceUID != "" && len(s.ClusterCA) == 64 &&
		strings.HasPrefix(s.RuntimeRef, s.Org+"/"+s.Environment+"/"+s.Service+"/runtime_") &&
		(s.RuntimeRevision == s.Deployment+"/"+s.Action || strings.HasPrefix(s.RuntimeRevision, s.Deployment+"/"+s.Action+"/"))
}

// guardManagedScope runs under the organization lock, which orders grant and
// Cloud scope changes. Row locks also serialize attempt takeover and revocation.
func guardManagedScope(ctx context.Context, tx driver.Tx, s ManagedScope) error {
	if !managedScopeValid(s) {
		return ErrManagedAuthority
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT true FROM loomplane_plans p
 JOIN loomplane_cluster_workload_intents i ON i.org_id=p.org_id AND i.plan_id=p.id AND i.plan_revision=p.revision
 JOIN loomplane_workload_attempts w ON w.org_id=p.org_id AND w.project_id=p.project_id AND w.environment_id=p.environment_id AND w.service=i.service AND w.plan_id=p.id
 JOIN loomplane_deployments d ON d.org_id=p.org_id AND d.id=w.deployment_id AND d.plan_id=p.id AND d.status='running'
 JOIN loomplane_cloud_connections c ON c.org_id=p.org_id AND c.id=i.body->'input'->>'connection_id'
 WHERE p.org_id=$1 AND p.project_id=$2 AND p.environment_id=$3 AND p.id=$4 AND i.service=$5 AND w.deployment_id=$6 AND w.generation=$7
 AND w.active AND NOT w.settled AND NOT w.legacy_unfenced AND p.status='approved' AND p.required_runtime_protocol IN ('managed-workload-journal-v1','managed-cleanup-journal-v1')
 AND loomplane_cluster_workload_scope_current(p,i.body) AND i.body->'input'->>'action_id'=$8 AND c.id=$9
 AND c.details->>'namespace'=$10 AND c.details->>'namespace_uid'=$11 AND c.details->>'cluster_ca'=$12
 AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=p.org_id AND d.id=w.deployment_id AND d.status IN ('cancelled','paused'))
 AND (i.body->>'mode'='best_effort' OR (i.body->>'mode'='guaranteed' AND EXISTS(SELECT 1 FROM loomplane_cluster_workload_holds h WHERE h.org_id=p.org_id AND h.id=i.body->'input'->>'id' AND NOT h.released AND h.body->'attempt'->>'Generation'=$7::text AND h.body->'attempt'->>'Deployment'=$6)))
 FOR UPDATE OF w FOR SHARE OF p,i,c,d`, s.Org, s.Project, s.Environment, s.Plan, s.Service, s.Deployment, s.Epoch, s.Action, s.Connection, s.Namespace, s.NamespaceUID, s.ClusterCA).Scan(&valid)
	if err != nil || !valid {
		return errors.Join(ErrManagedAuthority, err)
	}
	return nil
}

// OpenManagedScope binds the saved runtime identity to the approved demand.
// A takeover may advance the epoch, but a revoked release can never reopen.
func (s *ControllerStore) OpenManagedScope(ctx context.Context, scope ManagedScope) error {
	if !s.capacityEnabled {
		return ErrManagedCapacityDisabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if checkErr := lockManagedOrganization(ctx, tx, scope.Org); checkErr != nil {
		return checkErr
	}
	if checkErr := guardManagedScope(ctx, tx, scope); checkErr != nil {
		return checkErr
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	var saved []byte
	var epoch int64
	err = tx.QueryRow(ctx, `SELECT body,epoch FROM loomplane_managed_workload_authority WHERE org_id=$1 AND release_id=$2 AND revoked_at IS NULL FOR UPDATE`, scope.Org, scope.Release).Scan(&saved, &epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_workload_authority(org_id,release_id,epoch,body) VALUES($1,$2,$3,$4)`, scope.Org, scope.Release, scope.Epoch, raw)
	} else if err == nil {
		var old ManagedScope
		if json.Unmarshal(saved, &old) != nil || epoch > scope.Epoch {
			return ErrManagedAuthority
		}
		old.Epoch = scope.Epoch
		if old != scope {
			return ErrManagedAuthority
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_managed_workload_authority SET epoch=$3,body=$4,revision=revision+1,updated_at=clock_timestamp() WHERE org_id=$1 AND release_id=$2`, scope.Org, scope.Release, scope.Epoch, raw)
	}
	if err != nil {
		return errors.Join(ErrManagedAuthority, err)
	}
	return tx.Commit()
}

func (s *ControllerStore) Issue(ctx context.Context, scope ManagedScope, request ManagedRequest) (string, error) {
	if !s.capacityEnabled {
		return "", ErrManagedCapacityDisabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if checkErr := lockManagedOrganization(ctx, tx, scope.Org); checkErr != nil {
		return "", checkErr
	}
	if checkErr := guardManagedScope(ctx, tx, scope); checkErr != nil {
		return "", checkErr
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT true FROM loomplane_managed_workload_authority WHERE org_id=$1 AND release_id=$2 AND epoch=$3 AND body=$4 AND revoked_at IS NULL FOR UPDATE`, scope.Org, scope.Release, scope.Epoch, raw).Scan(&current)
	if err != nil || !current {
		return "", errors.Join(ErrManagedAuthority, err)
	}
	if request.Namespace != scope.Namespace || request.Resource == "" || request.Name == "" {
		return "", ErrManagedAuthority
	}
	id, err := typeid.Generate("issuance")
	if err != nil {
		return "", err
	}
	req, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_workload_issuances(id,org_id,release_id,epoch,scope,request) VALUES($1,$2,$3,$4,$5,$6)`, id.String(), scope.Org, scope.Release, scope.Epoch, raw, req)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_workload_attempts SET effects_started=true WHERE org_id=$1 AND environment_id=$2 AND service=$3 AND generation=$4`, scope.Org, scope.Environment, scope.Service, scope.Epoch)
	if err != nil {
		return "", err
	}
	return id.String(), tx.Commit()
}

func (s *ControllerStore) Resolve(ctx context.Context, id string, scope ManagedScope, outcome ManagedOutcome) error {
	if outcome.State != "accepted" && outcome.State != "rejected" {
		return ErrManagedAuthority
	}
	if outcome.State == "accepted" && (outcome.UID == "" || outcome.StatusCode < 200 || outcome.StatusCode > 299) {
		return ErrManagedAuthority
	}
	if outcome.State == "rejected" {
		switch outcome.StatusCode {
		case 400, 401, 403, 404, 409, 422, 429:
		default:
			return ErrManagedAuthority
		}
		if outcome.UID != "" || outcome.ResourceVersion != "" || outcome.Generation != 0 {
			return ErrManagedAuthority
		}
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	result, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	var resolved string
	err = s.db.QueryRow(ctx, `UPDATE loomplane_managed_workload_issuances SET outcome=$4,revision=revision+1,updated_at=clock_timestamp(),resolved_at=coalesce(resolved_at,clock_timestamp()) WHERE id=$1 AND org_id=$2 AND scope=$3 AND (outcome IS NULL OR outcome=$4) AND ($5='' OR request->>'UID'='' OR request->>'UID'=$5) RETURNING id`, id, scope.Org, raw, result, outcome.UID).Scan(&resolved)
	return err
}

// RevokeManagedScope permanently closes application issuance for this exact
// release. Cleanup authorization is separate and must not reuse this scope.
func (s *ControllerStore) RevokeManagedScope(ctx context.Context, scope ManagedScope) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit
	if checkErr := lockManagedOrganization(ctx, tx, scope.Org); checkErr != nil {
		return checkErr
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	var revoked string
	err = tx.QueryRow(ctx, `UPDATE loomplane_managed_workload_authority SET revision=revision+1,updated_at=clock_timestamp(),revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE org_id=$1 AND release_id=$2 AND epoch=$3 AND body=$4 RETURNING release_id`, scope.Org, scope.Release, scope.Epoch, raw).Scan(&revoked)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ManagedIssuances returns safe historical outcomes across all attempt epochs.
// An unresolved entry remains unresolved after revocation and later successes.
func (s *ControllerStore) ManagedIssuances(ctx context.Context, org, release string) ([]ManagedIssuance, error) {
	rows, err := s.db.Query(ctx, `SELECT id,scope,request,outcome,issued_at,resolved_at,coalesce(cleanup_grant_id,''),coalesce(cleanup_grant_revision,0) FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND release_id=$2 ORDER BY issued_at,id`, org, release)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedIssuance{}
	for rows.Next() {
		var v ManagedIssuance
		var scope, request, outcome []byte
		if checkErr := rows.Scan(&v.ID, &scope, &request, &outcome, &v.IssuedAt, &v.ResolvedAt, &v.CleanupGrantID, &v.CleanupGrantRevision); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(scope, &v.Scope); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(request, &v.Request); checkErr != nil {
			return nil, checkErr
		}
		if outcome != nil {
			if checkErr := json.Unmarshal(outcome, &v.Outcome); checkErr != nil {
				return nil, checkErr
			}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// This is the Cloud organization gate. The runtime package cannot import
// cloudprofiles, whose connection checks depend on this package.
func lockManagedOrganization(ctx context.Context, tx driver.Tx, org string) error {
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='60s'`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cloud-organization/"+org)
	return err
}

// GetManagedAuthority reads the current epoch without granting mutation rights.
func (s *ControllerStore) GetManagedAuthority(ctx context.Context, org, release string) (*ManagedAuthority, error) {
	var out ManagedAuthority
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT body,schema_version,revision,created_at,updated_at,revoked_at FROM loomplane_managed_workload_authority WHERE org_id=$1 AND release_id=$2`, org, release).Scan(&raw, &out.SchemaVersion, &out.Revision, &out.CreatedAt, &out.UpdatedAt, &out.RevokedAt)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &out.Scope) != nil || out.Scope.Org != org || out.Scope.Release != release || out.SchemaVersion != 1 || !managedScopeValid(out.Scope) {
		return nil, ErrManagedAuthority
	}
	return &out, nil
}
