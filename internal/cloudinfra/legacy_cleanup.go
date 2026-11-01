package cloudinfra

import (
	"context"

	"github.com/xraph/grove/driver"
)

// GuardLegacyCleanup stops new work on only the service slot or physical
// instance reached by an unresolved pre-upgrade cleanup.
func GuardLegacyCleanup(ctx context.Context, tx driver.Tx, org, project, environment, service, instance string) error {
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT loomplane_legacy_cleanup_blocked($1,$2,$3,$4,$5)`, org, project, environment, service, instance).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return &CapacityAdmissionError{Block: CapacityBlock{Reason: "historical_managed_cleanup_unresolved", Resource: "workload"}}
	}
	return nil
}
