package security

import (
	"context"
	"errors"
	"slices"
	"testing"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
)

// A member role lives at the root namespace and reaches a real project's
// namespace through Warden's ancestor walk: a check on a proj_ typeid runs
// at that project's namespace, not at the root.
func TestWorkspaceRoleReachesARealProject(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	p := newProjectID(t)
	f.setMember(alice, MemberMember)
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	if got := f.rows(t, alice); len(got) != 1 || got[""] != RoleDeveloper {
		t.Fatalf("rows %v, want only a root developer row", got)
	}
	me := f.person(alice)
	if err := f.a.Require(ctx, me, ProjectUpdate, p); err != nil {
		t.Fatalf("project:update on %s: %v", p, err)
	}
	if ok, err := f.a.AllowedOn(ctx, me, DeploymentCreate, p); err != nil || !ok {
		t.Fatalf("AllowedOn(deployment:create, %s) = %v, %v", p, ok, err)
	}
	if err := f.a.Require(ctx, me, ProjectDelete, p); !errors.Is(err, ErrForbidden) {
		t.Fatalf("project:delete on %s = %v, want ErrForbidden", p, err)
	}
}

// One bad grant row (a role a team can't hold, a project ID that isn't a
// project) is skipped: the rest of the sync goes ahead, and a demotion
// still lowers the root role.
func TestSyncSkipsABadGrant(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	p := newProjectID(t)
	f.setMember(alice, MemberAdmin)
	f.setGrants(alice,
		ScopedRole{Role: RoleOwner, TeamID: "team_bad_role"},
		ScopedRole{Role: RoleAdmin, ProjectID: "env_1", TeamID: "team_bad_project"},
		ScopedRole{Role: RoleAdmin, ProjectID: p, TeamID: "team_good"},
	)
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatalf("Sync with a bad grant: %v", err)
	}
	if got := f.rows(t, alice); len(got) != 1 || got[""] != RoleAdmin {
		t.Fatalf("rows %v, want only the root admin row", got)
	}

	// authsome demotes her: the hook's Assign lowers the root row, and the
	// good project grant now shows up above it.
	if err := f.a.Assign(ctx, org, alice, RoleViewer, "authsome:member.role_change"); err != nil {
		t.Fatalf("demotion with a bad grant: %v", err)
	}
	want := map[string]string{"": RoleViewer, mustNamespace(t, p): RoleAdmin}
	if got := f.rows(t, alice); len(got) != len(want) || got[""] != want[""] || got[mustNamespace(t, p)] != RoleAdmin {
		t.Fatalf("rows after the demotion %v, want %v", got, want)
	}
}

// TrySync is the queue job's Sync: it runs only when it can take the
// person's lock and a slot at once, and otherwise says so without waiting.
func TestTrySyncSkipsABusyPerson(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	f.setMember(alice, MemberViewer)

	// Unlocked (unit tests): it just runs.
	if ran, err := f.a.TrySync(ctx, org, alice); err != nil || !ran {
		t.Fatalf("unlocked TrySync = %v, %v", ran, err)
	}

	f.a.SetSyncLock(func(context.Context, string, func(context.Context) error) error {
		t.Error("TrySync waited on the blocking lock")
		return nil
	})
	// A blocking lock without a try-lock is a wiring mistake, never a wait.
	if ran, err := f.a.TrySync(ctx, org, alice); err == nil || ran {
		t.Fatalf("TrySync without a try-lock = %v, %v; want an error", ran, err)
	}

	busy := true
	var names []string
	f.a.SetSyncTryLock(func(ctx context.Context, name string, fn func(context.Context) error) (bool, error) {
		names = append(names, name)
		if busy {
			return false, nil
		}
		return true, fn(ctx)
	})
	before, _ := f.calls()
	if ran, err := f.a.TrySync(ctx, org, alice); err != nil || ran {
		t.Fatalf("TrySync of a busy person = %v, %v; want false, nil", ran, err)
	}
	if after, _ := f.calls(); after != before {
		t.Fatal("TrySync looked the person up without their lock")
	}
	busy = false
	f.setMember(alice, MemberAdmin)
	if ran, err := f.a.TrySync(ctx, org, alice); err != nil || !ran {
		t.Fatalf("TrySync of a free person = %v, %v", ran, err)
	}
	if got := f.rows(t, alice); got[""] != RoleAdmin {
		t.Fatalf("rows %v after TrySync", got)
	}
	want := syncLockPrefix + org + ":" + alice
	if !slices.Equal(names, []string{want, want}) {
		t.Fatalf("try-locks %v, want %q twice", names, want)
	}

	// Every slot taken: it doesn't wait for one either.
	for range cap(f.a.syncSlots) {
		f.a.syncSlots <- struct{}{}
	}
	names = nil
	if ran, err := f.a.TrySync(ctx, org, alice); err != nil || ran {
		t.Fatalf("TrySync with no free slot = %v, %v; want false, nil", ran, err)
	}
	if len(names) != 0 {
		t.Fatal("TrySync asked for the lock without a slot")
	}

	for _, id := range []string{SystemSubject(org), "agent:" + org, ""} {
		if _, err := f.a.TrySync(ctx, org, id); !errors.Is(err, ErrForbidden) {
			t.Errorf("TrySync(%q) = %v, want ErrForbidden", id, err)
		}
	}
}

// Between the before-hook's revoke and authsome's delete the person is
// still a member, so a sync in that window (a repair from their own
// request) writes their member role back. The after-hook revokes again
// under their lock, keyed by the member ID the before-hook saw.
func TestMemberRemoveRevokesAgainAfterTheDelete(t *testing.T) {
	ctx := context.Background()
	orgID, userID, memberID := authid.NewOrgID(), authid.NewUserID(), authid.NewMemberID()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	f := newTeamFixture(t, eng, store, orgID.String())
	user := userID.String()
	f.setMember(user, MemberAdmin)
	var locks []string
	f.a.SetSyncLock(func(ctx context.Context, name string, fn func(context.Context) error) error {
		locks = append(locks, name)
		return fn(ctx)
	})
	s := NewSync(f.a)
	m := &organization.Member{ID: memberID, OrgID: orgID, UserID: userID, Role: organization.RoleAdmin}

	if err = s.OnBeforeMemberRemove(ctx, m); err != nil {
		t.Fatal(err)
	}
	// The window: still a member, so a sync gives the role back.
	if err = f.a.Sync(ctx, f.org, user); err != nil {
		t.Fatal(err)
	}
	if len(f.assignments(t, user)) == 0 {
		t.Fatal("the window sync wrote nothing; the test proves nothing")
	}
	f.removeMember(user) // authsome's delete
	locks = nil
	if err = s.OnAfterMemberRemove(ctx, memberID); err != nil {
		t.Fatalf("after remove: %v", err)
	}
	if rows := f.assignments(t, user); len(rows) != 0 {
		t.Fatalf("%d rows left after the after-hook", len(rows))
	}
	if want := syncLockPrefix + f.org + ":" + user; !slices.Equal(locks, []string{want}) {
		t.Fatalf("after-hook locks %v, want %q", locks, want)
	}

	// Each removal is revoked once: a repeat, or a member ID the before-hook
	// never saw, does nothing.
	locks = nil
	if err = s.OnAfterMemberRemove(ctx, memberID); err != nil || len(locks) != 0 {
		t.Fatalf("repeat after-hook = %v, locks %v", err, locks)
	}
	if err = s.OnAfterMemberRemove(ctx, authid.NewMemberID()); err != nil || len(locks) != 0 {
		t.Fatalf("unknown member after-hook = %v, locks %v", err, locks)
	}

	// A before-hook that fails aborts the removal, so nothing is kept for
	// an after-hook that will never come.
	f.setMember(user, MemberAdmin)
	s.SetLeaver(func(context.Context, string, string) error { return errors.New("teams store is down") })
	if err = s.OnBeforeMemberRemove(ctx, m); err == nil {
		t.Fatal("a failing leaver didn't fail the before-hook")
	}
	if _, kept := s.leaving.Load(memberID.String()); kept {
		t.Fatal("an aborted removal left its member ID behind")
	}
}
