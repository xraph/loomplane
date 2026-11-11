package currentauthority

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/observability"
	keyid "github.com/xraph/keysmith/id"
	"github.com/xraph/warden"

	"github.com/xraph/loomplane/internal/security"
)

func TestFleetHandleUnavailableBeforeInstallation(t *testing.T) {
	c := &Coordinator{}
	handle := c.FleetAuthority()
	if schema, err := c.FleetSchema(pgdriver.New()); schema != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uninstalled receipt schema=%q %v", schema, err)
	}
	if err := handle.RunFleet(t.Context(), fleetTestBinding(), func(context.Context, *Attempt) error { t.Fatal("uninstalled callback ran"); return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := InstallBuiltInFleetProfile(t.Context(), c, nil, nil, "", "", nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if c.backend != nil || c.profile != nil || c.installing {
		t.Fatal("invalid installation changed coordinator")
	}
	var absent *FleetAuthority
	if err := absent.RunFleet(t.Context(), fleetTestBinding(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestFleetInstallationDrainPreservesPendingOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Coordinator{installing: true, installCancel: cancel}
	if err := c.Close(); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	stop, end := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer end()
	if err := c.Shutdown(stop); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if ctx.Err() == nil || c.closed || !c.installing {
		t.Fatal("shutdown lost installation ownership")
	}
	c.mu.Lock()
	c.installing = false
	close(c.changed)
	c.changed = nil
	c.mu.Unlock()
	if err := c.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFleetPostgresFixedInstallation(t *testing.T) {
	f := newOrdinaryPGFixture(t)
	c := &Coordinator{}
	handle := c.FleetAuthority()
	router := NewStoreRouter(c, f.store)
	engine, err := warden.NewEngine(warden.WithStore(router), warden.WithCache(NewFreshCache(c, warden.NewMemoryCache())))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if shutdownErr := c.Shutdown(context.Background()); shutdownErr != nil {
			t.Error(shutdownErr)
		}
		if stopErr := engine.Stop(context.Background()); stopErr != nil {
			t.Error(stopErr)
		}
	}()
	telemetry := observability.NewTelemetry("postgresql")
	application := fmt.Sprintf("fleet-install-%d", time.Now().UnixNano())
	dsn, err := withDSNParams(f.dsn, map[string]string{"application_name": application})
	if err != nil {
		t.Fatal(err)
	}
	poolCount := func() int {
		t.Helper()
		var count int
		if countErr := f.main.QueryRow(t.Context(), `SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE application_name=$1`, application).Scan(&count); countErr != nil {
			t.Fatal(countErr)
		}
		return count
	}
	awaitPoolCount := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			got := poolCount()
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("owned sessions=%d, want%d", got, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	table := pgx.Identifier{f.schema, "loomplane_fleet_namespaces"}.Sanitize()
	if _, triggerErr := f.main.Exec(t.Context(), `CREATE TRIGGER fleet_unreviewed AFTER INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger('fleet_namespace')`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	err = InstallBuiltInFleetProfile(t.Context(), c, f.main, engine, dsn, f.binding.AppID, telemetry, nil)
	if _, dropErr := f.main.Exec(t.Context(), `DROP TRIGGER fleet_unreviewed ON `+table); dropErr != nil {
		t.Fatal(dropErr)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown namespace hook=%v", err)
	}
	if c.backend != nil || c.profile != nil || c.installing {
		t.Fatal("failed qualification retained installed resources")
	}
	awaitPoolCount(0)
	if schema, err := c.FleetSchema(f.main); schema != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed startup exposed receipt schema=%q %v", schema, err)
	}
	projectTable := pgx.Identifier{f.schema, "loomplane_projects"}.Sanitize()
	var modeTrigger string
	if err := f.main.QueryRow(t.Context(), `SELECT pg_catalog.pg_get_triggerdef(oid) FROM pg_catalog.pg_trigger WHERE tgrelid=$1::pg_catalog.regclass AND tgname='loomplane_fleet_project_mode'`, projectTable).Scan(&modeTrigger); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, change, restore string }{
		{"nullable deployment mode", "ALTER COLUMN deployment_mode DROP NOT NULL", "ALTER COLUMN deployment_mode SET NOT NULL"},
		{"wrong deployment mode type", "ALTER COLUMN deployment_mode TYPE varchar", "ALTER COLUMN deployment_mode TYPE text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alter := func(statement string) error {
				query := "ALTER TABLE " + projectTable + " " + statement
				if strings.Contains(statement, " TYPE ") {
					query = "DROP TRIGGER loomplane_fleet_project_mode ON " + projectTable + ";" + query + ";" + modeTrigger
				}
				_, err := f.main.Exec(t.Context(), query)
				return err
			}
			if err := alter(tc.change); err != nil {
				t.Fatal(err)
			}
			err := InstallBuiltInFleetProfile(t.Context(), c, f.main, engine, dsn, f.binding.AppID, telemetry, nil)
			if restoreErr := alter(tc.restore); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			if !errors.Is(err, ErrUnavailable) || c.backend != nil || c.profile != nil {
				t.Fatalf("unsupported project schema installation=%v", err)
			}
			awaitPoolCount(0)
		})
	}

	if err := InstallBuiltInFleetProfile(t.Context(), c, f.main, engine, dsn, f.binding.AppID, telemetry, nil); err != nil {
		t.Fatal(err)
	}
	awaitPoolCount(2)
	installed := c.installation
	if installed == nil || installed.main != f.main || installed.backend != c.backend || installed.engine != engine || installed.dsnDigest != sha256.Sum256([]byte(dsn)) || installed.appID != f.binding.AppID || installed.schema != f.schema || installed.owner != f.role {
		t.Fatal("lost qualified shared ownership")
	}
	if schema, err := handle.FleetSchema(f.main); err != nil || schema != f.schema {
		t.Fatalf("installed receipt schema=%q %v", schema, err)
	}
	if schema, err := handle.FleetSchemaForApp(f.main, f.binding.AppID); err != nil || schema != f.schema {
		t.Fatalf("installed application schema=%q %v", schema, err)
	}
	if schema, err := handle.FleetSchemaForApp(f.main, "different-app"); schema != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign application borrowed schema=%q %v", schema, err)
	}

	if schema, err := c.FleetSchema(pgdriver.New()); schema != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("different main borrowed qualification=%q %v", schema, err)
	}
	t.Run("same schema in another isolated database is unavailable", func(t *testing.T) {
		config, err := pgxpool.ParseConfig(f.dsn)
		if err != nil {
			t.Fatal(err)
		}
		originalDatabase := config.ConnConfig.Database
		otherDatabase := "postgres"
		if originalDatabase == "postgres" {
			otherDatabase = "task031"
		}
		otherDSN := f.dsn
		if strings.HasPrefix(otherDSN, "postgres://") || strings.HasPrefix(otherDSN, "postgresql://") {
			separator := "?"
			if postgresURIQuery(otherDSN) >= 0 {
				separator = "&"
			}
			otherDSN += separator + "dbname=" + otherDatabase
		} else {
			otherDSN += " dbname='" + otherDatabase + "'"
		}
		other := pgdriver.New()
		if err := other.Open(t.Context(), otherDSN, driver.WithPoolSize(1), driver.WithQueryTimeout(AttemptLimit)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := other.Close(); err != nil {
				t.Error(err)
			}
		})
		if _, err := other.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{f.schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), AttemptLimit)
			defer cancel()
			if _, err := other.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE"); err != nil {
				t.Error(err)
			}
		})
		var schema, database string
		if err := other.QueryRow(t.Context(), "SELECT pg_catalog.current_schema(),pg_catalog.current_database()").Scan(&schema, &database); err != nil {
			t.Fatal(err)
		}
		if schema != f.schema || database == originalDatabase {
			t.Fatal("different-database fixture is not equivalent by schema name")
		}
		if schema, err := c.FleetSchema(other); schema != "" || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("other database borrowed qualification=%q %v", schema, err)
		}
	})
	first := c.backend
	if c.profile.ordinary != nil || c.profile.fleet == nil || c.schema != f.schema || c.reader == nil {
		t.Fatal("wrong installed profile")
	}
	if err := InstallBuiltInFleetProfile(t.Context(), c, f.main, engine, dsn, f.binding.AppID, telemetry, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("reinstallation=%v", err)
	}
	if c.backend != first {
		t.Fatal("reinstallation replaced pools")
	}
	if err := c.Run(t.Context(), f.binding, func(context.Context, *Attempt) error { t.Fatal("Fleet install enabled ordinary Run"); return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	// Registration retains this handle while successful installation publishes
	// the coordinator it already points to.
	if handle.coordinator != c {
		t.Fatal("dependency handle replaced")
	}
	if _, err := engine.Store().GetRoleBySlug(t.Context(), f.binding.Actor.OrgID, "", security.RoleDeveloper); err != nil {
		t.Fatalf("ordinary identity router=%v", err)
	}
	b := FleetBinding(f.binding)
	b.Actor.KeyID = keyid.NewKeyID().String()
	b.Actor.Kind = security.KindFleet
	b.Actor.UserID = security.FleetSubject(b.Actor.KeyID)
	b.Actor.Fleet = &security.FleetScope{ProjectID: f.project, FleetID: "fleet"}
	metadata, _ := json.Marshal(map[string]string{"kind": string(security.KindFleet), "access": "write", "project_id": f.project, "fleet_id": "fleet"})
	if _, err := f.main.Exec(t.Context(), `INSERT INTO keysmith_keys(id,tenant_id,app_id,name,prefix,hint,key_hash,created_by,metadata,expires_at) VALUES($1,$2,$3,'Fleet','lp','hint','hash','historical-creator',$4,now()+interval '1 hour')`, b.Actor.KeyID, b.Actor.OrgID, b.AppID, string(metadata)); err != nil {
		t.Fatal(err)
	}
	if err := handle.RunFleet(t.Context(), b, func(ctx context.Context, a *Attempt) error {
		d, err := a.EvaluateFleet(ctx)
		if err != nil {
			return err
		}
		return a.StageFleet(ctx, d, b, func(ctx context.Context, tx driver.Tx, at time.Time) error {
			_, err := tx.Exec(ctx, `INSERT INTO authority_candidates(id,authorized_at) VALUES($1,$2)`, b.RecordID, at)
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.main.Exec(t.Context(), `UPDATE keysmith_keys SET revoked_at=now() WHERE id=$1`, b.Actor.KeyID); err != nil {
		t.Fatal(err)
	}
	if schema, err := c.FleetSchema(f.main); err != nil || schema != f.schema {
		t.Fatalf("revocation erased receipt qualification=%q %v", schema, err)
	}
	if schema, err := handle.FleetSchemaForApp(f.main, b.AppID); err != nil || schema != f.schema {
		t.Fatalf("revocation erased application receipt qualification=%q %v", schema, err)
	}
	wrongApp := b
	wrongApp.AppID = "different-app"
	if err := handle.RunFleet(t.Context(), wrongApp, func(context.Context, *Attempt) error {
		t.Fatal("foreign application entered historical callback")
		return nil
	}); !errors.Is(err, ErrBinding) {
		t.Fatalf("foreign application entry=%v", err)
	}

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
		done <- handle.RunFleet(t.Context(), b, func(ctx context.Context, _ *Attempt) error { close(entered); <-ctx.Done(); <-release; return ctx.Err() })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("installed attempt did not start")
	}
	stop, cancelStop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelStop()
	if err := c.Shutdown(stop); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting shutdown=%v", err)
	}
	if schema, err := c.FleetSchema(f.main); schema != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stopping coordinator exposed receipt schema=%q %v", schema, err)
	}
	close(release)
	if err := c.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("drained attempt=%v", err)
	}
	awaitPoolCount(0)
	if schema, err := c.FleetSchema(f.main); schema != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("shutdown allowed receipt schema=%q %v", schema, err)
	}
	if err := handle.RunFleet(t.Context(), fleetTestBinding(), nil); !errors.Is(err, ErrIncomplete) && !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

type fleetDelayedCloseBackend struct {
	backend
	entered chan struct{}
	release chan struct{}
	failure error
}

func (b *fleetDelayedCloseBackend) close() error {
	close(b.entered)
	<-b.release
	return b.failure
}

func TestFleetShutdownBoundsDriverTeardownAndRetainsFailure(t *testing.T) {
	failure := errors.New("late driver teardown")
	backend := &fleetDelayedCloseBackend{entered: make(chan struct{}), release: make(chan struct{}), failure: failure}
	c := &Coordinator{backend: backend}
	if c.Drained() {
		t.Fatal("unclosed coordinator reported drained")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := c.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded shutdown=%v", err)
	}
	<-backend.entered
	if c.Drained() {
		t.Fatal("pending driver cleanup reported drained")
	}
	if err := c.Close(); !errors.Is(err, ErrBusy) {
		t.Fatalf("pending ownership=%v", err)
	}
	close(backend.release)
	if err := c.Shutdown(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("joined cleanup=%v", err)
	}
	if !c.Drained() {
		t.Fatal("completed cleanup diagnostic retained ownership")
	}
	if err := c.Close(); !errors.Is(err, failure) {
		t.Fatalf("retained cleanup failure=%v", err)
	}
}

func TestFleetSchemaForAppBindsExactInstallation(t *testing.T) {
	for _, name := range []string{"matching", "wrong app", "empty app", "nil main", "wrong main", "empty installation app", "profile mismatch", "stopping", "closing", "closed"} {
		t.Run(name, func(t *testing.T) {
			c := &Coordinator{}
			engine, router := fixedEngineFixture(t, c)
			profile, err := captureEngineProfile(engine, router)
			if err != nil {
				t.Fatal(err)
			}
			main := pgdriver.New()
			appID := "installed-app"
			owned := &nativeBackend{}
			profile.fleet = &fleetProfile{appID: appID}
			c.installation = &builtInInstallation{main: main, backend: owned, engine: engine, appID: appID, schema: "qualified", owner: 42}
			c.backend, c.profile, c.schema = owned, profile, "qualified"
			switch name {
			case "wrong app":
				appID = "other-app"
			case "empty app":
				appID = ""
			case "nil main":
				main = nil
			case "wrong main":
				main = pgdriver.New()
			case "empty installation app":
				c.installation.appID, profile.fleet.appID = "", ""
			case "profile mismatch":
				profile.fleet.appID = "other-app"
			case "stopping":
				c.stopping = true
			case "closing":
				c.closing = true
			case "closed":
				c.closed = true
			}
			// The main database is unopened, so this must remain a metadata check.
			schema, err := c.FleetAuthority().FleetSchemaForApp(main, appID)
			if name == "matching" {
				if err != nil || schema != "qualified" {
					t.Fatalf("matching installation=%q %v", schema, err)
				}
			} else if schema != "" || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unsupported installation=%q %v", schema, err)
			}
		})
	}
	for _, handle := range []*FleetAuthority{nil, {}, (&Coordinator{}).FleetAuthority()} {
		if schema, err := handle.FleetSchemaForApp(pgdriver.New(), "installed-app"); schema != "" || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("uninstalled app binding=%q %v", schema, err)
		}
	}
}
