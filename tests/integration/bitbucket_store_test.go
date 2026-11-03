//go:build integration

package integration

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/repositories"
)

func bitbucketStore(t *testing.T) (*repositories.PgStore, *pgdriver.PgDB) {
	t.Helper()
	dsn := freshDatabase(t)
	ctx := t.Context()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := pgdriver.New()
	if err := pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return repositories.NewPgStore(pg), pg
}

func workspace(org, uuid, slug string) *repositories.BitbucketConnection {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &repositories.BitbucketConnection{
		OrgID: org, WorkspaceUUID: uuid, WorkspaceSlug: slug, WorkspaceName: "Acme",
		AvatarURL: "https://bitbucket.org/avatar.png", BitbucketUserUUID: "{user-1}", BitbucketUserName: "Rex",
		Status: repositories.BitbucketReady, CreatedBy: "usr_1", CreatedAt: now, UpdatedAt: now,
	}
}

func TestBitbucketStore_workspacesPerOrganization(t *testing.T) {
	store, _ := bitbucketStore(t)
	ctx := t.Context()
	one, two := workspace("org_1", "{ws-1}", "acme"), workspace("org_2", "{ws-1}", "acme")
	for _, c := range []*repositories.BitbucketConnection{one, two} {
		if err := store.SaveBitbucketWorkspace(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.ID == "" {
			t.Fatal("saving a workspace gave it no ID")
		}
		if c.GrantGeneration != 1 {
			t.Fatalf("a new workspace's grant generation is %d, want 1", c.GrantGeneration)
		}
	}
	if one.ID == two.ID {
		t.Fatalf("two organizations share a connection ID %q", one.ID)
	}
	for _, org := range []string{"org_1", "org_2"} {
		list, err := store.ListBitbucketWorkspaces(ctx, org)
		if err != nil || len(list) != 1 || list[0].OrgID != org {
			t.Fatalf("%s lists %v %v", org, list, err)
		}
	}

	// Connecting the same workspace again in one organization refreshes the
	// row and keeps its ID and creation time. The member who reconnects is the
	// one whose sign-in is stored now, so they replace the first author.
	again := workspace("org_1", "{ws-1}", "acme-renamed")
	again.WorkspaceName = "Acme Inc"
	again.CreatedBy = "usr_2"
	again.CreatedAt = one.CreatedAt.Add(time.Hour)
	again.UpdatedAt = one.UpdatedAt.Add(time.Hour)
	if err := store.SaveBitbucketWorkspace(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != one.ID {
		t.Fatalf("reconnecting changed the ID from %q to %q", one.ID, again.ID)
	}
	if again.GrantGeneration != 2 {
		t.Fatalf("reconnecting left the grant generation at %d, want 2", again.GrantGeneration)
	}
	got, err := store.GetBitbucketWorkspace(ctx, "org_1", one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceSlug != "acme-renamed" || got.WorkspaceName != "Acme Inc" || !got.CreatedAt.Equal(one.CreatedAt) || got.CreatedBy != "usr_2" || !got.UpdatedAt.Equal(again.UpdatedAt) || got.GrantGeneration != 2 {
		t.Fatalf("reconnected row %+v", got)
	}
	if other, err := store.GetBitbucketWorkspace(ctx, "org_2", one.ID); !errors.Is(err, repositories.ErrWorkspaceNotFound) {
		t.Fatalf("org_2 read org_1's workspace: %+v %v", other, err)
	}

	if err := store.SetBitbucketWorkspaceStatus(ctx, "org_1", one.ID, repositories.BitbucketReconnectNeeded, "Bitbucket refused the sign-in"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetBitbucketWorkspace(ctx, "org_1", one.ID)
	if got.Status != repositories.BitbucketReconnectNeeded || got.Message != "Bitbucket refused the sign-in" {
		t.Fatalf("status %q message %q", got.Status, got.Message)
	}
	if err := store.SetBitbucketWorkspaceStatus(ctx, "org_2", one.ID, repositories.BitbucketReady, ""); !errors.Is(err, repositories.ErrWorkspaceNotFound) {
		t.Fatalf("org_2 changed org_1's status: %v", err)
	}

	if err := store.DeleteBitbucketWorkspace(ctx, "org_2", one.ID); !errors.Is(err, repositories.ErrWorkspaceNotFound) {
		t.Fatalf("org_2 deleted org_1's workspace: %v", err)
	}
	if err := store.DeleteBitbucketWorkspace(ctx, "org_1", one.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBitbucketWorkspace(ctx, "org_1", one.ID); !errors.Is(err, repositories.ErrWorkspaceNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if list, _ := store.ListBitbucketWorkspaces(ctx, "org_2"); len(list) != 1 {
		t.Fatalf("org_2 lost its workspace: %v", list)
	}
}

func TestBitbucketStore_lockSerializes(t *testing.T) {
	store, _ := bitbucketStore(t)
	holding, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- store.WithBitbucketLock(t.Context(), "org_1", "bbw_1", func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	var entered atomic.Bool
	second := make(chan error, 1)
	go func() {
		second <- store.WithBitbucketLock(t.Context(), "org_1", "bbw_1", func() error { entered.Store(true); return nil })
	}()
	// Another workspace's lock is independent.
	if err := store.WithBitbucketLock(t.Context(), "org_1", "bbw_2", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if entered.Load() {
		t.Fatal("the second caller entered while the first held the lock")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil || !entered.Load() {
		t.Fatalf("second caller: entered %v, %v", entered.Load(), err)
	}

	// An error from fn comes back, and releases the lock.
	boom := errors.New("boom")
	if err := store.WithBitbucketLock(t.Context(), "org_1", "bbw_1", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("fn's error: %v", err)
	}
	if err := store.WithBitbucketLock(t.Context(), "org_1", "bbw_1", func() error { return nil }); err != nil {
		t.Fatalf("lock after an error: %v", err)
	}
}

func TestBitbucketStore_hookPerOrgAndRepository(t *testing.T) {
	store, _ := bitbucketStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	hook := func(id, org, repo string) *repositories.BitbucketHook {
		return &repositories.BitbucketHook{HookID: id, OrgID: org, ConnectionID: "bbw_1", Repository: repo,
			BitbucketHookUUID: "{" + id + "}", Status: repositories.HookOn, CreatedAt: now, UpdatedAt: now}
	}
	if err := store.SaveBitbucketHook(ctx, hook("hook_1", "org_1", "bitbucket.org/ACME/api")); err != nil {
		t.Fatal(err)
	}
	// The same repository in the same organization conflicts, in any case.
	for _, repo := range []string{"bitbucket.org/ACME/api", "bitbucket.org/acme/API"} {
		if err := store.SaveBitbucketHook(ctx, hook("hook_2", "org_1", repo)); !errors.Is(err, repositories.ErrHookExists) {
			t.Fatalf("second hook for %s: %v", repo, err)
		}
	}
	// Another organization keeps its own.
	if err := store.SaveBitbucketHook(ctx, hook("hook_3", "org_2", "bitbucket.org/ACME/api")); err != nil {
		t.Fatal(err)
	}

	got, err := store.BitbucketHookFor(ctx, "org_1", "bitbucket.org/acme/api")
	if err != nil || got.HookID != "hook_1" || got.Repository != "bitbucket.org/ACME/api" || got.Status != repositories.HookOn || !got.CreatedAt.Equal(now) {
		t.Fatalf("hook for the repository: %+v %v", got, err)
	}
	if _, err := store.BitbucketHookFor(ctx, "org_1", "bitbucket.org/acme/other"); !errors.Is(err, repositories.ErrHookNotFound) {
		t.Fatalf("hook for another repository: %v", err)
	}
	byID, err := store.GetBitbucketHook(ctx, "hook_3")
	if err != nil || byID.OrgID != "org_2" {
		t.Fatalf("hook by ID: %+v %v", byID, err)
	}
	if _, err := store.GetBitbucketHook(ctx, "nope"); !errors.Is(err, repositories.ErrHookNotFound) {
		t.Fatalf("missing hook: %v", err)
	}

	// A hook that couldn't be created stays as an off row with its reason.
	off := hook("hook_4", "org_1", "bitbucket.org/acme/web")
	off.BitbucketHookUUID, off.Status, off.Reason = "", repositories.HookOff, "Bitbucket said the account can't admin the repository"
	if err := store.SaveBitbucketHook(ctx, off); err != nil {
		t.Fatal(err)
	}
	hooks, err := store.BitbucketHooksIn(ctx, "org_1", "bbw_1")
	if err != nil || len(hooks) != 2 || hooks[0].HookID != "hook_1" || hooks[1].Status != repositories.HookOff || hooks[1].Reason == "" {
		t.Fatalf("hooks in the workspace: %+v %v", hooks, err)
	}
	if hooks, _ := store.BitbucketHooksIn(ctx, "org_1", "bbw_2"); len(hooks) != 0 {
		t.Fatalf("another workspace's hooks: %+v", hooks)
	}

	// An off hook turns on in place: same ID, new UUID and status.
	later := now.Add(time.Hour)
	turnOn := &repositories.BitbucketHook{HookID: "hook_4", ConnectionID: "bbw_2", BitbucketHookUUID: "{hook_4}", Status: repositories.HookOn, UpdatedAt: later}
	if err := store.UpdateBitbucketHook(ctx, turnOn); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetBitbucketHook(ctx, "hook_4")
	if err != nil || updated.Status != repositories.HookOn || updated.Reason != "" || updated.BitbucketHookUUID != "{hook_4}" ||
		updated.ConnectionID != "bbw_2" || !updated.UpdatedAt.Equal(later) || !updated.CreatedAt.Equal(now) ||
		updated.OrgID != "org_1" || updated.Repository != "bitbucket.org/acme/web" {
		t.Fatalf("hook after turning it on: %+v %v", updated, err)
	}
	if err := store.UpdateBitbucketHook(ctx, &repositories.BitbucketHook{HookID: "nope", Status: repositories.HookOn}); !errors.Is(err, repositories.ErrHookNotFound) {
		t.Fatalf("updating an unknown hook: %v", err)
	}

	if err := store.DeleteBitbucketHook(ctx, "hook_1"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBitbucketHook(ctx, "hook_1"); !errors.Is(err, repositories.ErrHookNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := store.SaveBitbucketHook(ctx, hook("hook_5", "org_1", "bitbucket.org/ACME/api")); err != nil {
		t.Fatalf("hook after the first was deleted: %v", err)
	}
}

func TestBitbucketStore_connectorMembership(t *testing.T) {
	store, pg := bitbucketStore(t)
	ctx := t.Context()
	for _, q := range []string{
		// Only membership matters here, so drop the foreign keys that would
		// need a whole app, environment and organization behind each row.
		`DO $$ DECLARE r record; BEGIN
			FOR r IN SELECT conrelid::regclass AS t, conname FROM pg_constraint
				WHERE contype = 'f' AND conrelid IN ('authsome_users'::regclass, 'authsome_members'::regclass)
			LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.t, r.conname); END LOOP;
		END $$`,
		`INSERT INTO authsome_users(id,app_id,env_id,email,banned,deleted_at) VALUES
			('here','app','env','a@example.com',false,NULL),('gone','app','env','b@example.com',false,NULL),
			('banned','app','env','c@example.com',true,NULL),('deleted','app','env','d@example.com',false,now())`,
		`INSERT INTO authsome_members(id,org_id,user_id) VALUES ('m1','org_1','here'),('m2','org_1','banned'),('m3','org_1','deleted'),('m4','org_2','gone')`,
	} {
		if _, err := pg.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		org, user string
		want      bool
	}{
		{"org_1", "here", true},
		{"org_1", "gone", false},    // never a member of org_1
		{"org_2", "here", false},    // a member of another organization only
		{"org_2", "gone", true},     // member there
		{"org_1", "banned", false},  // banned
		{"org_1", "deleted", false}, // deleted
		{"org_1", "", false},
	} {
		got, err := store.BitbucketConnectorIsMember(ctx, c.org, c.user)
		if err != nil || got != c.want {
			t.Errorf("%s in %s: got %v, %v; want %v", c.user, c.org, got, err, c.want)
		}
	}
}
