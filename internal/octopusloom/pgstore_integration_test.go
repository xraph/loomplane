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
	ref := testRef()
	other := NewPgStore(db)
	ran := false
	err := store.WithGatewayLock(t.Context(), ref, func() error {
		inner := other.WithGatewayLock(t.Context(), ref, func() error {
			ran = true
			return nil
		})
		if !errors.Is(inner, ErrBusy) {
			t.Errorf("second lock: %v", inner)
		}
		// Another gateway is free.
		elsewhere := ref
		elsewhere.EnvironmentID = "env-2"
		if err := other.WithGatewayLock(t.Context(), elsewhere, func() error { return nil }); err != nil {
			t.Errorf("other gateway: %v", err)
		}
		return nil
	})
	if err != nil || ran {
		t.Fatalf("first lock: %v (inner ran: %v)", err, ran)
	}
	// Released once run returns, and run's error comes back as it is.
	boom := errors.New("boom")
	if err = store.WithGatewayLock(t.Context(), ref, func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("lock after release: %v", err)
	}
}
