//go:build integration

package teams

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/security"
)

const (
	cy  = "ausr_cy"
	dee = "ausr_dee"
)

// routeRig adds Cy and Dee to org o and builds teams Payments (Cy), Ops
// (Dee, proj_a's responsible team) and Empty (no one, proj_b's).
type routeRig struct {
	*rig
	payments, ops, empty *Team
}

func newRouteRig(t *testing.T) *routeRig {
	t.Helper()
	r := newRig(t)
	r.exec(`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES
		('ausr_cy','app','env','cy@example.com','Cy',''),('ausr_dee','app','env','dee@example.com','Dee','')`)
	r.exec(`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES
		('amem_cy','o','ausr_cy','developer'),('amem_dee','o','ausr_dee','developer')`)
	rr := &routeRig{rig: r}
	rr.payments = r.team("Payments", cy)
	rr.ops = r.team("Ops", dee)
	rr.empty = r.team("Empty")
	r.exec(`UPDATE loomplane_projects SET responsible_team_id=$1 WHERE id='proj_a'`, rr.ops.ID)
	r.exec(`UPDATE loomplane_projects SET responsible_team_id=$1 WHERE id='proj_b'`, rr.empty.ID)
	return rr
}

func (r *routeRig) admins() []string {
	r.t.Helper()
	got, err := notifications.Admins(context.Background(), r.db, org)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r *routeRig) resolve(project, named, reason string) Route {
	r.t.Helper()
	got, err := r.svc.Resolve(context.Background(), r.db, org, project, named, reason)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

func sameRoute(a, b Route) bool {
	return a.TeamID == b.TeamID && a.Team == b.Team && a.Source == b.Source &&
		a.Reason == b.Reason && a.Note == b.Note && slices.Equal(a.Members, b.Members)
}

func TestResolveNamedTeam(t *testing.T) {
	r := newRouteRig(t)
	want := Route{TeamID: r.payments.ID, Team: "Payments", Source: "named", Reason: "owns billing", Members: []string{cy}}
	if got := r.resolve("proj_a", "payments", "owns billing"); !sameRoute(got, want) {
		t.Fatalf("by slug %+v, want %+v", got, want)
	}
	if got := r.resolve("proj_a", r.payments.ID, "owns billing"); !sameRoute(got, want) {
		t.Fatalf("by ID %+v, want %+v", got, want)
	}
	// Named teams work with no project too.
	if got := r.resolve("", "payments", "owns billing"); !sameRoute(got, want) {
		t.Fatalf("without a project %+v, want %+v", got, want)
	}
}

func TestResolveUnknownNamedFallsToProject(t *testing.T) {
	r := newRouteRig(t)
	want := Route{TeamID: r.ops.ID, Team: "Ops", Source: "project", Members: []string{dee}, Note: `No team named "nope" in this workspace`}
	if got := r.resolve("proj_a", "nope", "why"); !sameRoute(got, want) {
		t.Fatalf("%+v, want %+v", got, want)
	}
}

func TestResolveEmptyTeamsFallToAdmins(t *testing.T) {
	r := newRouteRig(t)
	admins := r.admins()
	if len(admins) == 0 {
		t.Fatal("no admins seeded")
	}
	want := func(note string) Route {
		return Route{Team: AdminsName, Source: "admins", Members: admins, Note: note}
	}
	for _, c := range []struct{ named, note string }{
		{"", "Empty has no members"},
		{"empty", "Empty has no members"},
		{"nope", `No team named "nope" in this workspace; Empty has no members`},
	} {
		if got := r.resolve("proj_b", c.named, "why"); !sameRoute(got, want(c.note)) {
			t.Fatalf("named %q: %+v, want %+v", c.named, got, want(c.note))
		}
	}
}

func TestResolveWithoutProjectGoesToAdmins(t *testing.T) {
	r := newRouteRig(t)
	want := Route{Team: AdminsName, Source: "admins", Members: r.admins()}
	if got := r.resolve("", "", ""); !sameRoute(got, want) {
		t.Fatalf("%+v, want %+v", got, want)
	}
}

func TestResolveIgnoresLeftoverMemberships(t *testing.T) {
	r := newRouteRig(t)
	r.exec(`DELETE FROM authsome_members WHERE id='amem_dee'`)
	want := Route{Team: AdminsName, Source: "admins", Members: r.admins(), Note: "Ops has no members"}
	if got := r.resolve("proj_a", "", ""); !sameRoute(got, want) {
		t.Fatalf("%+v, want %+v", got, want)
	}
	// Invited back under a new member row: still not a member of Ops.
	r.exec(`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES('amem_dee2','o','ausr_dee','developer')`)
	if got := r.resolve("proj_a", "", ""); !sameRoute(got, want) {
		t.Fatalf("after the re-invite %+v, want %+v", got, want)
	}
}

func TestResolveStaysInTheWorkspace(t *testing.T) {
	r := newRouteRig(t)
	// A team of workspace x, named by its slug and its ID from workspace o.
	other, err := r.svc.Create(context.Background(), security.Principal{UserID: ada, OrgID: otherOrg}, CreateTeamInput{Name: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	r.exec(`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES($1,'x',$2,'amem_xgrace',$2)`, other.ID, grace)
	// proj_a pointing at a team of another workspace (the FK allows it).
	r.exec(`UPDATE loomplane_projects SET responsible_team_id=$1 WHERE id='proj_b'`, other.ID)
	for _, named := range []string{"elsewhere", other.ID} {
		got := r.resolve("proj_b", named, "why")
		if got.Source != "admins" || got.TeamID != "" || slices.Contains(got.Members, grace) {
			t.Fatalf("named %q crossed workspaces: %+v", named, got)
		}
	}
	// A project of another workspace has no team here.
	if got := r.resolve("proj_x", "", ""); got.Source != "admins" {
		t.Fatalf("proj_x %+v", got)
	}
}

func TestResolveNeverReturnsAutomation(t *testing.T) {
	r := newRouteRig(t)
	// Rows that bypass the service: a system and an agent principal as members.
	r.exec(`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES
		('loomplane:o','app','env','sys@example.com','Sys',''),('agent:o','app','env','agent@example.com','Agent','')`)
	r.exec(`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES('amem_sys','o','loomplane:o','owner'),('amem_agent','o','agent:o','developer')`)
	r.exec(`INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES
		($1,'o','loomplane:o','amem_sys','x'),($1,'o','agent:o','amem_agent','x')`, r.payments.ID)
	got := r.resolve("proj_a", "payments", "why")
	if !slices.Equal(got.Members, []string{cy}) {
		t.Fatalf("members %v", got.Members)
	}
	// The system owner row is an admin by role; the admins route skips it.
	for _, u := range r.resolve("", "", "").Members {
		if strings.Contains(u, ":") {
			t.Fatalf("admins route returned automation %q", u)
		}
	}
}

func TestResolveReadsTheCallersTransaction(t *testing.T) {
	r := newRouteRig(t)
	ctx := context.Background()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_teams(id,org_id,name,slug,description,created_by,created_at,updated_at) VALUES('team_late','o','Late','late','','ausr_ada',now(),now())`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO loomplane_team_members(team_id,org_id,user_id,member_id,added_by) VALUES('team_late','o',$1,'amem_cy',$1)`, cy); err != nil {
		t.Fatal(err)
	}
	got, err := r.svc.Resolve(ctx, tx, org, "proj_a", "late", "why")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "named" || got.TeamID != "team_late" || !slices.Equal(got.Members, []string{cy}) {
		t.Fatalf("in the transaction %+v", got)
	}
	if got = r.resolve("proj_a", "late", "why"); got.Source != "project" {
		t.Fatalf("outside the transaction %+v", got)
	}
	if ok, err := r.svc.IsMember(ctx, tx, org, "team_late", cy); err != nil || !ok {
		t.Fatalf("IsMember in the transaction %v %v", ok, err)
	}
	if ok, err := r.svc.IsMember(ctx, r.db, org, "team_late", cy); err != nil || ok {
		t.Fatalf("IsMember outside the transaction %v %v", ok, err)
	}
}

func TestIsMember(t *testing.T) {
	r := newRouteRig(t)
	ctx := context.Background()
	check := func(orgID, teamID, user string, want bool) {
		t.Helper()
		got, err := r.svc.IsMember(ctx, r.db, orgID, teamID, user)
		if err != nil || got != want {
			t.Fatalf("IsMember(%s, %s, %s) = %v, %v; want %v", orgID, teamID, user, got, err, want)
		}
	}
	check(org, r.payments.ID, cy, true)
	check(org, r.payments.ID, dee, false)
	check(otherOrg, r.payments.ID, cy, false)
	r.exec(`DELETE FROM authsome_members WHERE id='amem_cy'`)
	check(org, r.payments.ID, cy, false)
}
