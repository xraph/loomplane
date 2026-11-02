package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_dns_zones").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("revision", migrate.BigInt, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Unique("org_id", "name").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_domain_routes").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("server_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("zone_id", migrate.Text, migrate.NotNull()).
				Column("hostname", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.BigInt, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_hostname_claim").IfNotExists().Unique().
				On("loomplane_domain_routes").
				Columns("hostname").
				Where("status<>'removed'").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_route_operations").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("route_id", migrate.Text, migrate.NotNull()).
				Column("server_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.BigInt, migrate.NotNull()).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("principal", migrate.JSON, migrate.NotNull()).
				Column("checkpoint", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_active_route_operation").IfNotExists().Unique().
				On("loomplane_route_operations").
				Columns("route_id").
				Where("status NOT IN ('preview','succeeded','cancelled')").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_ingress_configs").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("server_id", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("body", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				PrimaryKey("org_id", "server_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_route_handoffs").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("route_id", migrate.Text, migrate.NotNull()).
				Column("source_connection_id", migrate.Text, migrate.NotNull()).
				Column("destination_connection_id", migrate.Text, migrate.NotNull()).
				Column("released", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_active_route_handoff").IfNotExists().Unique().
				On("loomplane_route_handoffs").
				Columns("route_id").
				Where("NOT released").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_route_operation_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	DELETE FROM loomplane_connection_uses WHERE kind='route_operation' AND owner_id=NEW.id;
	IF NEW.status NOT IN ('preview','succeeded','cancelled') THEN
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_operation',NEW.id) ON CONFLICT DO NOTHING;
	END IF;

	IF TG_OP='INSERT' AND NEW.body->>'kind'='prepare' THEN
		INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES(NEW.id,NEW.org_id,NEW.route_id,NEW.body->'source'->'binding'->>'connection_id',NEW.body->'route'->'binding'->>'connection_id');
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'source'->'binding'->>'connection_id','route_handoff',NEW.id),(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_handoff',NEW.id) ON CONFLICT DO NOTHING;
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_route_operation_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_route_operation_uses
AFTER INSERT OR UPDATE ON loomplane_route_operations
FOR EACH ROW EXECUTE FUNCTION loomplane_route_operation_uses()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_route_operation_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_route_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.principal IS DISTINCT FROM OLD.principal
		OR (NEW.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt']) IS DISTINCT FROM (OLD.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt'])
		OR ((NEW.body->'route') - ARRAY['status','operation_id','receipt','verification','revision','updated_at']) IS DISTINCT FROM ((OLD.body->'route') - ARRAY['status','operation_id','receipt','verification','revision','updated_at'])
	THEN
		RAISE EXCEPTION 'route intent is immutable' USING ERRCODE='23514';
	END IF;
	IF OLD.body->>'dns_attempted'='true' AND NEW.body->>'dns_attempted'<>'true' THEN
		RAISE EXCEPTION 'DNS attempt cannot be cleared' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_route_intent_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_route_intent
BEFORE UPDATE ON loomplane_route_operations
FOR EACH ROW EXECUTE FUNCTION loomplane_route_intent_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_route_intent: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_domain_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_TABLE_NAME='loomplane_dns_zones' THEN
		IF NEW.connection_id<>'' THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'dns_zone',NEW.id) ON CONFLICT DO NOTHING;
		END IF;
		RETURN NEW;
	END IF;

	IF NEW.status='removed' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
		RETURN NEW;
	END IF;

	PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND id=NEW.environment_id FOR KEY SHARE;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'route scope missing' USING ERRCODE='23503';
	END IF;
	PERFORM 1 FROM loomplane_dns_zones WHERE org_id=NEW.org_id AND id=NEW.zone_id FOR KEY SHARE;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'route zone missing' USING ERRCODE='23503';
	END IF;
	PERFORM 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.server_id AND status='ready' FOR KEY SHARE;
	IF NOT FOUND THEN
		RAISE EXCEPTION 'route server unavailable' USING ERRCODE='23503';
	END IF;

	DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
	INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_route',NEW.id) ON CONFLICT DO NOTHING;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_domain_scope: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_domain_route_scope
BEFORE INSERT OR UPDATE ON loomplane_domain_routes
FOR EACH ROW EXECUTE FUNCTION loomplane_domain_scope()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_domain_route_scope: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_dns_zone_scope
AFTER INSERT OR UPDATE ON loomplane_dns_zones
FOR EACH ROW EXECUTE FUNCTION loomplane_domain_scope()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_dns_zone_scope: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_domain_delete_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_TABLE_NAME='loomplane_cloud_profiles' THEN
		IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status<>'removed')
			OR EXISTS(SELECT 1 FROM loomplane_route_operations WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status NOT IN ('preview','succeeded','cancelled'))
		THEN
			RAISE EXCEPTION 'Cloud has domain claims or operations' USING ERRCODE='23503';
		END IF;
	ELSIF TG_TABLE_NAME='loomplane_projects' THEN
		IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND project_id=OLD.id AND status<>'removed') THEN
			RAISE EXCEPTION 'remove project routes first' USING ERRCODE='23503';
		END IF;
	ELSE
		IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND zone_id=OLD.id AND status<>'removed') THEN
			RAISE EXCEPTION 'zone still has claims' USING ERRCODE='23503';
		END IF;
		DELETE FROM loomplane_connection_uses WHERE kind='dns_zone' AND owner_id=OLD.id;
	END IF;
	RETURN OLD;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_domain_delete_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_domain_cloud_delete
BEFORE DELETE ON loomplane_cloud_profiles
FOR EACH ROW EXECUTE FUNCTION loomplane_domain_delete_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_domain_cloud_delete: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_domain_project_delete
BEFORE DELETE ON loomplane_projects
FOR EACH ROW EXECUTE FUNCTION loomplane_domain_delete_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_domain_project_delete: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_domain_zone_delete
BEFORE DELETE ON loomplane_dns_zones
FOR EACH ROW EXECUTE FUNCTION loomplane_domain_delete_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_domain_zone_delete: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_domain_cloud_delete ON loomplane_cloud_profiles`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_domain_cloud_delete: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_domain_project_delete ON loomplane_projects`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_domain_project_delete: %w", err)
			}

			err = db.NewDropTable("loomplane_route_handoffs").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_route_operations").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_ingress_configs").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_domain_routes").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_dns_zones").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_domain_scope()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_domain_scope: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_domain_delete_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_domain_delete_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_route_intent_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_route_intent_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_route_operation_uses()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_route_operation_uses: %w", err)
			}
			return nil
		},
	)
}
