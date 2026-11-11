package currentauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/grove/driver"
	keyid "github.com/xraph/keysmith/id"

	"github.com/xraph/loomplane/internal/security"
)

func TestFleetPostgresCurrentKeyAndTypedLifecycle(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	ctx := t.Context()
	b := FleetBinding(f.binding)
	b.Actor.KeyID = keyid.NewKeyID().String()
	b.Actor.Kind = security.KindFleet
	b.Actor.UserID = security.FleetSubject(b.Actor.KeyID)
	b.Actor.Fleet = &security.FleetScope{ProjectID: f.project, FleetID: "fleet"}
	execute := func(q string, args ...any) {
		t.Helper()
		if _, err := f.main.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	metadata, _ := json.Marshal(map[string]string{"kind": string(security.KindFleet), "access": "write", "project_id": f.project, "fleet_id": "fleet"})
	execute(`INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at) VALUES($1,$2,$3,'Fleet','lp','hint','hash',$4,$5,now()+interval '1 hour')`, b.Actor.KeyID, b.Actor.OrgID, b.AppID, f.binding.Actor.UserID, string(metadata))
	copied := *f.c.profile
	copied.fleet = &fleetProfile{appID: b.AppID}
	f.c.profile = &copied
	run := func(change func(context.Context, *Attempt, *Decision, FleetBinding) error) error {
		t.Helper()
		in := b
		in.RecordID = fmt.Sprintf("fleet-candidate-%d", time.Now().UnixNano())
		in.AttemptID = in.RecordID
		err := f.c.RunFleet(ctx, in, func(ctx context.Context, a *Attempt) error {
			d, err := a.EvaluateFleet(ctx)
			if err != nil {
				return err
			}
			if change != nil {
				return change(ctx, a, d, in)
			}
			return a.StageFleet(ctx, d, in, func(ctx context.Context, tx driver.Tx, at time.Time) error {
				_, err := tx.Exec(ctx, `INSERT INTO authority_candidates(id,authorized_at) VALUES($1,$2)`, in.RecordID, at)
				return err
			})
		})
		var count int
		if checkErr := f.main.QueryRow(ctx, `SELECT count(*) FROM authority_candidates WHERE id=$1`, in.RecordID).Scan(&count); checkErr != nil {
			t.Fatal(checkErr)
		}
		if err != nil && count != 0 {
			t.Fatal("failed run published candidate")
		}
		if change == nil && err == nil && count != 1 {
			t.Fatal("successful run lost candidate")
		}
		return err
	}
	if err := run(nil); err != nil {
		t.Fatal(err)
	}
	t.Run("creator membership is audit provenance", func(t *testing.T) {
		execute(`DELETE FROM authsome_members WHERE user_id=$1`, f.binding.Actor.UserID)
		execute(`UPDATE authsome_users SET banned=true,email_verified=false WHERE id=$1`, f.binding.Actor.UserID)
		if err := run(nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ordinary user boundary does not expire service authority", func(t *testing.T) {
		boundary := time.Now().UTC()
		execute(`UPDATE authsome_users SET banned=true,ban_expires=$2 WHERE id=$1`, f.binding.Actor.UserID, boundary)
		defer execute(`UPDATE authsome_users SET ban_expires=NULL WHERE id=$1`, f.binding.Actor.UserID)
		original := f.c.now
		defer func() { f.c.now = original }()
		calls := 0
		f.c.now = func() time.Time {
			calls++
			if calls == 1 {
				return boundary.Add(-time.Second)
			}
			return boundary.Add(time.Second)
		}
		if err := run(nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("persisted scopes deny", func(t *testing.T) {
		scope := keyid.NewScopeID().String()
		execute(`INSERT INTO keysmith_scopes(id,tenant_id,app_id,name) VALUES($1,$2,$3,'scope')`, scope, b.Actor.OrgID, b.AppID)
		execute(`INSERT INTO keysmith_key_scopes(key_id,scope_id) VALUES($1,$2)`, b.Actor.KeyID, scope)
		if err := run(nil); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("scoped key=%v", err)
		}
		execute(`DELETE FROM keysmith_key_scopes WHERE key_id=$1`, b.Actor.KeyID)
	})
	for _, tc := range []struct{ name, change, restore string }{
		{"revocation", `revoked_at=now()`, `revoked_at=NULL`},
		{"expired", `expires_at=now()-interval '1 second'`, `expires_at=now()+interval '1 hour'`},
		{"missing expiry", `expires_at=NULL`, `expires_at=now()+interval '1 hour'`},
		{"read only", `metadata=jsonb_set(metadata,'{access}','"read"')`, `metadata=jsonb_set(metadata,'{access}','"write"')`},
		{"numeric metadata", `metadata=jsonb_set(metadata,'{fleet_id}','42')`, `metadata=jsonb_set(metadata,'{fleet_id}','"fleet"')`},
		{"different fleet", `metadata=jsonb_set(metadata,'{fleet_id}','"other"')`, `metadata=jsonb_set(metadata,'{fleet_id}','"fleet"')`},
		{"missing creator", `created_by=NULL`, `created_by='historical-creator'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execute(`UPDATE keysmith_keys SET `+tc.change+` WHERE id=$1`, b.Actor.KeyID)
			defer execute(`UPDATE keysmith_keys SET `+tc.restore+` WHERE id=$1`, b.Actor.KeyID)
			if err := run(nil); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("key check=%v", err)
			}
		})
	}

	t.Run("queued key revocation is read after the fence", func(t *testing.T) {
		config, err := pgxpool.ParseConfig(f.dsn)
		if err != nil {
			t.Fatal(err)
		}
		connection, err := pgx.ConnectConfig(ctx, config.ConnConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close(context.Background())
		tx, err := connection.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(ctx, `UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, b.Actor.KeyID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			done <- f.c.RunFleet(ctx, b, func(ctx context.Context, a *Attempt) error {
				d, err := a.EvaluateFleet(ctx)
				if err != nil {
					return err
				}
				return a.StageFleet(ctx, d, b, writeNothing)
			})
		}()
		wait, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		for {
			var queued bool
			if err := f.main.QueryRow(wait, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_locks WHERE relation=$1::pg_catalog.regclass AND mode='ShareLock' AND NOT granted)`, pgx.Identifier{f.schema, "keysmith_keys"}.Sanitize()).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			if queued {
				break
			}
			select {
			case <-wait.Done():
				t.Fatal("authority did not wait for key writer")
			case <-time.After(5 * time.Millisecond):
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("queued revoke=%v", err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("authority did not drain")
		}
		execute(`UPDATE keysmith_keys SET revoked_at=NULL WHERE id=$1`, b.Actor.KeyID)
	})
	t.Run("cross-kind stage poisons even when swallowed", func(t *testing.T) {
		err := run(func(ctx context.Context, a *Attempt, d *Decision, in FleetBinding) error {
			_ = a.Stage(ctx, d, f.binding, writeNothing)
			return a.StageFleet(ctx, d, in, writeNothing)
		})
		if !errors.Is(err, ErrBinding) {
			t.Fatalf("stage=%v", err)
		}
	})
	t.Run("generic evaluation cannot run installed callback", func(t *testing.T) {
		err := f.c.RunFleet(ctx, b, func(ctx context.Context, a *Attempt) error {
			_, err := a.Evaluate(ctx, func(context.Context) error { t.Fatal("generic evaluator ran"); return nil })
			return err
		})
		if !errors.Is(err, ErrDecision) {
			t.Fatalf("evaluate=%v", err)
		}
	})
	t.Run("ordinary evaluator refuses fleet", func(t *testing.T) {
		err := f.c.RunFleet(ctx, b, func(ctx context.Context, a *Attempt) error {
			_, err := a.EvaluateOrdinary(ctx, security.ProjectRead, f.project)
			return err
		})
		if !errors.Is(err, ErrBinding) {
			t.Fatalf("evaluate=%v", err)
		}
	})
	t.Run("fleet-only profile refuses ordinary before callback", func(t *testing.T) {
		fleetOnly := *f.c.profile
		fleetOnly.ordinary = nil
		f.c.profile = &fleetOnly
		err := f.c.Run(ctx, f.binding, func(context.Context, *Attempt) error { t.Fatal("ordinary callback ran"); return nil })
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("ordinary=%v", err)
		}
		if err := run(nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("public scope mutation cannot alter source or attempt", func(t *testing.T) {
		original := *b.Actor.Fleet
		defer func() { *b.Actor.Fleet = original }()
		source := f.c.source
		defer func() { f.c.source = source }()
		f.c.source = func(ctx context.Context, tx driver.Tx, owned Binding) error {
			b.Actor.Fleet.FleetID = "changed"
			if owned.Actor.Fleet.FleetID != "fleet" {
				t.Fatal("source retained input pointer")
			}
			owned.Actor.Fleet.ProjectID = "changed"
			return source(ctx, tx, owned)
		}
		err := f.c.RunFleet(ctx, b, func(ctx context.Context, a *Attempt) error {
			if a.binding.fleet != "fleet" || a.binding.project != f.project {
				t.Fatal("attempt identity mutated")
			}
			d, err := a.EvaluateFleet(ctx)
			if err != nil {
				return err
			}
			durable := FleetBinding(publicBinding(a.binding))
			return a.StageFleet(ctx, d, durable, writeNothing)
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
