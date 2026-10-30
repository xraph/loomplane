package security

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	"github.com/xraph/warden/store/memory"
	"go.jetify.com/typeid/v2"
)

// teamFixture is an Authorizer whose members and team grants a test sets
// by hand, and which counts how often Sync asked for either.
type teamFixture struct {
	a     *Authorizer
	store Store
	org   string

	mu          sync.Mutex
	members     map[string]string       // user ID to authsome member role
	grants      map[string][]ScopedRole // user ID to their teams' grants
	memberCalls int
	grantCalls  int
}

// newTeamFixture builds the fixture over store, checked through eng, in
// the organization orgID.
func newTeamFixture(t *testing.T, eng Engine, store Store, orgID string) *teamFixture {
	t.Helper()
	f := &teamFixture{store: store, org: orgID, members: map[string]string{}, grants: map[string][]ScopedRole{}}
	f.a = NewAuthorizer(eng, store, func(_ context.Context, o, userID string) (string, bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.memberCalls++
		if o != f.org {
			return "", false, nil
		}
		r, ok := f.members[userID]
		return r, ok, nil
	})
	f.a.SetTeamGrants(func(_ context.Context, o, userID string) ([]ScopedRole, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.grantCalls++
		if o != f.org {
			return nil, nil
		}
		return slices.Clone(f.grants[userID]), nil
	})
	return f
}

// memTeamFixture is newTeamFixture over Warden's real engine and its
// in-memory store, as newAuthorizer builds it.
func memTeamFixture(t *testing.T) *teamFixture {
	t.Helper()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatalf("warden: %v", err)
	}
	return newTeamFixture(t, eng, store, org)
}

func (f *teamFixture) setMember(userID, memberRole string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[userID] = memberRole
}

func (f *teamFixture) removeMember(userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.members, userID)
}

func (f *teamFixture) setGrants(userID string, grants ...ScopedRole) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants[userID] = grants
}

func (f *teamFixture) calls() (members, grants int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.memberCalls, f.grantCalls
}

func (f *teamFixture) person(userID string) Principal {
	return Principal{UserID: userID, OrgID: f.org}
}

// assignments lists every row the user holds in the fixture's
// organization, in every namespace.
func (f *teamFixture) assignments(t *testing.T, userID string) []*assignment.Assignment {
	t.Helper()
	rows, err := f.store.ListAssignments(context.Background(), &assignment.ListFilter{
		TenantID: f.org, SubjectKind: string(warden.SubjectUser), SubjectID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// rows is the user's assignments as namespace to role slug. Two rows in
// one namespace fail the test: Sync writes one per namespace.
func (f *teamFixture) rows(t *testing.T, userID string) map[string]string {
	t.Helper()
	slugs := map[string]string{}
	for _, def := range Catalog() {
		r, err := f.store.GetRoleBySlug(context.Background(), f.org, rootNamespace, def.Slug)
		if err != nil {
			continue // the tenant may not exist yet
		}
		slugs[r.ID.String()] = def.Slug
	}
	out := map[string]string{}
	for _, row := range f.assignments(t, userID) {
		if _, dup := out[row.NamespacePath]; dup {
			t.Fatalf("two rows at namespace %q", row.NamespacePath)
		}
		out[row.NamespacePath] = slugs[row.RoleID.String()]
	}
	return out
}

// rowAt is the user's row at namespace ns, or nil.
func (f *teamFixture) rowAt(t *testing.T, userID, ns string) *assignment.Assignment {
	t.Helper()
	for _, row := range f.assignments(t, userID) {
		if row.NamespacePath == ns {
			return row
		}
	}
	return nil
}

// sourcesOf reads a row's metadata sources, which come back as []string
// from the memory store and []any from Postgres.
func sourcesOf(row *assignment.Assignment) []string {
	switch v := row.Metadata["sources"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, s := range v {
			str, _ := s.(string)
			out = append(out, str)
		}
		return out
	}
	return nil
}

func newProjectID(t *testing.T) string {
	t.Helper()
	return typeid.MustGenerate("proj").String()
}

func mustNamespace(t *testing.T, projectID string) string {
	t.Helper()
	ns, ok := ProjectNamespace(projectID)
	if !ok {
		t.Fatalf("%s has no namespace", projectID)
	}
	return ns
}

func TestProjectNamespace(t *testing.T) {
	id := typeid.MustGenerate("proj")
	ns, ok := ProjectNamespace(id.String())
	if !ok || ns != "proj-"+id.Suffix() {
		t.Fatalf("ProjectNamespace(%s) = %q, %v", id, ns, ok)
	}
	if err := warden.ValidateNamespacePath(ns, 0); err != nil {
		t.Fatalf("Warden refuses %q: %v", ns, err)
	}
	for _, bad := range []string{"proj_1", "env_" + id.Suffix(), "", org, "proj-" + id.Suffix()} {
		if ns, ok := ProjectNamespace(bad); ok || ns != "" {
			t.Errorf("ProjectNamespace(%q) = %q, %v, want \"\", false", bad, ns, ok)
		}
	}
}

func TestProjectGrantAppliesOnlyOnItsProject(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	p, q := newProjectID(t), newProjectID(t)
	f.setMember(alice, MemberViewer)
	f.setGrants(alice, ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_a"})
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	me := f.person(alice)
	if err := f.a.Require(ctx, me, ProjectUpdate, p); err != nil {
		t.Fatalf("project:update on the granted project: %v", err)
	}
	for _, other := range []string{q, ""} {
		if err := f.a.Require(ctx, me, ProjectUpdate, other); !errors.Is(err, ErrForbidden) {
			t.Fatalf("project:update on %q: %v, want ErrForbidden", other, err)
		}
	}
	allowedOn := func(perm Permission, projectID string) bool {
		t.Helper()
		ok, err := f.a.AllowedOn(ctx, me, perm, projectID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !allowedOn(DeploymentCreate, p) || allowedOn(DeploymentCreate, q) {
		t.Fatal("AllowedOn doesn't follow the project grant")
	}
	if ok, err := f.a.Allowed(ctx, me, ProjectUpdate); err != nil || ok {
		t.Fatalf("workspace-wide project:update = %v, %v; want false", ok, err)
	}
}

func TestSyncKeepsTheHighestRolePerNamespace(t *testing.T) {
	p := newProjectID(t)
	nsP := mustNamespace(t, p)
	tests := []struct {
		name        string
		member      string
		grants      []ScopedRole
		want        map[string]string
		wantSources map[string][]string
	}{
		{
			name: "team admin on the workspace beats a developer", member: MemberMember,
			grants:      []ScopedRole{{Role: RoleAdmin, TeamID: "team_ops"}},
			want:        map[string]string{"": RoleAdmin},
			wantSources: map[string][]string{"": {"team_ops"}},
		},
		{
			name: "an admin needs no developer row on a project", member: MemberAdmin,
			grants: []ScopedRole{{Role: RoleDeveloper, ProjectID: p, TeamID: "team_dev"}},
			want:   map[string]string{"": RoleAdmin},
		},
		{
			name: "a viewer with team admin on a project", member: MemberViewer,
			grants: []ScopedRole{{Role: RoleAdmin, ProjectID: p, TeamID: "team_ops"}},
			want:   map[string]string{"": RoleViewer, nsP: RoleAdmin},
		},
		{
			name: "two teams on one project, the higher wins", member: MemberViewer,
			grants: []ScopedRole{
				{Role: RoleDeveloper, ProjectID: p, TeamID: "team_dev"},
				{Role: RoleAdmin, ProjectID: p, TeamID: "team_ops"},
			},
			want:        map[string]string{"": RoleViewer, nsP: RoleAdmin},
			wantSources: map[string][]string{"": {"member"}, nsP: {"team_ops"}},
		},
		{
			name: "two teams with the same role both count as sources", member: MemberViewer,
			grants: []ScopedRole{
				{Role: RoleDeveloper, ProjectID: p, TeamID: "team_a"},
				{Role: RoleDeveloper, ProjectID: p, TeamID: "team_b"},
			},
			want:        map[string]string{"": RoleViewer, nsP: RoleDeveloper},
			wantSources: map[string][]string{nsP: {"team_a", "team_b"}},
		},
		{
			name: "a workspace grant covers the same project grant", member: MemberViewer,
			grants: []ScopedRole{
				{Role: RoleDeveloper, TeamID: "team_a"},
				{Role: RoleDeveloper, ProjectID: p, TeamID: "team_b"},
			},
			want: map[string]string{"": RoleDeveloper},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := memTeamFixture(t)
			f.setMember(alice, tt.member)
			f.setGrants(alice, tt.grants...)
			if err := f.a.Sync(context.Background(), org, alice); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if got := f.rows(t, alice); !maps.Equal(got, tt.want) {
				t.Fatalf("rows %v, want %v", got, tt.want)
			}
			for ns, want := range tt.wantSources {
				if got := sourcesOf(f.rowAt(t, alice, ns)); !slices.Equal(got, want) {
					t.Errorf("sources at %q = %v, want %v", ns, got, want)
				}
			}
		})
	}
}

func TestSyncRemovesExactlyWhatAGrantGave(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	p, q := newProjectID(t), newProjectID(t)
	nsP, nsQ := mustNamespace(t, p), mustNamespace(t, q)
	f.setMember(alice, MemberViewer)
	f.setGrants(alice,
		ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_a"},
		ScopedRole{Role: RoleDeveloper, ProjectID: q, TeamID: "team_b"},
	)
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	root, onP := f.rowAt(t, alice, ""), f.rowAt(t, alice, nsP)
	if root == nil || onP == nil || f.rowAt(t, alice, nsQ) == nil {
		t.Fatalf("setup rows %v", f.rows(t, alice))
	}

	f.setGrants(alice, ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_a"})
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	if f.rowAt(t, alice, nsQ) != nil {
		t.Fatal("the dropped grant's row is still there")
	}
	if got := f.rowAt(t, alice, ""); got == nil || got.ID != root.ID {
		t.Fatalf("root row %v, want it kept as %s", got, root.ID)
	}
	if got := f.rowAt(t, alice, nsP); got == nil || got.ID != onP.ID {
		t.Fatalf("project P row %v, want it kept as %s", got, onP.ID)
	}
}

func TestSyncOfANonMemberRevokesEverything(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	f.setMember(alice, MemberViewer)
	f.setGrants(alice, ScopedRole{Role: RoleAdmin, ProjectID: newProjectID(t), TeamID: "team_a"})
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	if len(f.assignments(t, alice)) != 2 {
		t.Fatalf("setup rows %v", f.rows(t, alice))
	}
	f.removeMember(alice)
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatalf("Sync of a non-member: %v", err)
	}
	if rows := f.assignments(t, alice); len(rows) != 0 {
		t.Fatalf("a non-member still holds %d rows", len(rows))
	}
}

func TestSyncRefusesNonPeople(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	for _, id := range []string{SystemSubject(org), "agent:" + org, ""} {
		f.setMember(id, MemberOwner)
		f.setGrants(id, ScopedRole{Role: RoleAdmin, TeamID: "team_a"})
		if err := f.a.Sync(ctx, org, id); !errors.Is(err, ErrForbidden) {
			t.Errorf("Sync(%q) = %v, want ErrForbidden", id, err)
		}
		if rows := f.assignments(t, id); len(rows) != 0 && id != "" {
			t.Errorf("Sync(%q) wrote %d user rows", id, len(rows))
		}
	}
	if members, grants := f.calls(); members != 0 || grants != 0 {
		t.Fatalf("a refused Sync looked up %d memberships and %d grants", members, grants)
	}
	all, err := f.store.ListAssignments(ctx, &assignment.ListFilter{TenantID: org, SubjectKind: string(warden.SubjectUser)})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("%d user rows written", len(all))
	}
}

// failingStore fails the next failCreates CreateAssignment calls, and runs
// beforeCreate ahead of every one, so a test can look at a sync midway.
type failingStore struct {
	Store
	mu           sync.Mutex
	failCreates  int
	beforeCreate func()
}

func (s *failingStore) CreateAssignment(ctx context.Context, as *assignment.Assignment) error {
	s.mu.Lock()
	before := s.beforeCreate
	fail := s.failCreates > 0
	if fail {
		s.failCreates--
	}
	s.mu.Unlock()
	if before != nil {
		before()
	}
	if fail {
		return errors.New("store is down")
	}
	return s.Store.CreateAssignment(ctx, as)
}

func (s *failingStore) failNext(n int, before func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCreates, s.beforeCreate = n, before
}

// memFailingFixture is memTeamFixture with a failingStore between the
// Authorizer and the memory store; the engine reads the store directly.
func memFailingFixture(t *testing.T) (*teamFixture, *failingStore) {
	t.Helper()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	fs := &failingStore{Store: store}
	return newTeamFixture(t, eng, fs, org), fs
}

// checkSyncFailsClosed runs the promotion and the demotion through a sync
// whose first create fails, on whatever store f and fs sit over.
func checkSyncFailsClosed(t *testing.T, f *teamFixture, fs *failingStore) {
	t.Helper()
	ctx := context.Background()
	allowed := func(user string, perm Permission) bool {
		t.Helper()
		ok, err := f.a.Allowed(ctx, f.person(user), perm)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	t.Run("promotion", func(t *testing.T) {
		f.setMember(alice, MemberMember)
		if err := f.a.Sync(ctx, f.org, alice); err != nil {
			t.Fatal(err)
		}
		f.setGrants(alice, ScopedRole{Role: RoleAdmin, TeamID: "team_ops"})
		fs.failNext(1, nil)
		if err := f.a.Sync(ctx, f.org, alice); err == nil {
			t.Fatal("Sync succeeded with a failing store")
		}
		if allowed(alice, ProjectRead) {
			t.Fatal("after a failed promotion the old role is still there: the sync created before it deleted")
		}
		if err := f.a.Sync(ctx, f.org, alice); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if got := f.rows(t, alice); !maps.Equal(got, map[string]string{"": RoleAdmin}) {
			t.Fatalf("after the retry %v, want admin", got)
		}
	})

	t.Run("demotion", func(t *testing.T) {
		f.setMember(bob, MemberMember)
		f.setGrants(bob, ScopedRole{Role: RoleAdmin, TeamID: "team_ops"})
		if err := f.a.Sync(ctx, f.org, bob); err != nil {
			t.Fatal(err)
		}
		if !allowed(bob, ProjectDelete) {
			t.Fatal("setup: the team admin can't delete")
		}
		f.setGrants(bob)
		var leaked []string
		watch := func() {
			if allowed(bob, ProjectDelete) {
				leaked = append(leaked, "project:delete still allowed while creating")
			}
		}
		fs.failNext(1, watch)
		if err := f.a.Sync(ctx, f.org, bob); err == nil {
			t.Fatal("Sync succeeded with a failing store")
		}
		if allowed(bob, ProjectDelete) {
			t.Fatal("after a failed demotion bob can still delete")
		}
		if err := f.a.Sync(ctx, f.org, bob); err != nil {
			t.Fatalf("retry: %v", err)
		}
		fs.failNext(0, nil)
		if len(leaked) != 0 {
			t.Fatal(leaked)
		}
		if allowed(bob, ProjectDelete) {
			t.Fatal("after the retry bob can still delete")
		}
		if got := f.rows(t, bob); !maps.Equal(got, map[string]string{"": RoleDeveloper}) {
			t.Fatalf("after the retry %v, want developer", got)
		}
	})
}

func TestSyncFailingMidwayFailsClosedAndConverges(t *testing.T) {
	f, fs := memFailingFixture(t)
	checkSyncFailsClosed(t, f, fs)
}

func TestAssignKeepsTeamRows(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	p := newProjectID(t)
	nsP := mustNamespace(t, p)
	f.setMember(alice, MemberViewer)
	f.setGrants(alice, ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_a"})
	if err := f.a.Sync(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Assign(ctx, org, alice, RoleViewer, "test"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	want := map[string]string{"": RoleViewer, nsP: RoleDeveloper}
	if got := f.rows(t, alice); !maps.Equal(got, want) {
		t.Fatalf("after Assign %v, want %v", got, want)
	}

	// The root row Assign creates names who granted it; the team rows name
	// the sync.
	f.setGrants(bob, ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_a"})
	if err := f.a.Assign(ctx, org, bob, RoleViewer, "authsome:member.add"); err != nil {
		t.Fatal(err)
	}
	if got := f.rowAt(t, bob, "").GrantedBy; got != "authsome:member.add" {
		t.Errorf("root row granted by %q", got)
	}
	if got := f.rowAt(t, bob, nsP).GrantedBy; got != "loomplane:sync" {
		t.Errorf("team row granted by %q", got)
	}
}

func TestRepairResyncsTeamRoles(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	p := newProjectID(t)
	f.setMember(alice, MemberViewer)
	f.setGrants(alice, ScopedRole{Role: RoleDeveloper, ProjectID: p, TeamID: "team_a"})
	// alice holds no rows at all: her hooks failed.
	if err := f.a.Require(ctx, f.person(alice), ProjectUpdate, p); err != nil {
		t.Fatalf("repair didn't bring back the team grant: %v", err)
	}
	if err := f.a.Require(ctx, f.person(alice), ProjectUpdate, newProjectID(t)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the repaired grant reaches another project: %v", err)
	}
}

func TestSyncTakesThePersonsLock(t *testing.T) {
	ctx := context.Background()
	f := memTeamFixture(t)
	f.setMember(alice, MemberViewer)
	var names []string
	lookupsOutside := 0
	f.a.SetSyncLock(func(ctx context.Context, name string, fn func(context.Context) error) error {
		names = append(names, name)
		before, _ := f.calls()
		err := fn(ctx)
		if after, _ := f.calls(); after == before {
			lookupsOutside++
		}
		return err
	})
	for range 2 {
		if err := f.a.Sync(ctx, org, alice); err != nil {
			t.Fatal(err)
		}
	}
	want := "loomplane-authz-sync:" + org + ":" + alice
	if !slices.Equal(names, []string{want, want}) {
		t.Fatalf("locks %v, want %q twice", names, want)
	}
	if lookupsOutside != 0 {
		t.Fatal("Sync looked the membership up outside the lock")
	}
}

func TestMemberRemoveLeavesTeamsThenRevokes(t *testing.T) {
	ctx := context.Background()
	orgID, userID := authid.NewOrgID(), authid.NewUserID()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	f := newTeamFixture(t, eng, store, orgID.String())
	user := userID.String()
	setup := func() {
		f.setMember(user, MemberViewer)
		f.setGrants(user, ScopedRole{Role: RoleAdmin, ProjectID: newProjectID(t), TeamID: "team_a"})
		if err := f.a.Sync(ctx, f.org, user); err != nil {
			t.Fatal(err)
		}
	}
	var events []string
	f.a.SetSyncLock(func(ctx context.Context, _ string, fn func(context.Context) error) error {
		events = append(events, "lock")
		return fn(ctx)
	})
	m := &organization.Member{OrgID: orgID, UserID: userID, Role: organization.RoleMember}

	for _, leaveErr := range []error{nil, errors.New("teams store is down")} {
		setup()
		events = nil
		s := NewSync(f.a)
		s.SetLeaver(func(_ context.Context, o, u string) error {
			events = append(events, "leave")
			if o != f.org || u != user {
				t.Errorf("leaver got %s %s", o, u)
			}
			if len(f.assignments(t, user)) == 0 {
				t.Error("Revoke ran before the leaver")
			}
			return leaveErr
		})
		err := s.OnBeforeMemberRemove(ctx, m)
		if leaveErr == nil && err != nil {
			t.Fatalf("member remove: %v", err)
		}
		if leaveErr != nil && !errors.Is(err, leaveErr) {
			t.Fatalf("member remove = %v, want the leaver's error", err)
		}
		if !slices.Equal(events, []string{"leave", "lock"}) {
			t.Fatalf("events %v, want the leaver then the locked revoke", events)
		}
		if rows := f.assignments(t, user); len(rows) != 0 {
			t.Fatalf("leaver error %v: %d rows left", leaveErr, len(rows))
		}
	}
}

func TestMemberRemoveRevokesWhenTheLockFails(t *testing.T) {
	ctx := context.Background()
	orgID, userID := authid.NewOrgID(), authid.NewUserID()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatal(err)
	}
	f := newTeamFixture(t, eng, store, orgID.String())
	user := userID.String()
	f.setMember(user, MemberAdmin)
	if err = f.a.Sync(ctx, f.org, user); err != nil {
		t.Fatal(err)
	}
	lockErr := errors.New("database is down")
	f.a.SetSyncLock(func(context.Context, string, func(context.Context) error) error { return lockErr })
	err = NewSync(f.a).OnBeforeMemberRemove(ctx, &organization.Member{OrgID: orgID, UserID: userID})
	if !errors.Is(err, lockErr) {
		t.Fatalf("member remove = %v, want the lock's error", err)
	}
	if rows := f.assignments(t, user); len(rows) != 0 {
		t.Fatalf("a failed lock left %d rows", len(rows))
	}
}

func TestProjectPermissionsAreTheProjectChecks(t *testing.T) {
	want := []Permission{
		ProjectRead, ProjectUpdate, ProjectDelete, EnvironmentRead, EnvironmentManage, AnalysisRun,
		VariableRead, VariableManage, DeploymentRead, DeploymentCreate, DeploymentApprove,
		InfrastructureRead, InfrastructureModify, BackupRead, BackupManage, RestoreManage,
		CloudAccountRead, CloudAccountManage, SecretManage, AIRead, AIEdit, AIManage,
	}
	got := ProjectPermissions()
	if !slices.Equal(got, want) {
		t.Fatalf("ProjectPermissions() = %v, want %v", got, want)
	}
	all := AllPermissions()
	for _, p := range got {
		if !slices.Contains(all, p) {
			t.Errorf("%s isn't in the catalog", p.Name())
		}
	}
	for _, p := range []Permission{MemberManage, OrganizationAdminister, BackupRetire, ClusterManage, ProjectCreate} {
		if slices.Contains(got, p) {
			t.Errorf("%s is a workspace permission", p.Name())
		}
	}
}

func TestRoleRank(t *testing.T) {
	tests := map[string]int{RoleOwner: 4, RoleAdmin: 3, RoleDeveloper: 2, RoleViewer: 1, RoleSystem: 0, "": 0, "superuser": 0}
	for slug, want := range tests {
		if got := RoleRank(slug); got != want {
			t.Errorf("RoleRank(%q) = %d, want %d", slug, got, want)
		}
	}
}
