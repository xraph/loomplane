package migrations

var catalogPlansUp = []string{`ALTER TABLE loomplane_plans ADD COLUMN IF NOT EXISTS catalog BOOLEAN NOT NULL DEFAULT FALSE`}
var catalogPlansDown = []string{`ALTER TABLE loomplane_plans DROP COLUMN IF EXISTS catalog`}
