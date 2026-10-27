//go:build integration

package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
)

const octopusMigration = "20261110000100"

// scratchStore is a PgStore over a database of its own, dropped when the
// test ends, with the octopus migration applied twice (a rerun must be
// harmless).
func scratchStore(t *testing.T) (*PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the Octopus store test")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("octopusloom_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("LOOMPLANE_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations.Group().Migrations() {
		if m.Version != octopusMigration {
			continue
		}
		if m.Name != "octopus_gateways" {
			t.Fatalf("migration %s is %q", octopusMigration, m.Name)
		}
		for range 2 {
			if err = m.Up(ctx, exec); err != nil {
				t.Fatal(err)
			}
		}
		return NewPgStore(pg), pg
	}
	t.Fatalf("no migration %s", octopusMigration)
	return nil, nil
}

func testRef() GatewayRef {
	return GatewayRef{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Resource: "octopus"}
}

func TestPgStoreRouteLifecycle(t *testing.T) {
	store, _ := scratchStore(t)
	ctx := t.Context()
	ref := testRef()

	g := &Gateway{GatewayRef: ref, Provider: ProviderServer}
	if err := store.EnsureGateway(ctx, g); err != nil {
		t.Fatal(err)
	}
	if g.Generation != 1 || g.AppliedGeneration != 0 || g.Revision != 1 || g.FarpHostnames == nil || len(g.FarpHostnames) != 0 {
		t.Fatalf("new gateway: %+v", g)
	}
	set, err := store.SetFarpHostnames(ctx, ref, []string{"api.acme.dev"}, g.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if set.Generation != 2 || set.Revision != 2 || !reflect.DeepEqual(set.FarpHostnames, []string{"api.acme.dev"}) {
		t.Fatalf("after hostnames: %+v", set)
	}
	if _, err = store.SetFarpHostnames(ctx, ref, nil, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("stale hostnames: %v", err)
	}
	if err = store.SaveSyncState(ctx, ref, 2, SyncState{StartTime: "t0", SyncedAt: time.Now().UTC(), OwnedUpstreams: []string{"lp-a-80"}, RouteErrors: map[string]string{"x": "bad"}}); err != nil {
		t.Fatal(err)
	}
	// A second EnsureGateway keeps hostnames, generation and sync state and
	// takes the new provider and namespace.
	again := &Gateway{GatewayRef: ref, Provider: ProviderKubernetes, Namespace: "gateway"}
	if err = store.EnsureGateway(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.Provider != ProviderKubernetes || again.Namespace != "gateway" ||
		!reflect.DeepEqual(again.FarpHostnames, []string{"api.acme.dev"}) || again.Generation != 2 ||
		again.AppliedGeneration != 2 || again.State.StartTime != "t0" || again.State.RouteErrors["x"] != "bad" {
		t.Fatalf("ensure again: %+v", again)
	}
	if _, err = store.Gateway(ctx, GatewayRef{OrgID: "org", EnvironmentID: "other", Resource: "octopus"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing gateway: %v", err)
	}
	all, err := store.Gateways(ctx)
	if err != nil || len(all) != 1 || all[0].ProjectID != "proj" {
		t.Fatalf("gateways: %v %v", all, err)
	}

	route := &Route{GatewayRef: ref, Hostname: "api.acme.dev", Path: "/auth/*", Service: "auth", Port: 8080, StripPrefix: true, CreatedBy: "user"}
	if err = store.CreateRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	if route.ID == "" || route.Revision != 1 || route.CreatedAt.IsZero() || route.Methods == nil {
		t.Fatalf("created route: %+v", route)
	}
	gw, err := store.Gateway(ctx, ref)
	if err != nil || gw.Generation != 3 {
		t.Fatalf("generation after create: %+v %v", gw, err)
	}

	// The same hostname and path is a conflict, and rolls the generation
	// bump back with it.
	dup := &Route{GatewayRef: ref, Hostname: "api.acme.dev", Path: "/auth/*", Service: "other", Port: 80, CreatedBy: "user"}
	if err = store.CreateRoute(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate route: %v", err)
	}
	if gw, err = store.Gateway(ctx, ref); err != nil || gw.Generation != 3 {
		t.Fatalf("generation after refused create: %+v %v", gw, err)
	}
	missing := &Route{GatewayRef: GatewayRef{OrgID: "org", EnvironmentID: "nope", Resource: "octopus"}, Path: "/", Service: "s", Port: 80, CreatedBy: "user"}
	if err = store.CreateRoute(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("route on a missing gateway: %v", err)
	}

	other := &Route{GatewayRef: ref, Hostname: "", Path: "/auth/*", Methods: []string{"GET", "POST"}, Service: "auth", Port: 8080, CreatedBy: "user", Note: "any host"}
	if err = store.CreateRoute(ctx, other); err != nil {
		t.Fatal(err)
	}
	routes, err := store.Routes(ctx, ref)
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes: %v %v", routes, err)
	}
	if routes[0].Hostname != "" || !reflect.DeepEqual(routes[0].Methods, []string{"GET", "POST"}) || routes[0].Note != "any host" {
		t.Fatalf("route order or fields: %+v", routes[0])
	}

	// Updating moves the revision on; reusing the old one is stale.
	old := *route
	route.Port = 9090
	if err = store.UpdateRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	if route.Revision != 2 {
		t.Fatalf("revision after update: %d", route.Revision)
	}
	gw, _ = store.Gateway(ctx, ref)
	genAfterUpdate := gw.Generation
	old.Port = 7070
	if err = store.UpdateRoute(ctx, &old); !errors.Is(err, ErrStale) {
		t.Fatalf("stale update: %v", err)
	}
	if gw, _ = store.Gateway(ctx, ref); gw.Generation != genAfterUpdate {
		t.Fatalf("generation moved on a stale update: %d vs %d", gw.Generation, genAfterUpdate)
	}
	ghost := *route
	ghost.ID = "octoroute_missing"
	if err = store.UpdateRoute(ctx, &ghost); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing route: %v", err)
	}
	// Moving a route onto a taken hostname and path is a conflict.
	clash := *route
	clash.Hostname = ""
	if err = store.UpdateRoute(ctx, &clash); !errors.Is(err, ErrConflict) {
		t.Fatalf("update onto a taken path: %v", err)
	}

	if err = store.DeleteRoute(ctx, ref, route.ID, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("stale delete: %v", err)
	}
	if err = store.DeleteRoute(ctx, ref, route.ID, route.Revision); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteRoute(ctx, ref, route.ID, route.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err = store.BumpGeneration(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err = store.BumpGeneration(ctx, GatewayRef{OrgID: "org", EnvironmentID: "nope", Resource: "octopus"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bump of a missing gateway: %v", err)
	}

	if err = store.DeleteGateway(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if routes, err = store.Routes(ctx, ref); err != nil || len(routes) != 0 {
		t.Fatalf("routes after the gateway went: %v %v", routes, err)
	}
	if err = store.DeleteGateway(ctx, ref); err != nil {
		t.Fatalf("deleting a gone gateway: %v", err)
	}
}

func TestPgStoreGatewayLockIsExclusive(t *testing.T) {
	store, db := scratchStore(t)
	ctx := t.Context()
	ref := testRef()
	elsewhere := ref
	elsewhere.EnvironmentID = "env-2"
	for _, r := range []GatewayRef{ref, elsewhere} {
		if err := store.EnsureGateway(ctx, &Gateway{GatewayRef: r, Provider: ProviderServer}); err != nil {
			t.Fatal(err)
		}
	}
	other := NewPgStore(db)
	ran := false
	err := store.WithGatewayLock(ctx, ref, func() error {
		inner := other.WithGatewayLock(ctx, ref, func() error {
			ran = true
			return nil
		})
		if !errors.Is(inner, ErrBusy) {
			t.Errorf("second lock: %v", inner)
		}
		// Another gateway is free.
		if err := other.WithGatewayLock(ctx, elsewhere, func() error { return nil }); err != nil {
			t.Errorf("other gateway: %v", err)
		}
		return nil
	})
	if err != nil || ran {
		t.Fatalf("first lock: %v (inner ran: %v)", err, ran)
	}
	// Released once run returns, and run's error comes back as it is.
	boom := errors.New("boom")
	if err = store.WithGatewayLock(ctx, ref, func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("lock after release: %v", err)
	}
	// A gateway that doesn't exist has no lease to take.
	missing := ref
	missing.Resource = "missing"
	if err = store.WithGatewayLock(ctx, missing, func() error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lock on a missing gateway: %v", err)
	}
}

func TestPgStoreGatewayLockIsALeaseThatExpires(t *testing.T) {
	store, db := scratchStore(t)
	ctx := t.Context()
	ref := testRef()
	if err := store.EnsureGateway(ctx, &Gateway{GatewayRef: ref, Provider: ProviderServer}); err != nil {
		t.Fatal(err)
	}
	lease := func() (token string, held bool) {
		t.Helper()
		if err := db.QueryRow(ctx, `SELECT sync_lease_token, sync_lease_until IS NOT NULL AND sync_lease_until > now()
			FROM loomplane_octopus_gateways WHERE org_id = $1 AND environment_id = $2 AND resource = $3`,
			ref.OrgID, ref.EnvironmentID, ref.Resource).Scan(&token, &held); err != nil {
			t.Fatal(err)
		}
		return token, held
	}
	other := NewPgStore(db)
	var first string
	err := store.WithGatewayLock(ctx, ref, func() error {
		var held bool
		if first, held = lease(); first == "" || !held {
			t.Errorf("no lease while run runs: %q %v", first, held)
		}
		// A holder that crashed leaves its lease to run out. Once it has,
		// the next sync takes it.
		if _, err := db.Exec(ctx, `UPDATE loomplane_octopus_gateways SET sync_lease_until = now() - interval '1 second'
			WHERE org_id = $1 AND environment_id = $2 AND resource = $3`, ref.OrgID, ref.EnvironmentID, ref.Resource); err != nil {
			t.Fatal(err)
		}
		took := false
		if err := other.WithGatewayLock(ctx, ref, func() error {
			took = true
			if token, _ := lease(); token == first {
				t.Errorf("the second holder kept the first one's token")
			}
			return nil
		}); err != nil || !took {
			t.Errorf("expired lease: %v (took %v)", err, took)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, held := lease(); held {
		t.Fatal("the lease outlived its holders")
	}
	if err = other.WithGatewayLock(ctx, ref, func() error { return nil }); err != nil {
		t.Fatalf("lock after both released: %v", err)
	}
}
