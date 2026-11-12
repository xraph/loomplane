package currentauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	authid "github.com/xraph/authsome/id"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/observability"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/warden"

	"github.com/xraph/loomplane/internal/security"
)

func ordinaryInstallationPGFixture(t *testing.T) *ordinaryPGFixture {
	t.Helper()
	f := newOrdinaryPGFixture(t)
	// Keep the migrated database and bootstrapped identity, but retire the
	// fixture's private authority before exercising the public installer.
	if err := f.c.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

type ordinaryInstallationPG struct {
	fixture     *ordinaryPGFixture
	coordinator *Coordinator
	engine      *warden.Engine
	authorizer  *security.Authorizer
	ordinary    *OrdinaryAuthority
	fleet       *FleetAuthority
	dsn         string
	application string
}

func newOrdinaryInstallationPG(t *testing.T, f *ordinaryPGFixture) *ordinaryInstallationPG {
	t.Helper()
	c := &Coordinator{}
	router := NewStoreRouter(c, f.store)
	engine, err := warden.NewEngine(warden.WithStore(router), warden.WithCache(NewFreshCache(c, warden.NewMemoryCache())))
	if err != nil {
		t.Fatal(err)
	}
	application := fmt.Sprintf("ordinary-install-%d", time.Now().UnixNano())
	dsn, err := withDSNParams(f.dsn, map[string]string{"application_name": application})
	if err != nil {
		t.Fatal("build isolated installation connection settings")
	}
	candidate := &ordinaryInstallationPG{
		fixture: f, coordinator: c, engine: engine,
		authorizer: security.NewAuthorizer(engine, router, nil),
		ordinary:   c.OrdinaryAuthority(), fleet: c.FleetAuthority(),
		dsn: dsn, application: application,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), CleanupLimit)
		defer cancel()
		if err := c.Shutdown(ctx); err != nil {
			t.Error(err)
			return
		}
		if err := engine.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return candidate
}

func (f *ordinaryInstallationPG) install(t *testing.T, authorizer *security.Authorizer, requireEmail bool) (InstallationReport, error) {
	t.Helper()
	return InstallBuiltInProfiles(t.Context(), f.coordinator, f.fixture.main, f.engine, authorizer, f.dsn, f.fixture.binding.AppID, requireEmail, observability.NewTelemetry("postgresql"), nil)
}

func (f *ordinaryInstallationPG) assertPools(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		var got int
		if err := f.fixture.main.QueryRow(t.Context(), `SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE application_name=$1`, f.application).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("installed sessions=%d, want %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *ordinaryInstallationPG) run(t *testing.T, input Binding) error {
	t.Helper()
	input.RecordID = fmt.Sprintf("installed-ordinary-%d", time.Now().UnixNano())
	input.AttemptID = input.RecordID
	err := f.ordinary.RunOrdinary(t.Context(), input, func(ctx context.Context, a *Attempt) error {
		d, err := a.EvaluateOrdinaryAll(ctx, []OrdinaryRequirement{
			{Permission: security.ProjectRead, ResourceID: f.fixture.project},
			{Permission: security.DeploymentRead, ResourceID: f.fixture.project},
		})
		if err != nil {
			return err
		}
		return a.Stage(ctx, d, input, func(ctx context.Context, tx driver.Tx, at time.Time) error {
			_, err := tx.Exec(ctx, `INSERT INTO authority_candidates(id,authorized_at) VALUES($1,$2)`, input.RecordID, at)
			return err
		})
	})
	assertOrdinaryInstallationCandidate(t, f.fixture, input.RecordID, err)
	return err
}

func (f *ordinaryInstallationPG) runFleet(t *testing.T) error {
	t.Helper()
	b := FleetBinding(f.fixture.binding)
	b.RecordID = fmt.Sprintf("installed-fleet-%d", time.Now().UnixNano())
	b.AttemptID = b.RecordID
	b.Actor.Kind = security.KindFleet
	b.Actor.KeyID = keyid.NewKeyID().String()
	b.Actor.UserID = security.FleetSubject(b.Actor.KeyID)
	b.Actor.Fleet = &security.FleetScope{ProjectID: f.fixture.project, FleetID: "installed-fleet"}
	metadata, marshalErr := json.Marshal(map[string]string{"kind": string(security.KindFleet), "access": "write", "project_id": f.fixture.project, "fleet_id": "installed-fleet"})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := f.fixture.main.Exec(t.Context(), `INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at) VALUES($1,$2,$3,'Installed Fleet','lp','hint',$1,$4,$5,now()+interval '1 hour')`, b.Actor.KeyID, b.Actor.OrgID, b.AppID, f.fixture.binding.Actor.UserID, string(metadata)); err != nil {
		t.Fatal(err)
	}
	runErr := f.fleet.RunFleet(t.Context(), b, func(ctx context.Context, a *Attempt) error {
		d, err := a.EvaluateFleet(ctx)
		if err != nil {
			return err
		}
		return a.StageFleet(ctx, d, b, func(ctx context.Context, tx driver.Tx, at time.Time) error {
			_, err := tx.Exec(ctx, `INSERT INTO authority_candidates(id,authorized_at) VALUES($1,$2)`, b.RecordID, at)
			return err
		})
	})
	assertOrdinaryInstallationCandidate(t, f.fixture, b.RecordID, runErr)
	return runErr
}

func assertOrdinaryInstallationCandidate(t *testing.T, f *ordinaryPGFixture, record string, result error) {
	t.Helper()
	var count int
	if err := f.main.QueryRow(t.Context(), `SELECT count(*) FROM authority_candidates WHERE id=$1`, record).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if (result == nil && count != 1) || (result != nil && count != 0) {
		t.Fatalf("candidate rows=%d, success=%v", count, result == nil)
	}
}

func (f *ordinaryInstallationPG) assertSchema(t *testing.T, main *pgdriver.PgDB, app, want string) {
	t.Helper()
	schema, err := f.ordinary.OrdinarySchemaForApp(main, app)
	if schema != want || (want == "" && !errors.Is(err, ErrUnavailable)) || (want != "" && err != nil) {
		t.Fatalf("ordinary schema=%q want=%q err=%v", schema, want, err)
	}
}

func TestOrdinaryInstallationPostgresTypedLifecycle(t *testing.T) {
	fixture := ordinaryInstallationPGFixture(t)
	f := newOrdinaryInstallationPG(t, fixture)
	work := func(context.Context, *Attempt) error { t.Fatal("unavailable callback ran"); return nil }
	f.assertSchema(t, fixture.main, fixture.binding.AppID, "")
	if err := f.ordinary.RunOrdinary(t.Context(), fixture.binding, work); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	report, installErr := f.install(t, f.authorizer, true)
	if installErr != nil || report.Ordinary != ProfileReady || report.Fleet != ProfileReady {
		t.Fatalf("joint installation report=%+v err=%v", report, installErr)
	}
	f.assertPools(t, 2)
	f.assertSchema(t, fixture.main, fixture.binding.AppID, fixture.schema)
	f.assertSchema(t, fixture.main, authid.NewAppID().String(), "")
	other := pgdriver.New()
	if err := other.Open(t.Context(), fixture.dsn, driver.WithPoolSize(1)); err != nil {
		t.Fatal("open second test database handle")
	}
	t.Cleanup(func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	})
	f.assertSchema(t, other, fixture.binding.AppID, "")
	f.assertPools(t, 2)
	installation, profile := f.coordinator.installation, f.coordinator.profile
	if installation == nil || installation.backend != f.coordinator.backend || installation.main != fixture.main || installation.engine != f.engine || installation.owner != fixture.role || profile.ordinary.migrationRole != fixture.role {
		t.Fatal("installation lost shared ownership")
	}
	if err := f.run(t, fixture.binding); err != nil {
		t.Fatal(err)
	}
	if err := f.runFleet(t); err != nil {
		t.Fatal(err)
	}
	keyBinding := fixture.binding
	keyBinding.Actor.KeyID = keyid.NewKeyID().String()
	if _, err := fixture.main.Exec(t.Context(), `INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at) VALUES($1,$2,$3,'Installed ordinary','lp','hint',$1,$4,'{"access":"write"}',now()+interval '1 hour')`, keyBinding.Actor.KeyID, keyBinding.Actor.OrgID, keyBinding.AppID, keyBinding.Actor.UserID); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t, keyBinding); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.main.Exec(t.Context(), `UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, keyBinding.Actor.KeyID); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t, keyBinding); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked exact key=%v", err)
	}
	wrongApp := fixture.binding
	wrongApp.AppID = authid.NewAppID().String()
	if err := f.run(t, wrongApp); !errors.Is(err, ErrBinding) {
		t.Fatalf("wrong app=%v", err)
	}
	if err := f.ordinary.RunOrdinary(t.Context(), Binding(fleetTestBinding()), work); !errors.Is(err, ErrBinding) {
		t.Fatalf("wrong kind=%v", err)
	}
	evaluationErr := f.ordinary.RunOrdinary(t.Context(), fixture.binding, func(ctx context.Context, a *Attempt) error {
		_, err := a.Evaluate(ctx, func(context.Context) error { t.Fatal("generic evaluator ran"); return nil })
		return err
	})
	if !errors.Is(evaluationErr, ErrDecision) {
		t.Fatalf("installed generic evaluation=%v", evaluationErr)
	}
	if _, err := f.install(t, f.authorizer, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("reinstallation=%v", err)
	}
	if f.coordinator.installation != installation || f.coordinator.profile != profile || !profile.ordinary.requireEmail {
		t.Fatal("reinstallation mutated the published profile")
	}
	f.assertPools(t, 2)
	if err := f.authorizer.Require(t.Context(), fixture.binding.Actor, security.ProjectRead, fixture.project); err != nil {
		t.Fatalf("normal identity after installation: %v", err)
	}
	// Hold an admitted attempt so shutdown can stop admission before close.
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- f.ordinary.RunOrdinary(t.Context(), fixture.binding, func(ctx context.Context, _ *Attempt) error {
			close(entered)
			<-release
			return ctx.Err()
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("ordinary attempt did not start: %v", err)
	case <-time.After(AttemptLimit):
		t.Fatal("ordinary attempt did not start")
	}
	stop, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.coordinator.Shutdown(stop); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown with active attempt=%v", err)
	}
	f.assertSchema(t, fixture.main, fixture.binding.AppID, "")
	f.assertPools(t, 2)
	close(release)
	if err := f.coordinator.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("drained ordinary attempt=%v", err)
	}
	f.assertSchema(t, fixture.main, fixture.binding.AppID, "")
	f.assertPools(t, 0)
	if err := fixture.main.Ping(t.Context()); err != nil {
		t.Fatalf("main database after ordinary shutdown: %v", err)
	}
	if err := f.ordinary.RunOrdinary(t.Context(), fixture.binding, work); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := f.fleet.RunFleet(t.Context(), fleetTestBinding(), work); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := f.authorizer.Require(t.Context(), fixture.binding.Actor, security.ProjectRead, fixture.project); err != nil {
		t.Fatalf("normal identity after authority shutdown: %v", err)
	}
}

func TestOrdinaryInstallationPostgresIndependentKinds(t *testing.T) {
	fixture := ordinaryInstallationPGFixture(t)
	for _, tc := range []struct {
		name            string
		authorizer      string
		breakFleet      bool
		ordinary, fleet ProfileStatus
	}{
		{"nil authorizer leaves Fleet", "nil", false, ProfileUnavailable, ProfileReady},
		{"different engine leaves Fleet", "engine", false, ProfileUnavailable, ProfileReady},
		{"different store leaves Fleet", "store", false, ProfileUnavailable, ProfileReady},
		{"Fleet schema mismatch leaves ordinary", "canonical", true, ProfileReady, ProfileUnavailable},
		{"neither kind qualifies", "nil", true, ProfileUnavailable, ProfileUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrdinaryInstallationPG(t, fixture)
			authorizer := f.authorizer
			switch tc.authorizer {
			case "nil":
				authorizer = nil
			case "engine":
				authorizer = security.NewAuthorizer(fixture.engine, f.engine.Store(), nil)
			case "store":
				authorizer = security.NewAuthorizer(f.engine, fixture.store, nil)
			}
			if tc.breakFleet {
				table := pgx.Identifier{fixture.schema, "loomplane_fleet_namespaces"}.Sanitize()
				if _, err := fixture.main.Exec(t.Context(), `CREATE TRIGGER ordinary_install_unreviewed AFTER INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger('fleet_namespace')`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := fixture.main.Exec(context.Background(), `DROP TRIGGER ordinary_install_unreviewed ON `+table); err != nil {
						t.Error(err)
					}
				})
			}
			report, err := f.install(t, authorizer, true)
			if report.Ordinary != tc.ordinary || report.Fleet != tc.fleet {
				t.Fatalf("readiness=%+v", report)
			}
			neither := tc.ordinary != ProfileReady && tc.fleet != ProfileReady
			if neither {
				if !errors.Is(err, ErrUnavailable) || f.coordinator.installation != nil || f.coordinator.profile != nil || f.coordinator.backend != nil {
					t.Fatalf("no-kind installation retained authority: %v", err)
				}
				f.assertPools(t, 0)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				f.assertPools(t, 2)
			}
			if tc.ordinary == ProfileReady {
				f.assertSchema(t, fixture.main, fixture.binding.AppID, fixture.schema)
				f.assertPools(t, 2)
			} else {
				f.assertSchema(t, fixture.main, fixture.binding.AppID, "")
			}
			ordinaryErr := f.run(t, fixture.binding)
			if (ordinaryErr == nil) != (tc.ordinary == ProfileReady) || (tc.ordinary != ProfileReady && !errors.Is(ordinaryErr, ErrUnavailable)) {
				t.Fatalf("ordinary result=%v", ordinaryErr)
			}
			fleetErr := f.runFleet(t)
			if (fleetErr == nil) != (tc.fleet == ProfileReady) || (tc.fleet != ProfileReady && !errors.Is(fleetErr, ErrUnavailable)) {
				t.Fatalf("Fleet result=%v", fleetErr)
			}
		})
	}
}

func TestOrdinaryInstallationPostgresEmailPolicy(t *testing.T) {
	fixture := ordinaryInstallationPGFixture(t)
	if _, err := fixture.main.Exec(t.Context(), `UPDATE authsome_users SET email_verified=false WHERE id=$1`, fixture.binding.Actor.UserID); err != nil {
		t.Fatal(err)
	}
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("required=%v", required), func(t *testing.T) {
			f := newOrdinaryInstallationPG(t, fixture)
			report, err := f.install(t, f.authorizer, required)
			if err != nil || report.Ordinary != ProfileReady {
				t.Fatalf("email-policy installation=%+v %v", report, err)
			}
			err = f.run(t, fixture.binding)
			if (!required && err != nil) || (required && !errors.Is(err, security.ErrForbidden)) {
				t.Fatalf("required=%v result=%v", required, err)
			}
		})
	}
}

func TestOrdinaryInstallationPostgresCommonSchemaFailure(t *testing.T) {
	fixture := ordinaryInstallationPGFixture(t)
	for _, tc := range []struct{ name, table, column string }{
		{"membership", "authsome_members", "role"},
		{"team", "loomplane_teams", "org_id"},
		{"deployment mode", "loomplane_projects", "deployment_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrdinaryInstallationPG(t, fixture)
			table := pgx.Identifier{fixture.schema, tc.table}.Sanitize()
			column := pgx.Identifier{tc.column}.Sanitize()
			if _, err := fixture.main.Exec(t.Context(), "ALTER TABLE "+table+" ALTER COLUMN "+column+" DROP NOT NULL"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := fixture.main.Exec(context.Background(), "ALTER TABLE "+table+" ALTER COLUMN "+column+" SET NOT NULL"); err != nil {
					t.Error(err)
				}
			})
			report, err := f.install(t, f.authorizer, true)
			if !errors.Is(err, ErrUnavailable) || report.Ordinary != ProfileUnavailable || report.Fleet != ProfileUnavailable || f.coordinator.installation != nil || f.coordinator.profile != nil {
				t.Fatalf("common schema failure=%+v %v", report, err)
			}
			f.assertSchema(t, fixture.main, fixture.binding.AppID, "")
			f.assertPools(t, 0)
		})
	}
}
