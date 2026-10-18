package security

var (
	BackupRead    = Permission{"service_backup", "read"}
	BackupManage  = Permission{"service_backup", "manage"}
	RestoreManage = Permission{"service_restore", "manage"}
)
