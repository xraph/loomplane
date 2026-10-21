package access

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/grove/driver"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// Input reads one consistent generation and only rules involving the selected
// scope. Quarantined destinations contribute no authority. Historical references
// remain retained until a separate cleanup proves old leases are no longer usable.
func (s *PolicyStore) Input(ctx context.Context, scope domain.Scope) (input domain.PolicyInput, err error) {
	if !scope.Valid() {
		return input, ErrPolicy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return input, err
	}
	defer func() { err = finishPolicyTx(tx, err) }()
	var exists bool
	if stepErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND id=$3)`, scope.OrgID, scope.ProjectID, scope.EnvironmentID).Scan(&exists); stepErr != nil {
		return input, stepErr
	}
	if !exists {
		return input, ErrPolicyNotFound
	}
	if stepErr := lockPolicyOrganization(ctx, tx, scope.OrgID); stepErr != nil {
		return input, stepErr
	}
	input.EvaluatedAt = time.Now().UTC()
	if stepErr := expirePolicyGrants(ctx, tx, scope.OrgID, input.EvaluatedAt); stepErr != nil {
		return input, stepErr
	}
	input, err = policyInputTx(ctx, tx, scope, input.EvaluatedAt)
	if err != nil {
		return domain.PolicyInput{}, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return domain.PolicyInput{}, commitErr
	}
	return input, nil
}

// policyInputTx reads the same durable rules for compilation and final signing.
// Its caller holds the organization policy lock and has materialized expiry.
func policyInputTx(ctx context.Context, tx driver.Tx, scope domain.Scope, evaluatedAt time.Time) (input domain.PolicyInput, err error) {
	input.EvaluatedAt = evaluatedAt
	input.Scope = scope
	if stepErr := tx.QueryRow(ctx, `SELECT COALESCE((SELECT generation FROM loomplane_federation_policy_versions WHERE org_id=$1),1)`, scope.OrgID).Scan(&input.Generation); stepErr != nil {
		return input, stepErr
	}
	exports, err := policyRecords[ExportRecord](ctx, tx, `SELECT e.state FROM loomplane_federation_exports e WHERE e.org_id=$1
 AND (e.project_id=$2 AND e.environment_id=$3 OR EXISTS(SELECT 1 FROM loomplane_federation_grants g WHERE g.org_id=e.org_id AND g.export_id=e.id AND g.source_project_id=$2 AND g.source_environment_id=$3))
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(e.state->'destination_ids') selected(id)
 JOIN loomplane_federation_destinations d ON d.org_id=e.org_id AND d.project_id=e.project_id AND d.environment_id=e.environment_id AND d.id=selected.id WHERE d.quarantined)
 ORDER BY e.id LIMIT $4`, scope)
	if err != nil {
		return input, err
	}
	for _, export := range exports {
		if export.RetiredAt == nil {
			input.Exports = append(input.Exports, export.ServiceExport)
		}
	}
	grants, err := policyRecords[GrantRecord](ctx, tx, `SELECT g.state FROM loomplane_federation_grants g JOIN loomplane_federation_destinations d
 ON d.org_id=g.org_id AND d.project_id=g.source_project_id AND d.environment_id=g.source_environment_id AND d.id=g.source_destination_id
 WHERE g.org_id=$1 AND NOT d.quarantined AND (g.project_id=$2 AND g.environment_id=$3 OR g.source_project_id=$2 AND g.source_environment_id=$3)
 ORDER BY g.id LIMIT $4`, scope)
	if err != nil {
		return input, err
	}
	if len(exports)+len(grants) > domain.MaxSnapshotRecords {
		return input, ErrPolicy
	}
	for _, grant := range grants {
		input.Grants = append(input.Grants, grant.AccessGrant)
	}
	return input, nil
}

func policyRecords[T any](ctx context.Context, tx driver.Tx, query string, scope domain.Scope) ([]T, error) {
	rows, err := tx.Query(ctx, query, scope.OrgID, scope.ProjectID, scope.EnvironmentID, domain.MaxSnapshotRecords+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []T
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var record T
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(records) > domain.MaxSnapshotRecords {
		return nil, ErrPolicy
	}
	return records, nil
}

// Expiry is a policy transition, so it advances the generation before readers
// compile different rules. The evaluation timestamp stays pinned through signing.
func expirePolicyGrants(ctx context.Context, tx driver.Tx, org string, now time.Time) error {
	rows, err := tx.Query(ctx, `SELECT state FROM loomplane_federation_grants WHERE org_id=$1
 AND coalesce(state->>'approved_revision','')<>'' AND (state->>'revoked_at') IS NULL
 AND (state->>'expires_at')::timestamptz <= $2 ORDER BY id LIMIT $3`, org, now, domain.MaxSnapshotRecords+1)
	if err != nil {
		return err
	}
	var expired []*GrantRecord
	for rows.Next() {
		record, decodeErr := readPolicyRecord[GrantRecord](rows)
		if decodeErr != nil {
			rows.Close()
			return decodeErr
		}
		expired = append(expired, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(expired) > domain.MaxSnapshotRecords {
		return ErrPolicy
	}
	for _, record := range expired {
		if !validExpected(record.Generation) {
			return ErrPolicyConflict
		}
		record.Generation++
		record.UpdatedAt = now
		record.RevokedAt = &now
		record.RevokedBy = "system"
		record.ApprovedRevision = ""
		if err := saveGrant(ctx, tx, record, "system", "grant.expire"); err != nil {
			return err
		}
	}
	return nil
}
