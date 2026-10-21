package migrations

var federationPreparedEnrollmentUp = atomicFederationDDL([]string{
	`ALTER TABLE loomplane_federation_preparations ADD COLUMN IF NOT EXISTS enrollment_id TEXT NOT NULL DEFAULT ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS federation_preparation_enrollment ON loomplane_federation_preparations(org_id,enrollment_id) WHERE enrollment_id<>''`,
})
var federationPreparedEnrollmentDown = atomicFederationDDL([]string{
	`LOCK TABLE loomplane_federation_preparations IN ACCESS EXCLUSIVE MODE`,
	`IF EXISTS(SELECT 1 FROM loomplane_federation_preparations WHERE enrollment_id<>'') THEN RAISE EXCEPTION 'preparation enrollment receipts are still retained' USING ERRCODE='55000'; END IF`,
	`DROP INDEX IF EXISTS federation_preparation_enrollment`,
	`ALTER TABLE loomplane_federation_preparations DROP COLUMN IF EXISTS enrollment_id`,
})
