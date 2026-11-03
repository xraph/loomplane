package security

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"
)

func TestAgentPrincipalJSONRoundTrips(t *testing.T) {
	p := AgentPrincipal("o", &Principal{UserID: "u", OrgID: "o", KeyID: "k"})
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Kind":"agent"`, `"OnBehalfOf":{`, `"UserID":"agent:o"`, `"UserID":"u"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("agent JSON %s lacks %s", raw, want)
		}
	}
	var back Principal
	if err = json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) {
		t.Fatalf("round trip = %+v, want %+v", back, p)
	}
	// People's JSON doesn't change: stored actors keep reading as before.
	got, err := json.Marshal(Principal{UserID: "u", OrgID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"UserID":"u","OrgID":"o","KeyID":"","ReadOnly":false}`; string(got) != want {
		t.Fatalf("person JSON = %s, want %s", got, want)
	}
}

func TestAgentSubjectRoundTrip(t *testing.T) {
	if got := AgentSubject("aorg_1"); got != "agent:aorg_1" {
		t.Fatalf("AgentSubject = %q", got)
	}
	if org, ok := IsAgentSubject("agent:aorg_1"); !ok || org != "aorg_1" {
		t.Fatalf("IsAgentSubject(agent:aorg_1) = %q, %v", org, ok)
	}
	for _, id := range []string{"usr_1", "agent:", "loomplane:aorg_1", "", "agent", "xagent:aorg_1"} {
		if org, ok := IsAgentSubject(id); ok {
			t.Errorf("IsAgentSubject(%q) = %q, true", id, org)
		}
	}
}

func TestAgentIsAServiceAccount(t *testing.T) {
	p := AgentPrincipal("o", nil)
	if want := (Principal{Kind: KindAgent, UserID: "agent:o", OrgID: "o"}); p != want {
		t.Fatalf("AgentPrincipal = %+v, want %+v", p, want)
	}
	if !p.IsAgent() || !p.IsServiceAccount() || p.IsSystem() {
		t.Fatalf("agent: IsAgent %v, IsServiceAccount %v, IsSystem %v", p.IsAgent(), p.IsServiceAccount(), p.IsSystem())
	}
	if SystemPrincipal("o").IsAgent() {
		t.Fatal("the automation reads as the agent")
	}
	if (Principal{UserID: "agent:o", OrgID: "o"}).IsAgent() {
		t.Fatal("a user principal with an agent-looking ID is the agent")
	}
	if _, ok := p.Person(); ok {
		t.Fatal("the agent alone acts for someone")
	}
	ada := Principal{UserID: "usr_ada", OrgID: "o"}
	got, ok := AgentPrincipal("o", &ada).Person()
	if !ok || got != ada {
		t.Fatalf("Person = %+v, %v, want %+v", got, ok, ada)
	}
	// A person, or the automation, never has someone behind it.
	if _, ok := (Principal{UserID: "u", OrgID: "o", OnBehalfOf: &ada}).Person(); ok {
		t.Fatal("a person acts for someone")
	}
}

func TestAgentPrincipalCopiesThePerson(t *testing.T) {
	ada := Principal{UserID: "usr_ada", OrgID: "o"}
	p := AgentPrincipal("o", &ada)
	ada.UserID = "usr_mallory"
	if got, _ := p.Person(); got.UserID != "usr_ada" {
		t.Fatalf("the agent's person changed with the caller's copy: %+v", got)
	}
}

func TestAgentPrincipalInheritsReadOnly(t *testing.T) {
	if !AgentPrincipal("o", &Principal{UserID: "u", OrgID: "o", ReadOnly: true}).ReadOnly {
		t.Fatal("a read-only person's agent can write")
	}
	if AgentPrincipal("o", &Principal{UserID: "u", OrgID: "o"}).ReadOnly {
		t.Fatal("a person's agent is read-only")
	}
}

func TestAgentPrincipalPanicsForNonPeople(t *testing.T) {
	for name, person := range map[string]Principal{
		"automation": SystemPrincipal("o"),
		"agent":      AgentPrincipal("o", nil),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			AgentPrincipal("o", &person)
		})
	}
}

func TestResolveRefusesAgentSubjects(t *testing.T) {
	for _, id := range []string{"agent:o", "agent:other"} {
		if _, err := Resolve(id, "o", ""); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Resolve(%q) = %v, want ErrUnauthenticated", id, err)
		}
	}
}

// subjectEngine is a Warden engine that answers by subject ID and
// remembers what it was asked.
type subjectEngine struct {
	mu     sync.Mutex
	allow  map[string]bool
	checks []warden.CheckRequest
}

func (e *subjectEngine) Check(_ context.Context, req *warden.CheckRequest, _ ...warden.CallOption) (*warden.CheckResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checks = append(e.checks, *req)
	if e.allow[req.Subject.ID] {
		return &warden.CheckResult{Allowed: true, Decision: warden.DecisionAllow}, nil
	}
	return &warden.CheckResult{Decision: warden.DecisionDeny}, nil
}

func (e *subjectEngine) InvalidateSubject(context.Context, string, warden.SubjectKind, string) {}

func TestAuthorizerChecksAgentsAsServiceAccounts(t *testing.T) {
	eng := &recordingEngine{result: warden.CheckResult{Allowed: true, Decision: warden.DecisionAllow}}
	a := NewAuthorizer(eng, memory.New(), nil)
	if err := a.Require(t.Context(), AgentPrincipal("o", nil), DeploymentApprove, "proj_1"); err != nil {
		t.Fatal(err)
	}
	if len(eng.checks) != 1 {
		t.Fatalf("%d checks, want 1", len(eng.checks))
	}
	if got := eng.checks[0].Subject; got.Kind != warden.SubjectServiceAcct || got.ID != "agent:o" {
		t.Fatalf("agent subject = %+v, want the service account agent:o", got)
	}
}

func TestAuthorizerOnBehalfNeedsBoth(t *testing.T) {
	ada := Principal{UserID: "usr_ada", OrgID: "o"}
	for name, tc := range map[string]struct {
		allow       map[string]bool
		p           Principal
		perm        Permission
		ok          bool
		mention     string
		wardenCalls int
		noWarden    bool
	}{
		"both allow": {
			allow: map[string]bool{"agent:o": true, "usr_ada": true}, p: AgentPrincipal("o", &ada),
			perm: DeploymentApprove, ok: true, wardenCalls: 2,
		},
		"the person lacks it": {
			allow: map[string]bool{"agent:o": true}, p: AgentPrincipal("o", &ada),
			perm: DeploymentApprove, mention: "person", wardenCalls: 2,
		},
		"the agent lacks it": {
			allow: map[string]bool{"usr_ada": true}, p: AgentPrincipal("o", &ada),
			perm: DeploymentApprove,
		},
		"a read-only person writes": {
			allow: map[string]bool{"agent:o": true, "usr_ada": true}, p: AgentPrincipal("o", &Principal{UserID: "usr_ada", OrgID: "o", ReadOnly: true}),
			perm: DeploymentApprove, noWarden: true,
		},
		"a read-only person behind a writable agent": {
			allow: map[string]bool{"agent:o": true, "usr_ada": true},
			p:     Principal{Kind: KindAgent, UserID: "agent:o", OrgID: "o", OnBehalfOf: &Principal{UserID: "usr_ada", OrgID: "o", ReadOnly: true}},
			perm:  DeploymentApprove, mention: "person", noWarden: true,
		},
		"a person in another organization": {
			allow: map[string]bool{"agent:o": true, "usr_ada": true},
			p:     Principal{Kind: KindAgent, UserID: "agent:o", OrgID: "o", OnBehalfOf: &Principal{UserID: "usr_ada", OrgID: "other"}},
			perm:  DeploymentApprove,
		},
		"the automation behind the agent": {
			allow: map[string]bool{"agent:o": true, "loomplane:o": true},
			p:     Principal{Kind: KindAgent, UserID: "agent:o", OrgID: "o", OnBehalfOf: &Principal{Kind: KindSystem, UserID: "loomplane:o", OrgID: "o"}},
			perm:  DeploymentApprove,
		},
		"another organization's agent": {
			allow: map[string]bool{"agent:other": true, "usr_ada": true},
			p:     Principal{Kind: KindAgent, UserID: "agent:other", OrgID: "o", OnBehalfOf: &ada},
			perm:  DeploymentRead,
		},
		"a read-only person reads": {
			allow: map[string]bool{"agent:o": true, "usr_ada": true}, p: AgentPrincipal("o", &Principal{UserID: "usr_ada", OrgID: "o", ReadOnly: true}),
			perm: DeploymentRead, ok: true, wardenCalls: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			eng := &subjectEngine{allow: tc.allow}
			a := NewAuthorizer(eng, memory.New(), nil)
			err := a.Require(t.Context(), tc.p, tc.perm, "proj_1")
			allowed, allowedErr := a.Allowed(t.Context(), tc.p, tc.perm)
			if allowedErr != nil {
				t.Fatalf("Allowed: %v", allowedErr)
			}
			if allowed != tc.ok {
				t.Fatalf("Allowed = %v, want %v", allowed, tc.ok)
			}
			if tc.ok {
				if err != nil {
					t.Fatalf("Require = %v, want nil", err)
				}
				if len(eng.checks) != 2*tc.wardenCalls {
					t.Fatalf("Warden was asked %d times, want %d", len(eng.checks), 2*tc.wardenCalls)
				}
				return
			}
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("Require = %v, want ErrForbidden", err)
			}
			if !strings.Contains(err.Error(), tc.perm.Name()) {
				t.Fatalf("Require = %v, doesn't name %s", err, tc.perm.Name())
			}
			if tc.mention != "" && !strings.Contains(err.Error(), tc.mention) {
				t.Fatalf("Require = %v, doesn't mention %q", err, tc.mention)
			}
			if tc.mention == "" && strings.Contains(err.Error(), "person") {
				t.Fatalf("Require = %v blames the person", err)
			}
			if tc.noWarden && len(eng.checks) != 0 {
				t.Fatalf("Warden was asked %d times, want none", len(eng.checks))
			}
		})
	}
}

func TestAuthorizerNeverRepairsAgents(t *testing.T) {
	eng := &recordingEngine{result: warden.CheckResult{Decision: warden.DecisionDenyNoRoles}}
	members := func(context.Context, string, string) (string, bool, error) {
		t.Error("the membership lookup ran for the agent")
		return MemberOwner, true, nil
	}
	a := NewAuthorizer(eng, memory.New(), members)
	if err := a.Require(t.Context(), AgentPrincipal("o", nil), DeploymentApprove, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Require = %v, want ErrForbidden", err)
	}
}

func TestBackupRetireRefusesAgents(t *testing.T) {
	eng := &recordingEngine{result: warden.CheckResult{Allowed: true, Decision: warden.DecisionAllow}}
	members := func(context.Context, string, string) (string, bool, error) {
		t.Error("the membership lookup ran for the agent")
		return MemberOwner, true, nil
	}
	a := NewAuthorizer(eng, memory.New(), members)
	owner := Principal{UserID: "usr_owner", OrgID: "o"}
	for _, p := range []Principal{AgentPrincipal("o", nil), AgentPrincipal("o", &owner)} {
		if err := a.Require(t.Context(), p, BackupRetire, ""); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Require = %v, want ErrForbidden", err)
		}
		if ok, err := a.Allowed(t.Context(), p, BackupRetire); ok || err != nil {
			t.Fatalf("Allowed = %v, %v", ok, err)
		}
	}
	if len(eng.checks) != 0 {
		t.Fatalf("Warden was asked %d times", len(eng.checks))
	}
}

func TestAssignAgentWritesOnlyThatTenant(t *testing.T) {
	ctx := t.Context()
	store := memory.New()
	eng := &recordingEngine{}
	a := NewAuthorizer(eng, store, nil)
	if err := a.EnsureTenant(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetRoleBySlug(ctx, "o", rootNamespace, RoleSystem); err != nil {
		t.Fatalf("EnsureTenant left out the automation's role: %v", err)
	}
	if _, err := store.GetRoleBySlug(ctx, "o", rootNamespace, RoleAgent); !errors.Is(err, warden.ErrNotFound) {
		t.Fatalf("EnsureTenant wrote the agent's role: %v", err)
	}

	if err := a.AssignAgent(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	r, err := store.GetRoleBySlug(ctx, "o", rootNamespace, RoleAgent)
	if err != nil {
		t.Fatalf("role %s: %v", RoleAgent, err)
	}
	refs, err := store.ListRolePermissions(ctx, "o", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	if len(names) != len(AgentRole().Permissions) {
		t.Fatalf("agent role holds %v, want %d permissions", names, len(AgentRole().Permissions))
	}
	for _, p := range AgentRole().Permissions {
		if !slices.Contains(names, p.Name()) {
			t.Fatalf("agent role holds %v, lacks %s", names, p.Name())
		}
	}
	all, err := store.ListAssignments(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var agent []string
	for _, as := range all {
		if as.SubjectID == "agent:o" {
			agent = append(agent, as.SubjectKind+":"+as.RoleID.String())
			if as.TenantID != "o" {
				t.Fatalf("assignment in tenant %q", as.TenantID)
			}
		}
	}
	if want := []string{"service_acct:" + r.ID.String()}; !slices.Equal(agent, want) {
		t.Fatalf("agent assignments %v, want %v", agent, want)
	}
	if !slices.Contains(eng.invalidated, "service_acct:agent:o") {
		t.Fatalf("invalidated %v, want the agent", eng.invalidated)
	}
	// No other tenant has the role.
	if _, err = store.GetRoleBySlug(ctx, "other", rootNamespace, RoleAgent); !errors.Is(err, warden.ErrNotFound) {
		t.Fatalf("another tenant has the agent's role: %v", err)
	}

	// A second AssignAgent changes nothing.
	before, _ := store.ListAssignments(ctx, nil)
	roles, _ := store.ListRoles(ctx, nil)
	if err = a.AssignAgent(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	after, _ := store.ListAssignments(ctx, nil)
	rolesAfter, _ := store.ListRoles(ctx, nil)
	if len(after) != len(before) || len(rolesAfter) != len(roles) {
		t.Fatalf("second AssignAgent: assignments %d to %d, roles %d to %d", len(before), len(after), len(roles), len(rolesAfter))
	}
	kept := map[string]bool{}
	for _, as := range before {
		kept[as.ID.String()] = true
	}
	for _, as := range after {
		if !kept[as.ID.String()] {
			t.Fatalf("second AssignAgent made assignment %s", as.ID)
		}
	}

	// A narrower role from an older release grows back on the next enable.
	if err = store.SetRolePermissions(ctx, "o", r.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = a.AssignAgent(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	if refs, _ = store.ListRolePermissions(ctx, "o", r.ID); len(refs) != len(AgentRole().Permissions) {
		t.Fatalf("after AssignAgent the role holds %d permissions, want %d", len(refs), len(AgentRole().Permissions))
	}

	eng.invalidated = nil
	if err = a.RevokeAgent(ctx, "o"); err != nil {
		t.Fatal(err)
	}
	held, err := store.ListRolesForSubject(ctx, "o", []string{rootNamespace}, string(warden.SubjectServiceAcct), "agent:o")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("after RevokeAgent the agent holds %v", held)
	}
	if !slices.Contains(eng.invalidated, "service_acct:agent:o") {
		t.Fatalf("invalidated %v, want the agent", eng.invalidated)
	}
	// The automation keeps its role.
	if held, _ = store.ListRolesForSubject(ctx, "o", []string{rootNamespace}, string(warden.SubjectServiceAcct), "loomplane:o"); len(held) != 1 {
		t.Fatalf("after RevokeAgent the automation holds %v", held)
	}
}

func TestAssignAgentNeedsAnOrganization(t *testing.T) {
	a := NewAuthorizer(&recordingEngine{}, memory.New(), nil)
	if err := a.AssignAgent(t.Context(), ""); err == nil {
		t.Fatal("AssignAgent took an empty organization")
	}
	if err := a.RevokeAgent(t.Context(), ""); err == nil {
		t.Fatal("RevokeAgent took an empty organization")
	}
}

func TestAgentRoleHasNoAdminPermissions(t *testing.T) {
	got := AgentRole().Permissions
	for _, p := range []Permission{DeploymentApprove, DeploymentCreate, ProjectCreate, VariableManage, AgentUse, AIEdit, InfrastructureModify} {
		if !slices.Contains(got, p) {
			t.Errorf("AgentRole lacks %s", p.Name())
		}
	}
	for _, p := range []Permission{
		SecretManage, RepositoryConnect, CloudAccountManage, OrganizationCloudManage, ProjectDelete,
		MemberManage, OrganizationAdminister, BackupRetire, AIManage, AIToolSecret, AIPublishNonProduction,
	} {
		if slices.Contains(got, p) {
			t.Errorf("AgentRole holds %s", p.Name())
		}
	}
	// Of everything above a developer, the agent holds deployment approval,
	// and infrastructure:modify only for running a deployment it approved.
	developer := rolePermissions(t, RoleDeveloper)
	for _, p := range got {
		if !slices.Contains(developer, p) && p != DeploymentApprove && !deploymentRunOnly(p) {
			t.Errorf("AgentRole holds %s, which developers don't", p.Name())
		}
	}
	if !deploymentRunOnly(InfrastructureModify) {
		t.Error("the agent modifies infrastructure outside a deployment run")
	}
	for _, p := range AllPermissions() {
		if p != InfrastructureModify && deploymentRunOnly(p) {
			t.Errorf("%s is limited to deployment runs", p.Name())
		}
	}
	seen := map[Permission]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("AgentRole lists %s twice", p.Name())
		}
		seen[p] = true
	}
	if AgentRole().Slug != RoleAgent || RoleAgent != "agent" {
		t.Fatalf("slug %q", AgentRole().Slug)
	}
}

func TestAssignRefusesTheAgentRole(t *testing.T) {
	a := newAuthorizer(t, nil)
	if err := a.Assign(t.Context(), org, alice, RoleAgent, "test"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Assign gave a person the agent's role: %v", err)
	}
}

// TestAgentThroughWarden runs the agent through Warden's real engine: it
// holds nothing until a workspace turns it on, then its role, and acting
// for a person, no more than that person.
func TestAgentThroughWarden(t *testing.T) {
	ctx := t.Context()
	a := newAuthorizer(t, map[string]string{alice: MemberOwner, bob: MemberViewer})
	agent := AgentPrincipal(org, nil)
	if err := a.Require(ctx, agent, DeploymentRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an agent that isn't on: %v, want ErrForbidden", err)
	}
	if err := a.AssignAgent(ctx, org); err != nil {
		t.Fatal(err)
	}
	run := WithDeploymentRun(ctx)
	for _, perm := range AgentRole().Permissions {
		if err := a.Require(run, agent, perm, "proj_1"); err != nil {
			t.Errorf("agent can't %s: %v", perm.Name(), err)
		}
		if err := a.Require(ctx, agent, perm, "proj_1"); (err != nil) != deploymentRunOnly(perm) {
			t.Errorf("agent %s outside a deployment run: %v", perm.Name(), err)
		}
	}
	for _, perm := range []Permission{ProjectDelete, MemberManage, SecretManage, InfrastructureModify, AIManage} {
		if err := a.Require(ctx, agent, perm, "proj_1"); !errors.Is(err, ErrForbidden) {
			t.Errorf("agent %s: %v, want ErrForbidden", perm.Name(), err)
		}
	}
	// For a viewer, the agent reads but doesn't deploy.
	forBob := AgentPrincipal(org, &Principal{UserID: bob, OrgID: org})
	if err := a.Require(ctx, forBob, DeploymentRead, "proj_1"); err != nil {
		t.Fatalf("agent for a viewer can't read: %v", err)
	}
	if err := a.Require(ctx, forBob, AgentUse, ""); err != nil {
		t.Fatalf("agent for a viewer can't be used: %v", err)
	}
	if err := a.Require(ctx, forBob, DeploymentCreate, "proj_1"); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "person") {
		t.Fatalf("agent for a viewer deploys: %v", err)
	}
	// For an owner, the agent still can't do what its own role can't.
	forAlice := AgentPrincipal(org, &Principal{UserID: alice, OrgID: org})
	if err := a.Require(ctx, forAlice, DeploymentApprove, "proj_1"); err != nil {
		t.Fatalf("agent for an owner can't approve: %v", err)
	}
	if err := a.Require(ctx, forAlice, SecretManage, "proj_1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent for an owner sets secrets: %v", err)
	}
	// The same ID as a person holds nothing.
	if err := a.Require(ctx, Principal{UserID: AgentSubject(org), OrgID: org}, DeploymentRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user principal with the agent's ID: %v, want ErrForbidden", err)
	}
	// Another organization's agent holds nothing here, even after turning on.
	if err := a.AssignAgent(ctx, "aorg_other"); err != nil {
		t.Fatal(err)
	}
	if err := a.Require(ctx, Principal{Kind: KindAgent, UserID: AgentSubject("aorg_other"), OrgID: org}, DeploymentRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another org's agent: %v, want ErrForbidden", err)
	}
	if err := a.RevokeAgent(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := a.Require(ctx, agent, DeploymentRead, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a revoked agent: %v, want ErrForbidden", err)
	}
}

func TestAgentCallContext(t *testing.T) {
	if _, ok := AgentCallFrom(t.Context()); ok {
		t.Fatal("an empty context has an agent call")
	}
	want := AgentCall{TaskID: "atask_1", RunID: "arun_1", Tool: "approve_plan", CallID: "call_1"}
	got, ok := AgentCallFrom(WithAgentCall(t.Context(), want))
	if !ok || got != want {
		t.Fatalf("AgentCallFrom = %+v, %v, want %+v", got, ok, want)
	}
}

// TestAgentModifiesInfrastructureOnlyInADeploymentRun: the agent holds
// infrastructure:modify for the deployments it approved, which run as it
// alone, and the Authorizer refuses it everywhere else.
func TestAgentModifiesInfrastructureOnlyInADeploymentRun(t *testing.T) {
	ctx := t.Context()
	run := WithDeploymentRun(ctx)
	if InDeploymentRun(ctx) || !InDeploymentRun(run) {
		t.Fatal("the deployment run marker")
	}
	a := newAuthorizer(t, map[string]string{alice: MemberOwner, bob: MemberViewer})
	if err := a.AssignAgent(ctx, org); err != nil {
		t.Fatal(err)
	}
	agent := AgentPrincipal(org, nil)
	err := a.Require(ctx, agent, InfrastructureModify, "proj_1")
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), InfrastructureModify.Name()) {
		t.Fatalf("agent outside a deployment run: %v, want ErrForbidden naming the permission", err)
	}
	if ok, allowedErr := a.Allowed(ctx, agent, InfrastructureModify); ok || allowedErr != nil {
		t.Fatalf("Allowed outside a deployment run = %v, %v", ok, allowedErr)
	}
	if err = a.Require(run, agent, InfrastructureModify, "proj_1"); err != nil {
		t.Fatalf("agent in a deployment run: %v", err)
	}
	if ok, allowedErr := a.Allowed(run, agent, InfrastructureModify); !ok || allowedErr != nil {
		t.Fatalf("Allowed in a deployment run = %v, %v", ok, allowedErr)
	}
	// Reading needs no marker.
	if err = a.Require(ctx, agent, InfrastructureRead, "proj_1"); err != nil {
		t.Fatalf("agent reads infrastructure: %v", err)
	}
	// Acting for a person, the person must hold it too, marker or not.
	if err = a.Require(run, AgentPrincipal(org, &Principal{UserID: bob, OrgID: org}), InfrastructureModify, "proj_1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent for a viewer in a run: %v, want ErrForbidden", err)
	}
	if err = a.Require(ctx, AgentPrincipal(org, &Principal{UserID: alice, OrgID: org}), InfrastructureModify, "proj_1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent for an owner outside a run: %v, want ErrForbidden", err)
	}
	// People and the automation don't need or gain anything from it.
	for _, c := range []context.Context{ctx, run} {
		if err = a.Require(c, Principal{UserID: alice, OrgID: org}, InfrastructureModify, "proj_1"); err != nil {
			t.Fatalf("owner: %v", err)
		}
		if err = a.Require(c, Principal{UserID: bob, OrgID: org}, InfrastructureModify, "proj_1"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("viewer: %v, want ErrForbidden", err)
		}
		if err = a.Require(c, SystemPrincipal(org), InfrastructureModify, "proj_1"); err != nil {
			t.Fatalf("automation: %v", err)
		}
	}
	// Turned off, it holds nothing, in a run or not.
	if err = a.RevokeAgent(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = a.Require(run, agent, InfrastructureModify, "proj_1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked agent in a run: %v, want ErrForbidden", err)
	}
}
