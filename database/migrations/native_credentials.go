package migrations

import "github.com/xraph/grove/migrate"

// NativeCredentialsGroup runs after both owners have created their tables.
func NativeCredentialsGroup() *migrate.Group {
	g := migrate.NewGroup("loomplane_native_credentials", migrate.DependsOn(GroupName, "vault"))
	g.MustRegister(statements("20261101000400", "native_credential_fence", nativeCredentialFenceUp, nativeCredentialFenceDown))
	return g
}

var nativeCredentialFenceUp = []string{
	`CREATE OR REPLACE FUNCTION loomplane_native_credential_fence() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $native_credential_fence$
 DECLARE saved record;
 BEGIN
 IF NEW.app_id<>'loomplane-resource-credentials' OR position('/native-copy:' in NEW.key)=0 THEN RETURN NEW; END IF;
 SELECT r.* INTO saved FROM public.loomplane_native_restores r WHERE r.id=NEW.metadata->>'native_restore_id' FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'native credential has no current restore owner' USING ERRCODE='55000'; END IF;
 IF saved.org_id IS DISTINCT FROM NEW.metadata->>'native_org_id'
 OR saved.environment_id IS DISTINCT FROM NEW.metadata->>'native_environment_id'
 OR encode(sha256(convert_to(saved.token,'UTF8')),'hex') IS DISTINCT FROM NEW.metadata->>'native_worker_token_hash'
 OR COALESCE(NEW.metadata->>'native_worker_token_hash','')=''
 OR saved.status<>'running' OR saved.lease_until IS NULL OR saved.lease_until<=clock_timestamp() OR saved.expires_at<=clock_timestamp()
 OR NEW.key IS DISTINCT FROM saved.org_id||'/'||saved.environment_id||'/native-copy:'||saved.id||'/credentials'
 THEN RAISE EXCEPTION 'native credential worker is fenced' USING ERRCODE='55000'; END IF;
 RETURN NEW;
 END $native_credential_fence$`,
	`CREATE OR REPLACE TRIGGER loomplane_native_credential_fence BEFORE INSERT OR UPDATE ON vault_secrets FOR EACH ROW EXECUTE FUNCTION loomplane_native_credential_fence()`,
}

var nativeCredentialFenceDown = []string{
	`DO $native_credential_fence_down$ BEGIN
 LOCK TABLE loomplane_native_restores IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_native_restores) OR EXISTS(SELECT 1 FROM vault_secrets WHERE app_id='loomplane-resource-credentials' AND position('/native-copy:' in key)>0) THEN RAISE EXCEPTION 'native credential history is still retained' USING ERRCODE='55000'; END IF;
 DROP TRIGGER IF EXISTS loomplane_native_credential_fence ON vault_secrets;
 END $native_credential_fence_down$`,
	`DROP FUNCTION IF EXISTS loomplane_native_credential_fence()`,
}
