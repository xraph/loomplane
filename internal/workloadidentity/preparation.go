package workloadidentity

import (
	"context"
	"errors"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// RevokePreparation derives its account scope from a retained retirement record.
// It must not hold destination or preparation locks while revoking enrollment:
// an in-flight issuer may hold its enrollment lock while waiting on destination.
func (s *EnrollmentStore) RevokePreparation(ctx context.Context, org, destination, id string, issuer EnrollmentIssuer) error {
	if issuer == nil || !domain.ValidIdentitySegment(org) || !domain.ValidIdentitySegment(destination) || !domain.ValidIdentitySegment(id) {
		return ErrIdentity
	}
	var namespaceUID string
	if err := s.db.QueryRow(ctx, `SELECT intent->>'namespace_uid' FROM loomplane_federation_preparations WHERE org_id=$1 AND destination_id=$2 AND id=$3 AND state IN ('retiring','retired')`, org, destination, id).Scan(&namespaceUID); err != nil {
		return enrollmentError(err)
	}
	if !domain.ValidIdentitySegment(namespaceUID) {
		return ErrIdentity
	}
	account := "loomplane-agent-" + id
	ids, err := s.preparationEnrollmentIDs(ctx, org, destination, namespaceUID, account)
	if err != nil {
		return err
	}
	var failures error
	for _, enrollmentID := range ids {
		failures = errors.Join(failures, s.Revoke(ctx, org, enrollmentID, issuer))
	}
	remaining, err := s.preparationEnrollmentIDs(ctx, org, destination, namespaceUID, account)
	if err != nil {
		return errors.Join(failures, err)
	}
	if len(remaining) > 0 {
		return errors.Join(failures, ErrIdentity)
	}
	return failures
}
func (s *EnrollmentStore) preparationEnrollmentIDs(ctx context.Context, org, destination, namespaceUID, account string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM loomplane_federation_enrollments WHERE org_id=$1 AND destination_id=$2 AND body->'binding'->>'NamespaceUID'=$3 AND body->'binding'->>'ServiceAccount'=$4 ORDER BY id LIMIT 129`, org, destination, namespaceUID, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		ids = append(ids, id)
	}
	if readErr := rows.Err(); readErr != nil {
		return nil, readErr
	}
	if len(ids) > 128 {
		return nil, ErrIdentity
	}
	return ids, nil
}
