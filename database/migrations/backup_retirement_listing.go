package migrations

var backupRetirementListingUp = []string{
	`CREATE INDEX loomplane_backup_retirements_project_history ON loomplane_backup_retirements(org_id,project_id,created_at DESC,id DESC)`,
}

var backupRetirementListingDown = []string{
	`DROP INDEX loomplane_backup_retirements_project_history`,
}
