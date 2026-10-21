package security

var (
	BackupRead    = Permission{"service_backup", "read"}
	BackupRetire  = Permission{"service_backup", "retire"}
	BackupManage  = Permission{"service_backup", "manage"}
	RestoreManage = Permission{"service_restore", "manage"}
)
