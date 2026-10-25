package gcp

import (
	"context"
	"slices"

	"github.com/xraph/loomplane/internal/manageddb"
)

// EnsureSQLPITRSource creates a PSC-only source with manual consumer endpoints.
// Existing public sources are refused, never patched into this profile.
// A returned instance still needs endpoint, DNS and SQL verification.
func (s *Session) EnsureSQLPITRSource(ctx context.Context, spec SQLInstanceSpec, endpoint manageddb.CloudSQLSourceEndpoint, track SQLMutation) (*SQLInstance, error) {
	profile, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil || !endpoint.Valid(spec.Region) || !sqlNativeName(spec.Name) || len(spec.Name) > 63 || spec.DatabaseVersion != "POSTGRES_17" || spec.Tier != profile.ComputeClass || spec.Storage == nil || spec.Storage.SizeGB != profile.StorageGB || spec.Storage.Type != profile.StorageType || spec.Storage.RetainedBackups != profile.BackupRetentionValue || spec.Storage.RecoveryDays != profile.PointInTimeRecoveryDays || !sqlNativeName(spec.Intent) || len(spec.Intent) > 63 || spec.Inspect == nil || track.Check == nil {
		return nil, ErrSQLPITRUnavailable
	}
	if guardErr := track.check(ctx); guardErr != nil {
		return nil, guardErr
	}
	inspect := spec.Inspect
	spec.Inspect = func(in *SQLInstance) error {
		if guardErr := track.check(ctx); guardErr != nil {
			return guardErr
		}
		if _, pinErr := in.Incarnation(s.key.ProjectID, spec.Region, spec.Name); pinErr != nil {
			return pinErr
		}
		if !spec.Existing && (in.Settings.UserLabels[SQLIntentLabel] != spec.Intent || in.Settings.UserLabels["managed-by"] != "loomplane") {
			return ErrSQLPITRUnavailable
		}
		// Capture ownership before checking readiness so a failed provision can
		// still be reconciled or removed through its original checkpoint.
		if inspectErr := inspect(in); inspectErr != nil {
			return inspectErr
		}
		if !slices.Contains([]string{"RUNNABLE", "PENDING_CREATE", "MAINTENANCE"}, in.State) {
			return ErrSQLPITRUnavailable
		}
		if in.State != "RUNNABLE" {
			return nil
		}
		if _, isolationErr := in.PITRIsolation(spec); isolationErr != nil {
			return isolationErr
		}
		psc := in.Settings.IPConfiguration.PSC
		if len(psc.AllowedProjects) != 1 || psc.AllowedProjects[0] != endpoint.ConsumerProject || psc.AutoDNS {
			return ErrSQLPITRUnavailable
		}
		return track.check(ctx)
	}
	spec.pscConsumerProject = endpoint.ConsumerProject
	track.strictIdentity = true
	return s.EnsureSQLInstance(ctx, spec, track)
}
