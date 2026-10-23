package migrations

import "strings"

// Reference writes may run while deployment admission holds a shared lock.
// A shared lock still excludes the updates and deletion retirement needs.
const retiringServerUseShared = `CREATE OR REPLACE FUNCTION loomplane_retiring_server_use() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.kind IN ('cloud_server','infrastructure_operation') THEN RETURN NEW; END IF;
 PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id FOR SHARE;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND runtime_connection_id=NEW.connection_id AND status NOT IN ('ready','deleted','disconnected')) THEN RAISE EXCEPTION 'server is not available' USING ERRCODE='23503'; END IF;
 RETURN NEW; END $$`

var connectionUseAdmissionUp = []string{retiringServerUseShared}
var connectionUseAdmissionDown = []string{strings.Replace(retiringServerUseShared, "FOR SHARE", "FOR UPDATE", 1)}
