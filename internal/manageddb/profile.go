// Package manageddb owns the versioned requests for managed database profiles.
package manageddb

import (
	"fmt"

	"github.com/xraph/loomplane/pkg/provider"
)

// CloudSQLFinalBackupDays is the lifetime requested when removing Cloud SQL.
const CloudSQLFinalBackupDays = 30

// Defaults are shared by legacy adapters and the catalog's reviewed profiles.
// Existing bindings are not silently assigned a profile or reconfigured.
func Defaults(target string) (provider.ManagedDatabaseProfile, error) {
	p := provider.ManagedDatabaseProfile{
		Provider: target, StorageEncrypted: true, Availability: "single-zone",
		BackupRetentionValue: 7, PointInTimeRecoveryDays: 7,
	}
	switch target {
	case "rds":
		p.ComputeClass, p.StorageGB, p.StorageType = "db.t4g.micro", 20, "gp3"
		p.Access, p.BackupRetentionUnit, p.FinalBackup = "private-vpc", "days", "snapshot"
		p.RetainAutomatedBackups = true
	case "cloud-sql":
		p.ComputeClass, p.StorageGB, p.StorageType = "db-custom-1-3840", 10, "PD_SSD"
		p.Access, p.BackupRetentionUnit, p.FinalBackup = "cloud-sql-connector", "count", "final-backup"
		p.FinalBackupRetentionDays = CloudSQLFinalBackupDays
	default:
		return p, fmt.Errorf("%s has no managed database defaults", target)
	}
	return p, nil
}

// Postgres returns a fresh copy of the supported managed request profile.
func Postgres(target, version string) (*provider.ManagedDatabaseProfile, error) {
	if version != "17" {
		return nil, fmt.Errorf("%s PostgreSQL %s has no managed catalog profile", target, version)
	}
	p, err := Defaults(target)
	if err != nil {
		return nil, err
	}
	p.ID, p.Revision, p.Kind, p.Version = "postgresql-17-"+target, "1", "postgres", version
	return &p, nil
}

// CloudSQLPSCPostgres is the opt-in profile for sources isolated at clone time.
// Selecting it requires an explicit manual endpoint in the source manifest.
func CloudSQLPSCPostgres(version string) (*provider.ManagedDatabaseProfile, error) {
	p, err := Postgres("cloud-sql", version)
	if err != nil {
		return nil, err
	}
	p.ID, p.Access = "postgresql-17-cloud-sql-psc", "cloud-sql-psc-connector"
	return p, nil
}

// PostgresSource selects an explicit source contract without changing defaults.
func PostgresSource(target, version, region string, endpoint CloudSQLSourceEndpoint) (*provider.ManagedDatabaseProfile, error) {
	if endpoint == (CloudSQLSourceEndpoint{}) {
		return Postgres(target, version)
	}
	if target != "cloud-sql" || !endpoint.Valid(region) {
		return nil, fmt.Errorf("cloud sql PSC requires a manual endpoint in the reviewed region")
	}
	return CloudSQLPSCPostgres(version)
}

// Profiles lists only managed profiles the standalone catalog can request.
func Profiles(target string) []provider.ManagedDatabaseProfile {
	p, err := Postgres(target, "17")
	if err != nil {
		return nil
	}
	if target == "cloud-sql" {
		psc, profileErr := CloudSQLPSCPostgres("17")
		if profileErr != nil {
			return nil
		}
		return []provider.ManagedDatabaseProfile{*p, *psc}
	}
	return []provider.ManagedDatabaseProfile{*p}
}
