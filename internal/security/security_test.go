package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/organization"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
)

const (
	org   = "aorg_test"
	alice = "ausr_alice"
	bob   = "ausr_bob"
)

// newAuthorizer returns an Authorizer over Warden's real engine and its
// in-memory store, with members as authsome's membership.
func newAuthorizer(t *testing.T, members map[string]string) *Authorizer {
	t.Helper()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatalf("warden: %v", err)
	}
	lookup := func(_ context.Context, orgID, userID string) (string, bool, error) {
		if orgID != org {
			return "", false, nil
		}
		r, ok := members[userID]
		return r, ok, nil
	}
	return NewAuthorizer(eng, store, lookup)
}

func TestCatalog_rolesNest(t *testing.T) {
	cat := Catalog()
	has := map[string]map[Permission]bool{}
	for _, r := range cat {
		has[r.Slug] = map[Permission]bool{}
		for _, p := range r.Permissions {
			if has[r.Slug][p] {
				t.Errorf("role %s lists %s twice", r.Slug, p.Name())
			}
			has[r.Slug][p] = true
		}
	}
	// Each role includes everything the one below it may do.
	order := []string{RoleViewer, RoleDeveloper, RoleAdmin, RoleOwner}
	for i := 1; i < len(order); i++ {
		for p := range has[order[i-1]] {
			if !has[order[i]][p] {
				t.Errorf("%s lacks %s, which %s has", order[i], p.Name(), order[i-1])
			}
		}
	}
	if has[RoleViewer][ProjectCreate] || has[RoleDeveloper][ProjectDelete] || has[RoleAdmin][OrganizationAdminister] {
		t.Error("a role holds a permission above its rank")
	}
}

func TestRoleForMember(t *testing.T) {
	tests := map[string]string{
		"owner": RoleOwner, "admin": RoleAdmin, "member": RoleDeveloper,
		"viewer": RoleViewer, "": RoleViewer, "OWNER": RoleViewer, "superuser": RoleViewer,
	}
	for in, want := range tests {
		if got := RoleForMember(in); got != want {
			t.Errorf("RoleForMember(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name              string
		user, org, header string
		wantErr           error
		wantStatus        int
	}{
		{"signed in", alice, org, "", nil, 0},
		{"matching tab", alice, org, org, nil, 0},
		{"anonymous", "", org, "", ErrUnauthenticated, http.StatusUnauthorized},
		{"no organization", alice, "", "", ErrNoOrganization, http.StatusForbidden},
		{"stale tab", alice, org, "aorg_other", ErrOrgMismatch, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Resolve(tt.user, tt.org, tt.header)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				if got := StatusFor(err); got != tt.wantStatus {
					t.Fatalf("status %d, want %d", got, tt.wantStatus)
				}
				return
			}
			if p.UserID != tt.user || p.OrgID != tt.org {
				t.Fatalf("principal %+v", p)
			}
		})
	}
}

func TestStatusFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"wrapped forbidden", fmt.Errorf("%w (project:delete)", ErrForbidden), http.StatusForbidden},
		// The dashboard reads any 403 on the session as "pick an organization".
		{"removed member", ErrNotMember, http.StatusForbidden},
		{"anything else", errors.New("database is down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StatusFor(tt.err); got != tt.want {
				t.Fatalf("StatusFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestAuthorizer_rolesDecide(t *testing.T) {
	ctx := context.Background()
	a := newAuthorizer(t, nil)
	if err := a.Assign(ctx, org, alice, RoleDeveloper, "test"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	p := Principal{UserID: alice, OrgID: org}
	if err := a.Require(ctx, p, ProjectCreate, ""); err != nil {
		t.Fatalf("developer can't create a project: %v", err)
	}
	if err := a.Require(ctx, p, ProjectDelete, "proj_1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("developer deleting a project: %v, want ErrForbidden", err)
	}
	// The same user has no rights in another organization.
	if err := a.Require(ctx, Principal{UserID: alice, OrgID: "aorg_other"}, ProjectRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reading another org: %v, want ErrForbidden", err)
	}
}

func TestAuthorizer_assignReplacesRole(t *testing.T) {
	ctx := context.Background()
	a := newAuthorizer(t, nil)
	p := Principal{UserID: alice, OrgID: org}
	for _, step := range []struct {
		role      string
		canDelete bool
	}{{RoleAdmin, true}, {RoleViewer, false}, {RoleAdmin, true}} {
		if err := a.Assign(ctx, org, alice, step.role, "test"); err != nil {
			t.Fatalf("Assign %s: %v", step.role, err)
		}
		ok, err := a.Allowed(ctx, p, ProjectDelete)
		if err != nil {
			t.Fatal(err)
		}
		if ok != step.canDelete {
			t.Fatalf("as %s, project:delete allowed = %v", step.role, ok)
		}
	}
	if err := a.Revoke(ctx, org, alice); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Allowed(ctx, p, ProjectRead); ok { // a denial is what's checked
		t.Fatal("revoked user can still read")
	}
}

func TestAuthorizer_ensureTenantIsIdempotent(t *testing.T) {
	ctx := context.Background()
	a := newAuthorizer(t, nil)
	for range 2 {
		if err := a.EnsureTenant(ctx, org); err != nil {
			t.Fatalf("EnsureTenant: %v", err)
		}
		// A fresh Authorizer over the same store has no memory of the first
		// call, so it really writes the catalog again.
		a.ensured.Delete(org)
	}
	roles, err := a.store.ListRoles(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != len(Catalog()) {
		t.Fatalf("%d roles after two ensures, want %d", len(roles), len(Catalog()))
	}
}

func TestAuthorizer_repairsMemberWithoutRole(t *testing.T) {
	ctx := context.Background()
	a := newAuthorizer(t, map[string]string{alice: "member"})
	// alice is a member in authsome but has no Warden role: the sync hook
	// failed, or she joined before Loomplane ran.
	if err := a.Require(ctx, Principal{UserID: alice, OrgID: org}, ProjectCreate, ""); err != nil {
		t.Fatalf("member wasn't repaired: %v", err)
	}
	// bob isn't a member, so there's nothing to repair.
	if err := a.Require(ctx, Principal{UserID: bob, OrgID: org}, ProjectRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member: %v, want ErrForbidden", err)
	}
}

func TestSync_followsMembership(t *testing.T) {
	ctx := context.Background()
	a := newAuthorizer(t, nil)
	s := NewSync(a)
	orgID := authid.NewOrgID()
	owner, member := authid.NewUserID(), authid.NewUserID()

	if err := s.OnAfterOrgCreate(ctx, &organization.Organization{ID: orgID, CreatedBy: owner}); err != nil {
		t.Fatalf("org create: %v", err)
	}
	m := &organization.Member{OrgID: orgID, UserID: member, Role: organization.RoleMember}
	if err := s.OnAfterMemberAdd(ctx, m); err != nil {
		t.Fatalf("member add: %v", err)
	}

	allowed := func(user authid.UserID, perm Permission) bool {
		t.Helper()
		ok, err := a.Allowed(ctx, Principal{UserID: user.String(), OrgID: orgID.String()}, perm)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !allowed(owner, OrganizationAdminister) {
		t.Error("creator isn't the owner")
	}
	if !allowed(member, ProjectCreate) || allowed(member, ProjectDelete) {
		t.Error("member isn't a developer")
	}

	m.Role = organization.RoleAdmin
	if err := s.OnAfterMemberRoleChange(ctx, m); err != nil {
		t.Fatalf("role change: %v", err)
	}
	if !allowed(member, ProjectDelete) {
		t.Error("promoted member can't delete")
	}

	if err := s.OnBeforeMemberRemove(ctx, m); err != nil {
		t.Fatalf("member remove: %v", err)
	}
	if allowed(member, ProjectRead) {
		t.Error("removed member can still read")
	}
}

func TestTokenEncryptor(t *testing.T) {
	enc, err := TokenEncryptor("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("TokenEncryptor: %v", err)
	}
	if enc == nil {
		t.Fatal("no encryptor")
	}
	if _, err := TokenEncryptor("not base64!"); err == nil {
		t.Fatal("accepted a key that isn't base64")
	}
}

func TestMemberRoleFor_roundTrips(t *testing.T) {
	for _, role := range []string{RoleOwner, RoleAdmin, RoleDeveloper, RoleViewer} {
		member, err := MemberRoleFor(role)
		if err != nil {
			t.Fatalf("MemberRoleFor(%q): %v", role, err)
		}
		if got := RoleForMember(member); got != role {
			t.Errorf("%q maps to member role %q, which maps back to %q", role, member, got)
		}
	}
	if _, err := MemberRoleFor("superuser"); err == nil {
		t.Error("accepted an unknown role")
	}
}

func TestMayGrant(t *testing.T) {
	tests := []struct {
		caller, granted string
		want            bool
	}{
		{MemberOwner, MemberOwner, true},
		{MemberAdmin, MemberAdmin, true},
		{MemberAdmin, MemberOwner, false},
		{MemberAdmin, MemberViewer, true},
		{MemberMember, MemberAdmin, false},
		{MemberViewer, MemberMember, false},
		{"superuser", MemberViewer, true},
		{"superuser", MemberMember, false},
	}
	for _, tt := range tests {
		if got := MayGrant(tt.caller, tt.granted); got != tt.want {
			t.Errorf("MayGrant(%q, %q) = %v, want %v", tt.caller, tt.granted, got, tt.want)
		}
	}
}
