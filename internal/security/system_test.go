package security

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
)

func TestPrincipalJSONIsUnchangedForPeople(t *testing.T) {
	got, err := json.Marshal(Principal{UserID: "u", OrgID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"UserID":"u","OrgID":"o","KeyID":"","ReadOnly":false}`; string(got) != want {
		t.Fatalf("person JSON = %s, want %s", got, want)
	}
}

func TestSystemPrincipal(t *testing.T) {
	p := SystemPrincipal("o")
	if want := (Principal{Kind: KindSystem, UserID: "loomplane:o", OrgID: "o"}); p != want {
		t.Fatalf("SystemPrincipal = %+v, want %+v", p, want)
	}
	if !p.IsSystem() || !p.IsServiceAccount() {
		t.Fatalf("system principal: IsSystem %v, IsServiceAccount %v", p.IsSystem(), p.IsServiceAccount())
	}
	person := Principal{UserID: "usr_1", OrgID: "o"}
	if person.IsSystem() || person.IsServiceAccount() {
		t.Fatal("a person reads as a service account")
	}
	// A person whose ID merely looks like the subject is still a person:
	// only Kind makes a principal the automation.
	if (Principal{UserID: "loomplane:o", OrgID: "o"}).IsServiceAccount() {
		t.Fatal("a user principal with a system-looking ID is a service account")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"Kind":"system"`) {
		t.Fatalf("system JSON %s lacks its kind", raw)
	}
	var back Principal
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back != p {
		t.Fatalf("round trip = %+v, want %+v", back, p)
	}
}

func TestSystemSubjectRoundTrip(t *testing.T) {
	if got := SystemSubject("o"); got != "loomplane:o" {
		t.Fatalf("SystemSubject = %q", got)
	}
	if org, ok := IsSystemSubject("loomplane:o"); !ok || org != "o" {
		t.Fatalf("IsSystemSubject(loomplane:o) = %q, %v", org, ok)
	}
	for _, id := range []string{"usr_1", "loomplane:", "agent:o", "", "loomplane", "xloomplane:o"} {
		if org, ok := IsSystemSubject(id); ok {
			t.Errorf("IsSystemSubject(%q) = %q, true", id, org)
		}
	}
}

func TestActorPrincipal(t *testing.T) {
	if got := ActorPrincipal("o", "loomplane:o"); got != SystemPrincipal("o") {
		t.Fatalf("automation actor = %+v", got)
	}
	if got, want := ActorPrincipal("o", "usr_1"), (Principal{OrgID: "o", UserID: "usr_1"}); got != want {
		t.Fatalf("person actor = %+v, want %+v", got, want)
	}
	// Another organization's automation never acts here as the automation.
	other := ActorPrincipal("o", "loomplane:other")
	if other.IsSystem() || other.IsServiceAccount() || other.Kind != KindUser {
		t.Fatalf("org mismatch gave %+v", other)
	}
}

func TestResolveRefusesSystemSubjects(t *testing.T) {
	for _, id := range []string{"loomplane:o", "loomplane:other"} {
		if _, err := Resolve(id, "o", ""); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Resolve(%q) = %v, want ErrUnauthenticated", id, err)
		}
	}
}

// recordingEngine is a Warden engine that answers every check with one
// result and remembers what it was asked.
type recordingEngine struct {
	mu          sync.Mutex
	result      warden.CheckResult
	checks      []warden.CheckRequest
	invalidated []string // kind:id
}

func (e *recordingEngine) Check(_ context.Context, req *warden.CheckRequest, _ ...warden.CallOption) (*warden.CheckResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checks = append(e.checks, *req)
	res := e.result
	return &res, nil
}

func (e *recordingEngine) InvalidateSubject(_ context.Context, _ string, kind warden.SubjectKind, id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.invalidated = append(e.invalidated, string(kind)+":"+id)
}

func TestAuthorizerChecksServiceAccounts(t *testing.T) {
	ctx := context.Background()
	eng := &recordingEngine{result: warden.CheckResult{Allowed: true, Decision: warden.DecisionAllow}}
	a := NewAuthorizer(eng, memory.New(), nil)
	if err := a.Require(ctx, SystemPrincipal("o"), DeploymentApprove, "proj_1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Require(ctx, Principal{UserID: "usr_1", OrgID: "o"}, DeploymentApprove, "proj_1"); err != nil {
		t.Fatal(err)
	}
	if len(eng.checks) != 2 {
		t.Fatalf("%d checks, want 2", len(eng.checks))
	}
	if got := eng.checks[0].Subject; got.Kind != warden.SubjectServiceAcct || got.ID != "loomplane:o" {
		t.Errorf("system subject = %+v, want the service account loomplane:o", got)
	}
	if got := eng.checks[1].Subject; got.Kind != warden.SubjectUser || got.ID != "usr_1" {
		t.Errorf("person subject = %+v, want the user usr_1", got)
	}
}

func TestAuthorizerNeverRepairsServiceAccounts(t *testing.T) {
	ctx := context.Background()
	eng := &recordingEngine{result: warden.CheckResult{Decision: warden.DecisionDenyNoRoles}}
	members := func(context.Context, string, string) (string, bool, error) {
		t.Error("the membership lookup ran for the automation")
		return MemberOwner, true, nil
	}
	a := NewAuthorizer(eng, memory.New(), members)
	if err := a.Require(ctx, SystemPrincipal("o"), DeploymentApprove, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Require = %v, want ErrForbidden", err)
	}
}

func TestBackupRetireRefusesServiceAccounts(t *testing.T) {
	ctx := context.Background()
	eng := &recordingEngine{result: warden.CheckResult{Allowed: true, Decision: warden.DecisionAllow}}
	members := func(context.Context, string, string) (string, bool, error) {
		t.Error("the membership lookup ran for the automation")
		return MemberOwner, true, nil
	}
	a := NewAuthorizer(eng, memory.New(), members)
	if err := a.Require(ctx, SystemPrincipal("o"), BackupRetire, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Require = %v, want ErrForbidden", err)
	}
	if ok, err := a.Allowed(ctx, SystemPrincipal("o"), BackupRetire); ok || err != nil {
		t.Fatalf("Allowed = %v, %v", ok, err)
	}
	if len(eng.checks) != 0 {
		t.Fatalf("Warden was asked %d times", len(eng.checks))
	}
}

func TestEnsureTenantAssignsTheSystemRole(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	eng := &recordingEngine{}
	a := NewAuthorizer(eng, store, nil)
	if err := a.EnsureTenant(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetRoleBySlug(ctx, "o", rootNamespace, RoleSystem)
	if err != nil {
		t.Fatalf("role %s: %v", RoleSystem, err)
	}
	assertRolePermissions(t, store, r.ID.String(), SystemRole().Permissions)
	held, err := store.ListRolesForSubject(ctx, "o", []string{rootNamespace}, string(warden.SubjectServiceAcct), "loomplane:o")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0] != r.ID {
		t.Fatalf("automation holds %v, want only %s", held, r.ID)
	}
	if !slices.Contains(eng.invalidated, "service_acct:loomplane:o") {
		t.Fatalf("invalidated %v, want the automation", eng.invalidated)
	}

	// An older Loomplane left a narrower role and a stray extra one.
	if err = store.SetRolePermissions(ctx, "o", r.ID, nil); err != nil {
		t.Fatal(err)
	}
	viewer, err := store.GetRoleBySlug(ctx, "o", rootNamespace, RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.assign(ctx, "o", warden.SubjectServiceAcct, "loomplane:o", RoleViewer, "test"); err != nil {
		t.Fatal(err)
	}
	if held, _ = store.ListRolesForSubject(ctx, "o", []string{rootNamespace}, string(warden.SubjectServiceAcct), "loomplane:o"); len(held) != 1 || held[0] != viewer.ID {
		t.Fatalf("setup: automation holds %v", held)
	}
	a.ensured.Delete("o")
	if err := a.EnsureTenant(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	assertRolePermissions(t, store, r.ID.String(), SystemRole().Permissions)
	held, _ = store.ListRolesForSubject(ctx, "o", []string{rootNamespace}, string(warden.SubjectServiceAcct), "loomplane:o")
	if len(held) != 1 || held[0] != r.ID {
		t.Fatalf("after an upgrade the automation holds %v, want only %s", held, r.ID)
	}

	// A second ensure changes nothing.
	before, _ := store.ListAssignments(ctx, nil)
	roles, _ := store.ListRoles(ctx, nil)
	a.ensured.Delete("o")
	if err := a.EnsureTenant(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	after, _ := store.ListAssignments(ctx, nil)
	rolesAfter, _ := store.ListRoles(ctx, nil)
	if len(after) != len(before) || len(rolesAfter) != len(roles) {
		t.Fatalf("second ensure: assignments %d to %d, roles %d to %d", len(before), len(after), len(roles), len(rolesAfter))
	}
	kept := map[string]bool{}
	for _, as := range before {
		kept[as.ID.String()] = true
	}
	for _, as := range after {
		if !kept[as.ID.String()] {
			t.Fatalf("second ensure made assignment %s", as.ID)
		}
	}
}

// TestSystemPrincipalThroughWarden runs the automation through Warden's
// real engine: it may approve and modify infrastructure, nothing more.
func TestSystemPrincipalThroughWarden(t *testing.T) {
	ctx := context.Background()
	a := newAuthorizer(t, nil)
	p := SystemPrincipal(org)
	for _, perm := range SystemRole().Permissions {
		if err := a.Require(ctx, p, perm, "proj_1"); err != nil {
			t.Errorf("automation can't %s: %v", perm.Name(), err)
		}
	}
	for _, perm := range []Permission{ProjectDelete, MemberManage, SecretManage, CloudAccountManage, OrganizationAdminister, BackupManage, VariableManage} {
		if err := a.Require(ctx, p, perm, "proj_1"); !errors.Is(err, ErrForbidden) {
			t.Errorf("automation %s: %v, want ErrForbidden", perm.Name(), err)
		}
	}
	// The same ID as a person holds nothing: the role is the service
	// account's, never a user's.
	if err := a.Require(ctx, Principal{UserID: SystemSubject(org), OrgID: org}, DeploymentRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user principal with the automation's ID: %v, want ErrForbidden", err)
	}
	// Another organization's automation holds nothing here.
	if err := a.Require(ctx, Principal{Kind: KindSystem, UserID: SystemSubject("aorg_other"), OrgID: org}, DeploymentRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another org's automation: %v, want ErrForbidden", err)
	}
}

func TestSystemRoleIsNarrow(t *testing.T) {
	want := []Permission{DeploymentRead, DeploymentCreate, DeploymentApprove, ProjectRead, EnvironmentRead, InfrastructureRead, InfrastructureModify}
	got := SystemRole().Permissions
	if len(got) != len(want) {
		t.Fatalf("SystemRole has %d permissions, want %d", len(got), len(want))
	}
	for _, p := range want {
		if !slices.Contains(got, p) {
			t.Errorf("SystemRole lacks %s", p.Name())
		}
	}
	for _, p := range AllPermissions() {
		if !slices.Contains(want, p) && slices.Contains(got, p) {
			t.Errorf("SystemRole holds %s", p.Name())
		}
	}
	if SystemRole().Slug != RoleSystem || RoleSystem != "loomplane-automation" {
		t.Fatalf("slug %q", SystemRole().Slug)
	}
}

func TestCatalogHasNoSystemMemberRole(t *testing.T) {
	for _, r := range Catalog() {
		if r.Slug == RoleSystem {
			t.Fatal("the member catalog has the automation's role")
		}
	}
	for _, m := range []string{"owner", "admin", "member", "viewer", "", RoleSystem, "loomplane-automation"} {
		if RoleForMember(m) == RoleSystem {
			t.Fatalf("RoleForMember(%q) is the automation's role", m)
		}
	}
	if _, err := MemberRoleFor(RoleSystem); err == nil {
		t.Fatal("MemberRoleFor accepted the automation's role")
	}
	a := newAuthorizer(t, nil)
	if err := a.Assign(context.Background(), org, alice, RoleSystem, "test"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Assign gave a person the automation's role: %v", err)
	}
}

func assertRolePermissions(t *testing.T, store *memory.Store, roleID string, want []Permission) {
	t.Helper()
	r, err := store.GetRoleBySlug(context.Background(), "o", rootNamespace, RoleSystem)
	if err != nil || r.ID.String() != roleID {
		t.Fatalf("role %s: %v", roleID, err)
	}
	refs, err := store.ListRolePermissions(context.Background(), "o", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	if len(names) != len(want) {
		t.Fatalf("role holds %v, want %d permissions", names, len(want))
	}
	for _, p := range want {
		if !slices.Contains(names, p.Name()) {
			t.Fatalf("role holds %v, lacks %s", names, p.Name())
		}
	}
}
