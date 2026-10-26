package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// cloudInfrastructureRetiringServerUseSQL is the connection use guard this
// migration installs. cloud_infrastructure_review_guards narrows it and puts
// it back on the way down.
const cloudInfrastructureRetiringServerUseSQL = `CREATE OR REPLACE FUNCTION loomplane_retiring_server_use() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.kind IN ('cloud_server','infrastructure_operation') THEN RETURN NEW; END IF;

	PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id FOR UPDATE;
	IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND runtime_connection_id=NEW.connection_id AND status<>'ready') THEN
		RAISE EXCEPTION 'server is retiring' USING ERRCODE='23503';
	END IF;

	RETURN NEW;
END $$`

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_cloud_servers").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("runtime_connection_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("account_id", migrate.Text, migrate.NotNull()).
				Column("resource_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("ownership", migrate.Text, migrate.NotNull(), migrate.Check("ownership IN ('managed','adopted')")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_cloud_server_resource").IfNotExists().Unique().
				On("loomplane_cloud_servers").
				Columns("org_id", "provider", "account_id", "resource_id").
				Where("resource_id<>'' AND status NOT IN ('deleted','disconnected')").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_cloud_server_runtime").IfNotExists().Unique().
				On("loomplane_cloud_servers").
				Columns("org_id", "runtime_connection_id").
				Where("runtime_connection_id<>'' AND status NOT IN ('deleted','disconnected')").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_infrastructure_operations").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("server_id", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("principal", migrate.JSON, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_infrastructure_active_server").IfNotExists().Unique().
				On("loomplane_infrastructure_operations").
				Columns("org_id", "server_id").
				Where("status NOT IN ('preview','succeeded','cancelled')").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF (NEW.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at']) IS DISTINCT FROM
		(OLD.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at'])
		OR NEW.principal IS DISTINCT FROM OLD.principal
	THEN
		RAISE EXCEPTION 'infrastructure intent is immutable' USING ERRCODE='23514';
	END IF;
	IF OLD.body->>'attempted'='true' AND NEW.body->>'attempted'<>'true' THEN
		RAISE EXCEPTION 'infrastructure attempt cannot be cleared' USING ERRCODE='23514';
	END IF;
	IF coalesce(OLD.body->>'resource_id','')<>'' AND NEW.body->>'resource_id' IS DISTINCT FROM OLD.body->>'resource_id' THEN
		RAISE EXCEPTION 'infrastructure identity is immutable' USING ERRCODE='23514';
	END IF;
	IF coalesce(OLD.body->>'host_key','')<>'' AND NEW.body->>'host_key' IS DISTINCT FROM OLD.body->>'host_key' THEN
		RAISE EXCEPTION 'enrolled host key is immutable' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_infrastructure_intent_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_infrastructure_intent ON loomplane_infrastructure_operations`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_infrastructure_intent: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE TRIGGER loomplane_infrastructure_intent
BEFORE UPDATE ON loomplane_infrastructure_operations
FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_intent_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_infrastructure_intent: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_connection_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_TABLE_NAME='loomplane_cloud_servers' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='cloud_server' AND owner_id=NEW.id;
		IF NEW.status NOT IN ('deleted','disconnected') THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'cloud_server',NEW.id) ON CONFLICT DO NOTHING;
			IF NEW.runtime_connection_id<>'' THEN
				INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.runtime_connection_id,'cloud_server',NEW.id) ON CONFLICT DO NOTHING;
			END IF;
		END IF;
	ELSE
		DELETE FROM loomplane_connection_uses WHERE kind='infrastructure_operation' AND owner_id=NEW.id;
		IF NEW.status NOT IN ('preview','succeeded','cancelled') THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->>'connection_id','infrastructure_operation',NEW.id);
		END IF;
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_infrastructure_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_server_connection_uses
AFTER INSERT OR UPDATE ON loomplane_cloud_servers
FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_connection_uses()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_server_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_operation_connection_uses
AFTER INSERT OR UPDATE ON loomplane_infrastructure_operations
FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_connection_uses()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_operation_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, cloudInfrastructureRetiringServerUseSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_retiring_server_use: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_retiring_server_use
BEFORE INSERT ON loomplane_connection_uses
FOR EACH ROW EXECUTE FUNCTION loomplane_retiring_server_use()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_retiring_server_use: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_cloud_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=NEW.org_id AND id=NEW.cloud_id FOR KEY SHARE;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'Cloud scope missing' USING ERRCODE='23503';
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_infrastructure_cloud_scope: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_infrastructure_cloud_scope
BEFORE INSERT ON loomplane_infrastructure_operations
FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_cloud_scope()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_infrastructure_cloud_scope: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_server_cloud_scope
BEFORE INSERT ON loomplane_cloud_servers
FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_cloud_scope()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_server_cloud_scope: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_cloud_delete() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status NOT IN ('deleted','disconnected'))
		OR EXISTS(SELECT 1 FROM loomplane_infrastructure_operations WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status NOT IN ('preview','succeeded','cancelled'))
	THEN
		RAISE EXCEPTION 'Cloud still owns infrastructure' USING ERRCODE='23503';
	END IF;
	RETURN OLD;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_infrastructure_cloud_delete: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_infrastructure_cloud_delete
BEFORE DELETE ON loomplane_cloud_profiles
FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_cloud_delete()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_infrastructure_cloud_delete: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_infrastructure_cloud_delete ON loomplane_cloud_profiles`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_infrastructure_cloud_delete: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_infrastructure_cloud_delete()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_infrastructure_cloud_delete: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_retiring_server_use ON loomplane_connection_uses`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_retiring_server_use: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_retiring_server_use()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_retiring_server_use: %w", err)
			}

			err = db.NewDropTable("loomplane_infrastructure_operations").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_cloud_servers").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_infrastructure_intent_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_infrastructure_intent_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_infrastructure_connection_uses()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_infrastructure_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_infrastructure_cloud_scope()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_infrastructure_cloud_scope: %w", err)
			}
			return nil
		},
	)
}
