package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A create records the SSH port its server's bootstrap opened for
// Loomplane, since a host that can't connect out to port 22, such as
// DigitalOcean App Platform, uses another. The intent guard lets the port
// be recorded once, as it does the host key, and never changed after.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
	IF (NEW.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at','absence_confirmed_at','ssh_port']) IS DISTINCT FROM
		(OLD.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at','absence_confirmed_at','ssh_port'])
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

	IF coalesce(OLD.body->>'ssh_port','')<>'' AND NEW.body->>'ssh_port' IS DISTINCT FROM OLD.body->>'ssh_port'
	THEN RAISE EXCEPTION 'managed SSH port is immutable' USING ERRCODE='23514'; END IF;

	RETURN NEW;
END $$`); err != nil {
				return fmt.Errorf("let infrastructure operations record their SSH port: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_infrastructure_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
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
END $$`); err != nil {
				return fmt.Errorf("restore the infrastructure intent guard: %w", err)
			}
			return nil
		},
	)
}
