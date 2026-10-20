package migrations

// A plan remembers the branch each repository is read from, for targets
// that build a branch rather than a commit. Plans made before this read
// "", the default branch, which is what they were analysed from.
var planSourceRefsUp = []string{
	`ALTER TABLE loomplane_plan_sources ADD COLUMN IF NOT EXISTS ref TEXT NOT NULL DEFAULT ''`,
}

var planSourceRefsDown = []string{
	`ALTER TABLE loomplane_plan_sources DROP COLUMN IF EXISTS ref`,
}
