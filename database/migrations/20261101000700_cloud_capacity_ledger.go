package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	IF (NEW.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at','absence_confirmed_at']) IS DISTINCT FROM
		(OLD.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at','absence_confirmed_at'])
		OR NEW.principal IS DISTINCT FROM OLD.principal
	THEN RAISE EXCEPTION 'infrastructure intent is immutable' USING ERRCODE='23514'; END IF;

	IF OLD.body->>'attempted'='true' AND NEW.body->>'attempted'<>'true'
	THEN RAISE EXCEPTION 'infrastructure attempt cannot be cleared' USING ERRCODE='23514'; END IF;

	IF coalesce(OLD.body->>'resource_id','')<>'' AND NEW.body->>'resource_id' IS DISTINCT FROM OLD.body->>'resource_id'
	THEN RAISE EXCEPTION 'infrastructure identity is immutable' USING ERRCODE='23514'; END IF;

	IF coalesce(OLD.body->>'host_key','')<>'' AND NEW.body->>'host_key' IS DISTINCT FROM OLD.body->>'host_key'
	THEN RAISE EXCEPTION 'enrolled host key is immutable' USING ERRCODE='23514'; END IF;

	IF coalesce(NEW.body->>'absence_confirmed_at','')<>coalesce(OLD.body->>'absence_confirmed_at','')
		AND (NEW.body->'input'->>'kind'<>'delete' OR coalesce(NEW.body->>'resource_id','')='')
	THEN RAISE EXCEPTION 'absence receipt requires a delete identity' USING ERRCODE='23514'; END IF;

	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_infrastructure_intent_guard: %w", err)
			}

			err = db.NewAlterTable("loomplane_cloud_capacity_reservations").
				AddColumn("cleanup_confirmed_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_cloud_capacity_reservations_account").
				On("loomplane_cloud_capacity_reservations").
				Columns("org_id").
				ColumnExpr("body->>'provider'").
				ColumnExpr("body->>'account_id'").
				Where("status='reserved'").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	IF (NEW.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at']) IS DISTINCT FROM
		(OLD.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at'])
		OR NEW.principal IS DISTINCT FROM OLD.principal
	THEN RAISE EXCEPTION 'infrastructure intent is immutable' USING ERRCODE='23514'; END IF;

	IF OLD.body->>'attempted'='true' AND NEW.body->>'attempted'<>'true'
	THEN RAISE EXCEPTION 'infrastructure attempt cannot be cleared' USING ERRCODE='23514'; END IF;

	IF coalesce(OLD.body->>'resource_id','')<>'' AND NEW.body->>'resource_id' IS DISTINCT FROM OLD.body->>'resource_id'
	THEN RAISE EXCEPTION 'infrastructure identity is immutable' USING ERRCODE='23514'; END IF;

	IF coalesce(OLD.body->>'host_key','')<>'' AND NEW.body->>'host_key' IS DISTINCT FROM OLD.body->>'host_key'
	THEN RAISE EXCEPTION 'enrolled host key is immutable' USING ERRCODE='23514'; END IF;

	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_infrastructure_intent_guard: %w", err)
			}

			err = db.NewDropIndex("loomplane_cloud_capacity_reservations_account").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_cloud_capacity_reservations").
				DropColumn("cleanup_confirmed_at").
				Exec(ctx)
		},
	)
}
