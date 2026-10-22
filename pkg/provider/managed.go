package provider

// ManagedDatabaseProfile describes the requested provider configuration, not an
// observation or a guarantee that a native backup can be restored by Loomplane.
type ManagedDatabaseProfile struct {
	ID                       string `json:"id"`
	Revision                 string `json:"revision"`
	Provider                 string `json:"provider"`
	Kind                     string `json:"kind"`
	Version                  string `json:"version"`
	ComputeClass             string `json:"compute_class"`
	StorageGB                int32  `json:"storage_gb"` // GiB, as the provider allocates it.
	StorageType              string `json:"storage_type"`
	StorageEncrypted         bool   `json:"storage_encrypted"`
	Availability             string `json:"availability"`
	Access                   string `json:"access"`
	BackupRetentionValue     int32  `json:"backup_retention_value"`
	BackupRetentionUnit      string `json:"backup_retention_unit"`
	PointInTimeRecoveryDays  int32  `json:"point_in_time_recovery_days"`
	FinalBackup              string `json:"final_backup"`
	FinalBackupRetentionDays int32  `json:"final_backup_retention_days"`
	RetainAutomatedBackups   bool   `json:"retain_automated_backups"`
}
