package projects

import (
	"context"

	"github.com/xraph/grove/driver"
)

// EnvironmentOwnershipReader supports both admission transactions and fresh
// worker checks. Callers retain their existing organization and workload locks.
type EnvironmentOwnershipReader interface {
	QueryRow(context.Context, string, ...any) driver.Row
}

// GuardUnmanagedEnvironment rejects generic mutations of a fleet environment.
// Environment IDs are unique within an organization; a different project ID
// must not make an owned environment appear unmanaged.
func GuardUnmanagedEnvironment(ctx context.Context, db EnvironmentOwnershipReader, org, environment string) error {
	var managed bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_fleet_instances WHERE org_id=$1 AND environment_id=$2)`, org, environment).Scan(&managed); err != nil {
		return err
	}
	if managed {
		return ErrFleetManaged
	}
	return nil
}
