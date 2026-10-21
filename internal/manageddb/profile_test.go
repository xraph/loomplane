package manageddb

import "testing"

func TestPostgresProfilesDescribeProviderRequests(t *testing.T) {
	for _, target := range []string{"rds", "cloud-sql"} {
		t.Run(target, func(t *testing.T) {
			profile, err := Postgres(target, "17")
			if err != nil {
				t.Fatal(err)
			}
			if profile.ID != "postgresql-17-"+target || profile.Revision != "1" || profile.Provider != target || profile.Kind != "postgres" || profile.Version != "17" || !profile.StorageEncrypted || profile.Availability != "single-zone" || profile.PointInTimeRecoveryDays != 7 {
				t.Fatalf("incomplete managed request profile: %+v", profile)
			}
			if target == "rds" {
				if profile.ComputeClass != "db.t4g.micro" || profile.StorageGB != 20 || profile.StorageType != "gp3" || profile.Access != "private-vpc" || profile.BackupRetentionValue != 7 || profile.BackupRetentionUnit != "days" || profile.FinalBackup != "snapshot" || profile.FinalBackupRetentionDays != 0 || !profile.RetainAutomatedBackups {
					t.Fatalf("RDS request profile: %+v", profile)
				}
			} else if profile.ComputeClass != "db-custom-1-3840" || profile.StorageGB != 10 || profile.StorageType != "PD_SSD" || profile.Access != "cloud-sql-connector" || profile.BackupRetentionValue != 7 || profile.BackupRetentionUnit != "count" || profile.FinalBackup != "final-backup" || profile.FinalBackupRetentionDays != 30 || profile.RetainAutomatedBackups {
				t.Fatalf("Cloud SQL request profile: %+v", profile)
			}
			profile.ComputeClass = "changed"
			next, err := Postgres(target, "17")
			if err != nil || next.ComputeClass == "changed" {
				t.Fatal("a caller mutated the shared request profile")
			}
		})
	}
	for _, target := range []string{"server", "kubernetes", "unknown"} {
		if _, err := Postgres(target, "17"); err == nil {
			t.Fatalf("unsupported managed target %s has a profile", target)
		}
	}
	if _, err := Postgres("rds", "16"); err == nil {
		t.Fatal("an unqualified managed catalog version has a profile")
	}
}
