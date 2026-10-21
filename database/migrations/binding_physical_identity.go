package migrations

var bindingPhysicalIdentityUp = []string{
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS physical_id TEXT NOT NULL DEFAULT ''`,
}

var bindingPhysicalIdentityDown = []string{
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS physical_id`,
}
