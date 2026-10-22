package federation

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
)

// ReadPreparationStatus reads all lifecycle evidence from one read-only MVCC
// snapshot. No status poll issues credentials, renews a lease or resumes work.
func (s *PgStore) ReadPreparationStatus(ctx context.Context, org, destination, id string) (out *PreparationStatus, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &driver.TxOptions{IsolationLevel: driver.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		err = finishInstallation(tx, err)
		if err != nil {
			out = nil
		}
	}()
	observedAt := time.Now().UTC()
	evidence := preparationEvidence{}
	evidence.preparation, err = scanPreparation(tx.QueryRow(ctx, `SELECT `+preparationColumns+` FROM loomplane_federation_preparations WHERE org_id=$1 AND destination_id=$2 AND id=$3`, org, destination, id))
	if err != nil {
		return nil, err
	}
	evidence.destination, err = scanDestination(tx.QueryRow(ctx, `SELECT `+columns+` FROM loomplane_federation_destinations WHERE org_id=$1 AND id=$2`, org, destination))
	if err != nil {
		return nil, err
	}
	if evidence.preparation.OrgID != org || evidence.preparation.ID != id || evidence.preparation.Scope != evidence.destination.Scope {
		return nil, ErrInvalid
	}
	evidence.installation, err = scanInstallation(tx.QueryRow(ctx, `SELECT `+installationColumns+` FROM loomplane_federation_installations WHERE org_id=$1 AND destination_id=$2`, org, destination), org, destination)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	evidence.launch, err = scanLaunch(tx.QueryRow(ctx, `SELECT `+launchColumns+` FROM loomplane_federation_launch_attempts WHERE org_id=$1 AND id=$2`, org, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if receipt := evidence.preparation.EnrollmentID; receipt != "" {
		evidence.enrollment, evidence.phase, err = workloadidentity.ReadEnrollmentTx(ctx, tx, org, receipt)
		if err != nil && !errors.Is(err, workloadidentity.ErrIdentity) {
			return nil, err
		}
	}
	if evidence.enrollment != nil {
		if readErr := tx.QueryRow(ctx, `SELECT COALESCE((SELECT epoch FROM loomplane_federation_enrollment_epochs WHERE org_id=$1 AND destination_id=$2),0)`, org, destination).Scan(&evidence.epoch); readErr != nil {
			return nil, readErr
		}
		if evidence.phase == "consumed" {
			evidence.delivery, err = access.ReadDeliveryTx(ctx, tx, evidence.enrollment.Binding.Agent)
			switch {
			case err == nil, errors.Is(err, access.ErrPolicyNotFound):
				evidence.active = true
			case errors.Is(err, runtimecatalog.ErrUnauthorized):
			default:
				return nil, err
			}
			if readErr := tx.QueryRow(ctx, `SELECT COALESCE((SELECT generation FROM loomplane_federation_policy_versions WHERE org_id=$1),1)`, org).Scan(&evidence.policyGeneration); readErr != nil {
				return nil, readErr
			}
		}
	}
	out = evidence.status(time.Now())
	out.ObservedAt = observedAt
	return out, tx.Commit()
}
