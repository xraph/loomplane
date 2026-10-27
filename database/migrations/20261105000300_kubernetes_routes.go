package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Routes and their operations record which backend serves them. A Kubernetes
// route holds a ready Kubernetes connection where a server route holds a
// ready Cloud server. Every other rule is the cloud_domains one, unchanged.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_domain_routes").
				AddColumn("backend", migrate.Text, migrate.NotNull(), migrate.Default("server")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_route_operations").
				AddColumn("backend", migrate.Text, migrate.NotNull(), migrate.Default("server")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
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
	IF NOT FOUND THEN RAISE EXCEPTION 'route scope missing' USING ERRCODE='23503'; END IF;
	PERFORM 1 FROM loomplane_dns_zones WHERE org_id=NEW.org_id AND id=NEW.zone_id FOR KEY SHARE;
	IF NOT FOUND THEN RAISE EXCEPTION 'route zone missing' USING ERRCODE='23503'; END IF;
	IF NEW.backend='kubernetes' THEN
		PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id AND cloud='kubernetes' AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route connection unavailable' USING ERRCODE='23503'; END IF;
	ELSE
		PERFORM 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.server_id AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route server unavailable' USING ERRCODE='23503'; END IF;
	END IF;

	DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
	INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_route',NEW.id) ON CONFLICT DO NOTHING;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_domain_scope: %w", err)
			}
			return nil
		},
		nil,
	)
}
