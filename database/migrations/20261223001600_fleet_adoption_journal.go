package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_transfer_journals").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("operation_id", migrate.Text, migrate.NotNull()).
			Column("source_key", migrate.Text, migrate.NotNull(), migrate.Check("length(source_key)=64")).
			Column("intent", migrate.JSON, migrate.NotNull(), migrate.Check("octet_length(intent::text)<=65536")).
			Column("intent_digest", migrate.Text, migrate.NotNull(), migrate.Check("length(intent_digest)=64")).
			Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
			Column("head_digest", migrate.Text, migrate.NotNull(), migrate.Check("length(head_digest)=64")).
			Unique("source_key").Unique("operation_id").
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_transfer_entries").
			Column("transfer_id", migrate.Text, migrate.NotNull()).
			Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>1")).
			Column("payload", migrate.JSON, migrate.NotNull(), migrate.Check("octet_length(payload::text)<=65536")).
			PrimaryKey("transfer_id", "revision").
			ForeignKey("transfer_id").References("loomplane_fleet_transfer_journals", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_transfer_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
   BEGIN
    IF TG_TABLE_NAME='loomplane_fleet_transfer_entries' OR TG_OP='DELETE' THEN
     RAISE EXCEPTION 'transfer history is immutable' USING ERRCODE='23514';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.org_id IS DISTINCT FROM OLD.org_id
     OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.fleet_id IS DISTINCT FROM OLD.fleet_id
     OR NEW.operation_id IS DISTINCT FROM OLD.operation_id OR NEW.source_key IS DISTINCT FROM OLD.source_key
     OR NEW.intent::text IS DISTINCT FROM OLD.intent::text OR NEW.intent_digest IS DISTINCT FROM OLD.intent_digest
     OR NEW.revision<>OLD.revision+1 THEN
     RAISE EXCEPTION 'transfer identity is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
   END $$;
   CREATE TRIGGER fleet_transfer_identity BEFORE UPDATE OR DELETE ON loomplane_fleet_transfer_journals FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_transfer_immutable();
   CREATE TRIGGER fleet_transfer_entry_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_transfer_entries FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_transfer_immutable();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		// Refuse rollback while evidence exists, including canceled transfers.
		if _, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_fleet_transfer_journals) THEN RAISE EXCEPTION 'retained transfer journals prevent rollback'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_transfer_entries").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_transfer_journals").Exec(ctx); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_transfer_immutable()`)
		return err
	})
}
