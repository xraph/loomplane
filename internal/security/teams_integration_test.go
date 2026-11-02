//go:build integration

package security

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	_ "github.com/xraph/grove/drivers/pgdriver/pgmigrate" // registers the Postgres migration executor
	"github.com/xraph/warden"
	wardenpg "github.com/xraph/warden/store/postgres"

	"github.com/xraph/loomplane/internal/pglock"
)

// pgPoolSize is the scratch database's connection pool, small so the
// concurrent sync test runs more syncs than there are connections.
const pgPoolSize = 8

// pgWarden is Warden's Postgres store and engine over a database of its
// own, dropped when the test ends, with pglock's advisory locks on it.
func pgWarden(t *testing.T) (*wardenpg.Store, *warden.Engine, LockFunc) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the Warden sync tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("security_teams_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	query := u.Query()
	query.Set("pool_max_conns", fmt.Sprint(pgPoolSize))
	u.RawQuery = query.Encode()
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	gdb, err := grove.Open(pg)
	if err != nil {
		t.Fatal(err)
	}
	store := wardenpg.New(gdb)
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	return store, eng, pglock.For(pg)
}

func pgTeamFixture(t *testing.T, orgID string) *teamFixture {
	t.Helper()
	store, eng, lock := pgWarden(t)
	f := newTeamFixture(t, eng, store, orgID)
	f.a.SetSyncLock(lock)
	return f
}

// TestSyncOnPostgres runs Sync and project checks on Warden's real
// Postgres store: rows per namespace, metadata that survives the round
// trip, checks that stay on their project, a grant removed exactly, and a
// non-member left with nothing.
func TestSyncOnPostgres(t *testing.T) {
	ctx := context.Background()
	f := pgTeamFixture(t, org)
	p, q, r := newProjectID(t), newProjectID(t), newProjectID(t)
	nsP, nsQ := mustNamespace(t, p), mustNamespace(t, q)
	f.setMember(alice, MemberViewer)
	f.setGrants(alice,
		ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_dev"},
		ScopedRole{Role: RoleAdmin, ProjectID: q, TeamID: "team_ops"},
		ScopedRole{Role: RoleViewer, ProjectID: r, TeamID: "team_read"},
	)
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := map[string]string{"": RoleViewer, nsP: RoleDeveloper, nsQ: RoleAdmin}
	if got := f.rows(t, alice); !maps.Equal(got, want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	if got := sourcesOf(f.rowAt(t, alice, nsQ)); !slices.Equal(got, []string{"team_ops"}) {
		t.Fatalf("sources at Q = %v", got)
	}
	if got := f.rowAt(t, alice, nsQ).GrantedBy; got != SyncGrantedBy {
		t.Fatalf("Q row granted by %q", got)
	}

	me := f.person(alice)
	if err := f.a.Require(ctx, me, ProjectUpdate, p); err != nil {
		t.Fatalf("project:update on P: %v", err)
	}
	if err := f.a.Require(ctx, me, DeploymentApprove, q); err != nil {
		t.Fatalf("deployment:approve on Q: %v", err)
	}
	for _, other := range []string{r, ""} {
		if err := f.a.Require(ctx, me, ProjectUpdate, other); !errors.Is(err, ErrForbidden) {
			t.Fatalf("project:update on %q: %v, want ErrForbidden", other, err)
		}
	}
	if ok, err := f.a.AllowedOn(ctx, me, ProjectDelete, q); err != nil || !ok {
		t.Fatalf("AllowedOn(project:delete, Q) = %v, %v", ok, err)
	}
	if ok, err := f.a.AllowedOn(ctx, me, ProjectDelete, p); err != nil || ok {
		t.Fatalf("AllowedOn(project:delete, P) = %v, %v", ok, err)
	}

	root, onP := f.rowAt(t, alice, ""), f.rowAt(t, alice, nsP)
	f.setGrants(alice, ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_dev"})
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	if got := f.rows(t, alice); !maps.Equal(got, map[string]string{"": RoleViewer, nsP: RoleDeveloper}) {
		t.Fatalf("after dropping Q: %v", got)
	}
	if f.rowAt(t, alice, "").ID != root.ID || f.rowAt(t, alice, nsP).ID != onP.ID {
		t.Fatal("dropping Q rewrote another row")
	}
	if err := f.a.Require(ctx, me, DeploymentApprove, q); !errors.Is(err, ErrForbidden) {
		t.Fatalf("deployment:approve on Q after the grant went: %v", err)
	}

	f.removeMember(alice)
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	if rows := f.assignments(t, alice); len(rows) != 0 {
		t.Fatalf("a non-member holds %d rows", len(rows))
	}
}

func TestSyncOnPostgresFailsClosedAndConverges(t *testing.T) {
	store, eng, lock := pgWarden(t)
	fs := &failingStore{Store: store}
	f := newTeamFixture(t, eng, fs, org)
	f.a.SetSyncLock(lock)
	checkSyncFailsClosed(t, f, fs)
}

// TestSyncOnPostgresSerialisesAPerson runs three times as many syncs of
// one person as the pool has connections, at once, under pglock's advisory
// lock: none deadlocks waiting for a connection, every one succeeds, and
// the person ends with one row per namespace.
func TestSyncOnPostgresSerialisesAPerson(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := pgTeamFixture(t, org)
	p := newProjectID(t)
	f.setMember(alice, MemberViewer)
	f.setGrants(alice, ScopedRole{Role: RoleAdmin, ProjectID: p, TeamID: "team_ops"})
	if err := f.a.EnsureTenant(ctx, org); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3*pgPoolSize)
	for i := range 3 * pgPoolSize {
		wg.Go(func() {
			// Half re-assign the member role, as the authsome hooks do.
			if i%2 == 0 {
				errs <- f.a.Assign(ctx, org, alice, RoleViewer, "test")
				return
			}
			errs <- f.a.Sync(ctx, org, alice)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent sync: %v", err)
		}
	}
	want := map[string]string{"": RoleViewer, mustNamespace(t, p): RoleAdmin}
	if got := f.rows(t, alice); !maps.Equal(got, want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
}

func TestMemberRemoveOnPostgres(t *testing.T) {
	ctx := context.Background()
	orgID, userID := authid.NewOrgID(), authid.NewUserID()
	f := pgTeamFixture(t, orgID.String())
	user := userID.String()
	f.setMember(user, MemberMember)
	f.setGrants(user, ScopedRole{Role: RoleAdmin, ProjectID: newProjectID(t), TeamID: "team_ops"})
	if err := f.a.Sync(ctx, f.org, user); err != nil {
		t.Fatal(err)
	}
	s := NewSync(f.a)
	s.SetLeaver(func(context.Context, string, string) error {
		f.setGrants(user)
		return errors.New("teams store is down")
	})
	// The leaver's error is logged and the removal goes ahead.
	if err := s.OnBeforeMemberRemove(ctx, &organization.Member{OrgID: orgID, UserID: userID}); err != nil {
		t.Fatalf("a leaver error blocked the removal: %v", err)
	}
	if rows := f.assignments(t, user); len(rows) != 0 {
		t.Fatalf("a removed member holds %d rows", len(rows))
	}
}
