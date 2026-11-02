package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	nativeCredentials.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_native_credential_fence() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $native_credential_fence$
DECLARE
	saved record;
BEGIN
	IF NEW.app_id<>'loomplane-resource-credentials' OR position('/native-copy:' in NEW.key)=0 THEN
		RETURN NEW;
	END IF;

	SELECT r.* INTO saved FROM public.loomplane_native_restores r WHERE r.id=NEW.metadata->>'native_restore_id' FOR SHARE;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'native credential has no current restore owner' USING ERRCODE='55000';
	END IF;

	IF saved.org_id IS DISTINCT FROM NEW.metadata->>'native_org_id'
		OR saved.environment_id IS DISTINCT FROM NEW.metadata->>'native_environment_id'
		OR encode(sha256(convert_to(saved.token,'UTF8')),'hex') IS DISTINCT FROM NEW.metadata->>'native_worker_token_hash'
		OR COALESCE(NEW.metadata->>'native_worker_token_hash','')=''
		OR saved.status<>'running' OR saved.lease_until IS NULL OR saved.lease_until<=clock_timestamp() OR saved.expires_at<=clock_timestamp()
		OR NEW.key IS DISTINCT FROM saved.org_id||'/'||saved.environment_id||'/native-copy:'||saved.id||'/credentials'
	THEN
		RAISE EXCEPTION 'native credential worker is fenced' USING ERRCODE='55000';
	END IF;

	RETURN NEW;
END $native_credential_fence$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_native_credential_fence: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_native_credential_fence
	BEFORE INSERT OR UPDATE ON vault_secrets
	FOR EACH ROW EXECUTE FUNCTION loomplane_native_credential_fence()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_native_credential_fence: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_credential_fence_down$
BEGIN
	LOCK TABLE loomplane_native_restores IN ACCESS EXCLUSIVE MODE;
	IF EXISTS(SELECT 1 FROM loomplane_native_restores)
		OR EXISTS(SELECT 1 FROM vault_secrets WHERE app_id='loomplane-resource-credentials' AND position('/native-copy:' in key)>0)
	THEN
		RAISE EXCEPTION 'native credential history is still retained' USING ERRCODE='55000';
	END IF;

	DROP TRIGGER IF EXISTS loomplane_native_credential_fence ON vault_secrets;
END $native_credential_fence_down$`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_native_credential_fence: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_native_credential_fence()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_native_credential_fence: %w", err)
			}
			return nil
		},
	)
}
