package workloadidentity

import (
	"context"

	"github.com/xraph/grove/driver"
)

// Ready loads the current unconsumed enrollment for trusted launch delivery. It
// holds the row, destination and epoch until the read commits. Callers must check
// again before publication; reading a record does not reserve its future use.
func (s *EnrollmentStore) Ready(ctx context.Context, org, id string) (record *EnrollmentRecord, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = enrollmentFinish(tx, err) }()
	record, phase, err := loadEnrollment(ctx, tx, org, id)
	if err != nil {
		return nil, err
	}
	if checkErr := s.checkReady(ctx, tx, record, phase); checkErr != nil {
		return nil, checkErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return record, nil
}

func (s *EnrollmentStore) checkReady(ctx context.Context, tx driver.Tx, r *EnrollmentRecord, phase string) error {
	if phase != "ready" || !r.ExpiresAt.After(s.now()) {
		return ErrIdentity
	}
	if err := s.checkDestination(ctx, tx, r.Binding); err != nil {
		return err
	}
	return s.checkEpoch(ctx, tx, r)
}
func (s *EnrollmentStore) checkEpoch(ctx context.Context, tx driver.Tx, r *EnrollmentRecord) error {
	var epoch uint64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM loomplane_federation_enrollment_epochs WHERE org_id=$1 AND destination_id=$2 FOR SHARE`, r.Binding.Agent.OrgID, r.Binding.Agent.DestinationID).Scan(&epoch); err != nil {
		return enrollmentError(err)
	}
	if epoch != r.Binding.Agent.Epoch || !r.ExpiresAt.After(s.now()) {
		return ErrIdentity
	}
	return nil
}
